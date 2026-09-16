package chatgpt

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mathrand "math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"autobro/internal/logging"
	"autobro/internal/mail"
)

const (
	protocolUserAgent        = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36"
	protocolClientVersion    = "prod-c4ad2074065cc40142f2fa2e09294009480c7d3f"
	protocolClientBuild      = "10547157"
	protocolResponseMaxBytes = 8 << 20
)

var (
	ErrProtocolBootstrap    = errors.New("protocol bootstrap failed")
	ErrProtocolCSRF         = errors.New("protocol csrf failed")
	ErrProtocolOAuth        = errors.New("protocol oauth failed")
	ErrProtocolChallenge    = errors.New("protocol challenge")
	ErrSentinelUnsupported  = errors.New("sentinel protocol provider unavailable")
	ErrSentinelRuntimeProof = errors.New("sentinel runtime proof required")
	ErrProtocolOTP          = errors.New("protocol otp failed")
	ErrProtocolAccount      = errors.New("protocol account creation failed")
	ErrProtocolSession      = errors.New("protocol session failed")
)

// ProtocolError contains only safe request metadata. It never includes a response
// body or a URL query, because both may contain credentials or one-time values.
type ProtocolError struct {
	Stage      string
	URL        string
	StatusCode int
	Message    string
	Cause      error
}

func (e *ProtocolError) Error() string {
	if e == nil {
		return ""
	}
	message := "chatgpt protocol error"
	if e.Stage != "" {
		message += " at " + e.Stage
	}
	if e.StatusCode != 0 {
		message += fmt.Sprintf(" (status %d)", e.StatusCode)
	}
	if e.Message != "" {
		message += ": " + e.Message
	}
	return message
}

func (e *ProtocolError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type ProtocolOption func(*ProtocolFlow)

type ProtocolFlow struct {
	m      mail.IMail
	client *http.Client

	baseURL     string
	authBaseURL string
	sentinelURL string

	userAgent     string
	clientVersion string
	clientBuild   string

	mailCodeInterval   time.Duration
	mailCodeTimeout    time.Duration
	mailCodeTimeoutSet bool

	sentinel SentinelProvider

	steps stepTracker
}

// markStep 记录当前步骤用于失败归因，并在详细级别下输出诊断行。
func (f *ProtocolFlow) markStep(step string, args ...any) {
	f.steps.mark(step, args...)
}

func (f *ProtocolFlow) wrapAuthError(err error) error {
	return f.steps.wrap(err)
}

// SentinelRequest is the dynamic input needed by the authentication Sentinel
// step. A provider must generate a fresh payload and perform the current HTTP
// exchange; historical HAR values must never be reused.
type SentinelRequest struct {
	Endpoint      string
	Flow          string
	PageURL       string
	DeviceID      string
	SentinelID    string
	Payload       string
	UserAgent     string
	ScriptSources []string
	DataBuild     string
	CreatedAt     time.Time
	Cookies       []*http.Cookie
	CookieScopes  []SentinelCookieScope
}

// SentinelCookieScope preserves the URL scope needed when a browser runtime
// provider imports the protocol session. CookieJar.Cookies intentionally
// returns host-appropriate values without retaining their source URL.
type SentinelCookieScope struct {
	URL     string
	Cookies []*http.Cookie
}

type SentinelProof struct {
	Token        string
	SOToken      string
	Cookies      []*http.Cookie
	CookieScopes []SentinelCookieScope
}

type SentinelProvider interface {
	Prepare(context.Context, SentinelRequest) (SentinelProof, error)
}

type sentinelChallengeProvider interface {
	RecoverChallenge(context.Context, string, []SentinelCookieScope) ([]SentinelCookieScope, error)
}

type unsupportedSentinelProvider struct{}

func (unsupportedSentinelProvider) Prepare(context.Context, SentinelRequest) (SentinelProof, error) {
	return SentinelProof{}, ErrSentinelUnsupported
}

func WithProtocolIMail(m mail.IMail) ProtocolOption {
	return func(f *ProtocolFlow) {
		f.m = m
	}
}

func WithProtocolHTTPClient(client *http.Client) ProtocolOption {
	return func(f *ProtocolFlow) {
		if client != nil {
			f.client = client
			if provider, ok := f.sentinel.(interface{ setHTTPClient(*http.Client) }); ok {
				provider.setHTTPClient(client)
			}
		}
	}
}

func WithProtocolEndpoints(base, auth, sentinel string) ProtocolOption {
	return func(f *ProtocolFlow) {
		if strings.TrimSpace(base) != "" {
			f.baseURL = strings.TrimRight(strings.TrimSpace(base), "/")
		}
		if strings.TrimSpace(auth) != "" {
			f.authBaseURL = strings.TrimRight(strings.TrimSpace(auth), "/")
		}
		if strings.TrimSpace(sentinel) != "" {
			f.sentinelURL = strings.TrimRight(strings.TrimSpace(sentinel), "/")
		}
	}
}

func WithProtocolSentinelProvider(provider SentinelProvider) ProtocolOption {
	return func(f *ProtocolFlow) {
		if provider == nil {
			f.sentinel = unsupportedSentinelProvider{}
			return
		}
		f.sentinel = provider
		if httpProvider, ok := provider.(interface{ setHTTPClient(*http.Client) }); ok {
			httpProvider.setHTTPClient(f.client)
		}
	}
}

func WithProtocolMailCodeTimeout(timeout time.Duration) ProtocolOption {
	return func(f *ProtocolFlow) {
		f.mailCodeTimeout = timeout
		f.mailCodeTimeoutSet = true
	}
}

func WithProtocolMailCodeInterval(interval time.Duration) ProtocolOption {
	return func(f *ProtocolFlow) {
		f.mailCodeInterval = interval
	}
}

func NewProtocol(options ...ProtocolOption) *ProtocolFlow {
	f := &ProtocolFlow{
		client:           &http.Client{Timeout: time.Minute},
		baseURL:          baseURL,
		authBaseURL:      "https://auth.openai.com",
		sentinelURL:      "https://sentinel.openai.com",
		userAgent:        protocolUserAgent,
		clientVersion:    protocolClientVersion,
		clientBuild:      protocolClientBuild,
		mailCodeInterval: defaultMailCodeInterval,
		sentinel:         newHTTPSentinelProvider(),
		steps:            stepTracker{prefix: "协议"},
	}
	for _, option := range options {
		option(f)
	}
	return f
}

// RegisterOrLogin executes registration or passwordless existing-account login
// without creating or attaching a browser.
func (f *ProtocolFlow) RegisterOrLogin(ctx context.Context, account *Account) (result *Account, err error) {
	f.steps.reset()
	defer func() {
		err = f.wrapAuthError(err)
	}()
	if f.m == nil {
		f.markStep("初始化邮箱服务")
		return nil, errors.New("缺少邮箱服务提供方")
	}
	if account != nil && strings.TrimSpace(account.Email) != "" {
		return f.loginExisting(ctx, account)
	}

	if account == nil {
		account = &Account{}
	}
	name := mail.GenerateName()
	f.markStep("创建邮箱地址")
	address, err := f.m.NewAddress(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("创建邮箱地址：%w", err)
	}
	account.Email = address
	logging.Sub("邮箱别名已创建", slog.String("email", address))
	metadata := f.m.Metadata(address)
	metadata.Email = address
	// 已创建或已停用的账号保留别名；只有创建前失败才释放地址。
	defer func() {
		if errors.Is(err, ErrAccountDeactivated) {
			f.m.ForgetAddress(account.Email)
			return
		}
		if account.AuthStage >= AuthStageAccountCreated {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if cleanupErr := f.m.DelAddressByMetadata(cleanupCtx, metadata); cleanupErr != nil {
			logAuthFailure("协议", "清理邮箱地址", cleanupErr, slog.String("email", metadata.Email))
		} else {
			logging.Sub("邮箱别名已清理", slog.String("email", metadata.Email))
		}
	}()
	account.MailProvider = metadata.Provider
	account.ProviderAddressID = metadata.AddressID
	account.ProviderOwnerID = metadata.OwnerID

	f.markStep("获取转发地址")
	account.ForwardMail, err = f.m.ForwardAddress(ctx, account.Email)
	if err != nil {
		return nil, fmt.Errorf("获取转发地址：%w", err)
	}
	logging.Sub("账号信息", slog.String("email", account.Email), slog.String("address", account.ForwardMail))

	f.markStep("建立协议会话")
	session, err := f.newProtocolSession()
	if err != nil {
		return nil, fmt.Errorf("建立协议会话：%w", err)
	}
	f.markStep("进入登录流程")
	if err = session.bootstrap(ctx, account.Email, protocolScreenHintSignup); err != nil {
		return nil, err
	}
	f.markStep("等待邮箱验证码")
	code, err := f.waitProtocolMailCode(ctx, account.ForwardMail, session.resendOTP)
	if err != nil {
		return nil, wrapProtocol("otp/wait", ErrProtocolOTP, session.emailURL, err)
	}
	f.markStep("准备校验邮箱验证码")
	proof, err := session.prepareSentinel(ctx, protocolSentinelFlowEmailOTPValidate)
	if err != nil {
		return nil, wrapProtocol("otp/sentinel", ErrProtocolOTP, session.emailURL, err)
	}
	f.markStep("校验邮箱验证码")
	if err = session.validateOTP(ctx, code, proof); err != nil {
		return nil, err
	}
	logging.SubDone("邮箱验证码已校验")
	f.markStep("进入账号资料页面")
	if err = session.openAboutYou(ctx); err != nil {
		return nil, err
	}
	f.markStep("填写账号资料")

	f.markStep("准备创建账号")
	proof, err = session.prepareSentinel(ctx, protocolSentinelFlowOAuthCreateAccount)
	if err != nil {
		return nil, wrapProtocol("account/sentinel", ErrProtocolAccount, session.aboutURL, err)
	}
	f.markStep("创建账号")
	callbackURL, err := session.createAccount(ctx, name, randomBirthdate(), proof)
	if err != nil {
		return nil, err
	}
	logging.SubDone("账号已创建")
	account.AuthStage = AuthStageAccountCreated
	f.markStep("处理登录回调")
	if err = session.followCallback(ctx, callbackURL); err != nil {
		return nil, err
	}
	f.markStep("获取访问令牌")
	account.AccessToken, err = session.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	account.AuthStage = AuthStageTokenReady
	logging.SubDone("访问令牌已获取")

	f.m.ForgetAddress(account.Email)
	return account, nil
}

func (f *ProtocolFlow) loginExisting(ctx context.Context, account *Account) (*Account, error) {
	account.Email = strings.TrimSpace(account.Email)
	if account.Email == "" {
		f.markStep("校验账号信息")
		return nil, errors.New("已有账号缺少邮箱地址")
	}
	if strings.TrimSpace(account.ForwardMail) == "" {
		f.markStep("获取转发地址")
		forwardMail, err := f.m.ForwardAddress(ctx, account.Email)
		if err != nil {
			return nil, fmt.Errorf("获取已有账号转发地址：%w", err)
		}
		account.ForwardMail = forwardMail
	}
	logging.Sub("账号信息", slog.String("email", account.Email), slog.String("address", account.ForwardMail))

	f.markStep("建立协议会话")
	session, err := f.newProtocolSession()
	if err != nil {
		return nil, fmt.Errorf("建立协议会话：%w", err)
	}
	f.markStep("进入登录流程")
	if err = session.bootstrap(ctx, account.Email, protocolScreenHintLoginOrSignup); err != nil {
		return nil, err
	}
	f.markStep("等待邮箱验证码")
	code, err := f.waitProtocolMailCode(ctx, account.ForwardMail, session.resendOTP)
	if err != nil {
		return nil, wrapProtocol("login/otp/wait", ErrProtocolOTP, session.emailURL, err)
	}
	f.markStep("准备校验邮箱验证码")
	proof, err := session.prepareSentinel(ctx, protocolSentinelFlowEmailOTPValidate)
	if err != nil {
		return nil, wrapProtocol("login/otp/sentinel", ErrProtocolOTP, session.emailURL, err)
	}
	f.markStep("校验邮箱验证码")
	callbackURL, err := session.validateOTPAndGetCallback(ctx, code, proof)
	if err != nil {
		return nil, err
	}
	logging.SubDone("邮箱验证码已校验")
	if callbackURL == "" {
		return nil, &ProtocolError{
			Stage:   "login/otp/callback",
			URL:     safeURL(session.emailURL),
			Message: "otp validation did not return a ChatGPT callback URL",
			Cause:   ErrProtocolOAuth,
		}
	}
	f.markStep("处理登录回调")
	if err = session.followCallback(ctx, callbackURL); err != nil {
		return nil, err
	}
	f.markStep("获取访问令牌")
	account.AccessToken, err = session.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	account.AuthStage = AuthStageTokenReady
	logging.SubDone("访问令牌已获取")
	f.m.ForgetAddress(account.Email)
	return account, nil
}

func randomBirthdate() string {
	age := mathrand.IntN(10) + 18
	year := time.Now().Year() - age
	month := mathrand.IntN(12) + 1
	day := mathrand.IntN(28) + 1
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day)
}

type protocolSession struct {
	flow   *ProtocolFlow
	client *http.Client
	jar    *cookiejar.Jar

	deviceID             string
	authSessionLoggingID string
	sessionID            string
	documentNavigationID string
	emailURL             string
	aboutURL             string
	createdAt            time.Time
	sentinelID           string
	scriptSources        []string
	dataBuild            string
}

const (
	protocolScreenHintSignup        = "signup"
	protocolScreenHintLoginOrSignup = "login_or_signup"
)

type protocolResponse struct {
	response *http.Response
	body     []byte
}

func (f *ProtocolFlow) newProtocolSession() (*protocolSession, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := *f.client
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	deviceID, err := newUUID()
	if err != nil {
		return nil, err
	}
	authSessionLoggingID, err := newUUID()
	if err != nil {
		return nil, err
	}
	sessionID, err := newUUID()
	if err != nil {
		return nil, err
	}
	sentinelID, err := newUUID()
	if err != nil {
		return nil, err
	}
	return &protocolSession{
		flow:                 f,
		client:               &client,
		jar:                  jar,
		deviceID:             deviceID,
		authSessionLoggingID: authSessionLoggingID,
		sessionID:            sessionID,
		sentinelID:           sentinelID,
		createdAt:            time.Now(),
	}, nil
}

func (s *protocolSession) bootstrap(ctx context.Context, email, screenHint string) error {
	rootURL := endpointURL(s.flow.baseURL, "/")
	rootParsed, err := url.Parse(rootURL)
	if err != nil {
		return wrapProtocol("bootstrap/home", ErrProtocolBootstrap, rootURL, err)
	}
	if reply, err := s.request(ctx, http.MethodGet, rootURL, nil, nil); err != nil {
		return wrapProtocol("bootstrap/home", ErrProtocolBootstrap, rootURL, err)
	} else if err = require2xx("bootstrap/home", ErrProtocolBootstrap, rootURL, reply); err != nil {
		return err
	} else {
		if cookies := s.jar.Cookies(rootParsed); len(cookies) > 0 {
			for _, cookie := range cookies {
				if cookie.Name == "oai-did" && strings.TrimSpace(cookie.Value) != "" {
					s.deviceID = cookie.Value
					break
				}
			}
		}
		s.observeSentinelResources(string(reply.body))
	}

	handoffURL := endpointURL(s.flow.baseURL, "/unauth-mweb/auth/handoff")
	handoffBody, err := json.Marshal(map[string]any{
		"authSessionLoggingId":  s.authSessionLoggingID,
		"authenticationStarted": true,
	})
	if err != nil {
		return wrapProtocol("bootstrap/handoff", ErrProtocolBootstrap, handoffURL, err)
	}
	handoffHeaders := s.headers()
	handoffHeaders.Set("Content-Type", "application/json")
	handoffHeaders.Set("Origin", originURL(s.flow.baseURL))
	handoffHeaders.Set("Referer", rootURL)
	if reply, err := s.request(ctx, http.MethodPost, handoffURL, handoffHeaders, handoffBody); err != nil {
		return wrapProtocol("bootstrap/handoff", ErrProtocolBootstrap, handoffURL, err)
	} else if err = require2xx("bootstrap/handoff", ErrProtocolBootstrap, handoffURL, reply); err != nil {
		return err
	}

	loginURL, err := s.loginURL(email, screenHint)
	if err != nil {
		return wrapProtocol("oauth/login-url", ErrProtocolOAuth, s.flow.baseURL, err)
	}
	loginHeaders := s.headers()
	loginHeaders.Set("Accept", "text/html,application/xhtml+xml")
	loginHeaders.Set("Referer", rootURL)
	if reply, err := s.request(ctx, http.MethodGet, loginURL, loginHeaders, nil); err != nil {
		return wrapProtocol("oauth/login-page", ErrProtocolOAuth, loginURL, err)
	} else if err = require2xx("oauth/login-page", ErrProtocolOAuth, loginURL, reply); err != nil {
		return err
	} else {
		s.observeSentinelResources(string(reply.body))
	}

	providersURL := endpointURL(s.flow.baseURL, "/api/auth/providers")
	providersHeaders := s.headers()
	providersHeaders.Set("Accept", "application/json")
	providersHeaders.Set("Referer", loginURL)
	providersReply, err := s.request(ctx, http.MethodGet, providersURL, providersHeaders, nil)
	if err != nil {
		return wrapProtocol("oauth/providers", ErrProtocolOAuth, providersURL, err)
	}
	if err = require2xx("oauth/providers", ErrProtocolOAuth, providersURL, providersReply); err != nil {
		return err
	}
	var providers map[string]json.RawMessage
	if err = json.Unmarshal(providersReply.body, &providers); err != nil {
		return wrapProtocol("oauth/providers", ErrProtocolOAuth, providersURL, err)
	}
	if _, ok := providers["openai"]; !ok {
		return &ProtocolError{
			Stage:   "oauth/providers",
			URL:     safeURL(providersURL),
			Message: "openai provider is missing",
			Cause:   ErrProtocolOAuth,
		}
	}

	csrfURL := endpointURL(s.flow.baseURL, "/api/auth/csrf")
	csrfHeaders := s.headers()
	csrfHeaders.Set("Accept", "application/json")
	csrfHeaders.Set("Referer", loginURL)
	csrfReply, err := s.request(ctx, http.MethodGet, csrfURL, csrfHeaders, nil)
	if err != nil {
		return wrapProtocol("oauth/csrf", ErrProtocolCSRF, csrfURL, err)
	}
	if err = require2xx("oauth/csrf", ErrProtocolCSRF, csrfURL, csrfReply); err != nil {
		return err
	}
	csrfToken, err := jsonStringField(csrfReply.body, "csrfToken")
	if err != nil {
		return wrapProtocol("oauth/csrf", ErrProtocolCSRF, csrfURL, err)
	}
	if strings.TrimSpace(csrfToken) == "" {
		return &ProtocolError{
			Stage:   "oauth/csrf",
			URL:     safeURL(csrfURL),
			Message: "csrfToken is missing",
			Cause:   ErrProtocolCSRF,
		}
	}

	signinURL, err := s.signinURL(email, screenHint)
	if err != nil {
		return wrapProtocol("oauth/signin-url", ErrProtocolOAuth, s.flow.baseURL, err)
	}
	form := url.Values{}
	form.Set("callbackUrl", rootURL)
	form.Set("csrfToken", csrfToken)
	form.Set("json", "true")
	signinHeaders := s.headers()
	signinHeaders.Set("Accept", "application/json")
	signinHeaders.Set("Content-Type", "application/x-www-form-urlencoded")
	signinHeaders.Set("Origin", originURL(s.flow.baseURL))
	signinHeaders.Set("Referer", loginURL)
	signinReply, err := s.request(ctx, http.MethodPost, signinURL, signinHeaders, []byte(form.Encode()))
	if err != nil {
		return wrapProtocol("oauth/signin", ErrProtocolOAuth, signinURL, err)
	}
	if err = require2xx("oauth/signin", ErrProtocolOAuth, signinURL, signinReply); err != nil {
		return err
	}
	authorizeURLValue, err := responseURL(signinReply.body)
	if err != nil {
		return wrapProtocol("oauth/signin", ErrProtocolOAuth, signinURL, err)
	}
	authorizeURL, err := resolveURL(s.flow.baseURL, authorizeURLValue)
	if err != nil {
		return wrapProtocol("oauth/authorize-url", ErrProtocolOAuth, signinURL, err)
	}
	if !sameOrigin(authorizeURL, s.flow.authBaseURL) {
		return &ProtocolError{
			Stage:   "oauth/authorize-url",
			URL:     safeURL(signinURL),
			Message: "authorize URL host is not auth.openai.com",
			Cause:   ErrProtocolOAuth,
		}
	}

	authorizeHeaders := s.headers()
	authorizeHeaders.Set("Accept", "text/html,application/xhtml+xml")
	authorizeHeaders.Set("Referer", loginURL)
	authorizeReply, err := s.request(ctx, http.MethodGet, authorizeURL.String(), authorizeHeaders, nil)
	if err != nil {
		return wrapProtocol("oauth/authorize", ErrProtocolOAuth, authorizeURL.String(), err)
	}
	if err = requireStatus("oauth/authorize", ErrProtocolOAuth, authorizeURL.String(), authorizeReply, http.StatusFound); err != nil {
		return err
	}
	emailURLValue := authorizeReply.response.Header.Get("Location")
	emailURLParsed, err := resolveURL(s.flow.authBaseURL, emailURLValue)
	if err != nil {
		return wrapProtocol("oauth/email-url", ErrProtocolOAuth, authorizeURL.String(), err)
	}
	if !sameOrigin(emailURLParsed, s.flow.authBaseURL) {
		return &ProtocolError{
			Stage:   "oauth/email-url",
			URL:     safeURL(authorizeURL.String()),
			Message: "email verification URL host is not auth.openai.com",
			Cause:   ErrProtocolOAuth,
		}
	}
	s.emailURL = emailURLParsed.String()

	emailHeaders := s.headers()
	emailHeaders.Set("Accept", "text/html,application/xhtml+xml")
	emailHeaders.Set("Referer", authorizeURL.String())
	emailReply, err := s.request(ctx, http.MethodGet, s.emailURL, emailHeaders, nil)
	if err != nil {
		return wrapProtocol("oauth/email-verification", ErrProtocolOAuth, s.emailURL, err)
	}
	if err = require2xx("oauth/email-verification", ErrProtocolOAuth, s.emailURL, emailReply); err != nil {
		return err
	}
	s.observeSentinelResources(string(emailReply.body))
	s.documentNavigationID = strings.TrimSpace(emailReply.response.Header.Get("x-openai-document-navigation-id"))
	if s.documentNavigationID == "" {
		return &ProtocolError{
			Stage:   "oauth/email-verification",
			URL:     safeURL(s.emailURL),
			Message: "x-openai-document-navigation-id is missing",
			Cause:   ErrProtocolOAuth,
		}
	}
	s.aboutURL = endpointURL(s.flow.authBaseURL, "/about-you")
	return nil
}

func (s *protocolSession) loginURL(email, screenHint string) (string, error) {
	u, err := url.Parse(endpointURL(s.flow.baseURL, "/auth/login_with"))
	if err != nil {
		return "", err
	}
	query := u.Query()
	query.Set("callback_path", "/")
	query.Set("screen_hint", screenHint)
	query.Set("login_hint", email)
	query.Set("auth_session_logging_id", s.authSessionLoggingID)
	query.Set("ext-oai-did", s.deviceID)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (s *protocolSession) signinURL(email, screenHint string) (string, error) {
	u, err := url.Parse(endpointURL(s.flow.baseURL, "/api/auth/signin/openai"))
	if err != nil {
		return "", err
	}
	query := u.Query()
	query.Set("prompt", "login")
	query.Set("screen_hint", screenHint)
	query.Set("ext-oai-did", s.deviceID)
	query.Set("auth_session_logging_id", s.authSessionLoggingID)
	query.Set("login_hint", email)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (s *protocolSession) resendOTP(ctx context.Context) error {
	if s.emailURL == "" {
		return errors.New("email verification URL is not initialized")
	}
	target := endpointURL(s.flow.authBaseURL, "/api/accounts/email-otp/resend")
	invocationID, err := newUUID()
	if err != nil {
		return err
	}
	headers := s.headers()
	headers.Set("Accept", "application/json")
	headers.Set("Origin", originURL(s.flow.authBaseURL))
	headers.Set("Referer", s.emailURL)
	headers.Set("X-Access-Flow-Invocation-ID", invocationID)
	headers.Set("X-OpenAI-Document-Navigation-ID", s.documentNavigationID)
	reply, err := s.request(ctx, http.MethodPost, target, headers, nil)
	if err != nil {
		return err
	}
	return require2xx("otp/resend", ErrProtocolOTP, target, reply)
}

// waitProtocolMailCode 等待转发邮箱收到验证码。等待与重发策略由 waitForMailCode 提供。
func (f *ProtocolFlow) waitProtocolMailCode(ctx context.Context, forwardMail string, resend func(context.Context) error) (string, error) {
	code, err := waitForMailCode(ctx, f.m, forwardMail, mailCodeWait{
		interval:   f.mailCodeInterval,
		timeout:    f.mailCodeTimeout,
		timeoutSet: f.mailCodeTimeoutSet,
	}, resend)
	if err != nil {
		return "", err
	}
	f.markStep("收到邮箱验证码")
	return code, nil
}

func (s *protocolSession) prepareSentinel(ctx context.Context, flowName string) (SentinelProof, error) {
	endpoint := endpointURL(s.flow.sentinelURL, "/backend-api/sentinel/req")
	endpointURLParsed, err := url.Parse(endpoint)
	if err != nil {
		return SentinelProof{}, err
	}
	cookies := append([]*http.Cookie(nil), s.client.Jar.Cookies(endpointURLParsed)...)
	pageURL := s.emailURL
	if flowName == protocolSentinelFlowOAuthCreateAccount {
		pageURL = s.aboutURL
	}
	var pageURLParsed *url.URL
	if strings.TrimSpace(pageURL) != "" {
		pageURLParsed, err = url.Parse(pageURL)
		if err != nil {
			return SentinelProof{}, err
		}
	}
	cookieScopes := s.sentinelCookieScopes(endpointURLParsed, pageURLParsed)
	sentinelRequest := SentinelRequest{
		Endpoint:      endpoint,
		Flow:          flowName,
		PageURL:       pageURL,
		DeviceID:      s.deviceID,
		SentinelID:    s.sentinelID,
		UserAgent:     s.flow.userAgent,
		ScriptSources: append([]string(nil), s.scriptSources...),
		DataBuild:     s.dataBuild,
		CreatedAt:     s.createdAt,
		Cookies:       cookies,
		CookieScopes:  cookieScopes,
	}
	proof, err := s.flow.sentinel.Prepare(ctx, sentinelRequest)
	if err != nil {
		if errors.Is(err, ErrSentinelUnsupported) || errors.Is(err, ErrSentinelRuntimeProof) {
			message := "dynamic Sentinel payload/provider is unavailable"
			if errors.Is(err, ErrSentinelRuntimeProof) {
				message = "dynamic Sentinel runtime proof is required"
			}
			return SentinelProof{}, &ProtocolError{
				Stage:   "sentinel/" + flowName,
				URL:     safeURL(endpoint),
				Message: message,
				Cause:   err,
			}
		}
		return SentinelProof{}, fmt.Errorf("prepare sentinel %s: %w", flowName, err)
	}
	if len(proof.Cookies) > 0 {
		s.client.Jar.SetCookies(endpointURLParsed, proof.Cookies)
	}
	for _, scope := range proof.CookieScopes {
		target, parseErr := url.Parse(scope.URL)
		if parseErr != nil || target.Scheme == "" || target.Host == "" {
			return SentinelProof{}, fmt.Errorf("apply sentinel browser cookies: invalid cookie scope")
		}
		if len(scope.Cookies) > 0 {
			s.client.Jar.SetCookies(target, scope.Cookies)
		}
	}
	if strings.TrimSpace(proof.Token) == "" {
		return SentinelProof{}, &ProtocolError{
			Stage:   "sentinel/" + flowName,
			URL:     safeURL(endpoint),
			Message: "sentinel proof is incomplete",
			Cause:   ErrSentinelUnsupported,
		}
	}
	return proof, nil
}

func (s *protocolSession) sentinelCookieScopes(endpoint, pageURL *url.URL) []SentinelCookieScope {
	urls := []*url.URL{endpoint, pageURL}
	for _, raw := range []string{s.flow.baseURL, s.flow.authBaseURL} {
		parsed, err := url.Parse(raw)
		if err == nil {
			urls = append(urls, parsed)
		}
	}

	scopes := make([]SentinelCookieScope, 0, len(urls))
	seen := make(map[string]struct{}, len(urls))
	for _, target := range urls {
		if target == nil || target.Scheme == "" || target.Host == "" {
			continue
		}
		key := target.Scheme + "://" + target.Host
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		cookies := s.client.Jar.Cookies(target)
		if len(cookies) == 0 {
			continue
		}
		scopes = append(scopes, SentinelCookieScope{
			URL:     target.String(),
			Cookies: cookies,
		})
	}
	return scopes
}

func (s *protocolSession) observeSentinelResources(document string) {
	sources, build := parseSentinelResources(document)
	seen := make(map[string]struct{}, len(s.scriptSources)+len(sources))
	for _, source := range s.scriptSources {
		seen[source] = struct{}{}
	}
	for _, source := range sources {
		if _, ok := seen[source]; ok {
			continue
		}
		s.scriptSources = append(s.scriptSources, source)
		seen[source] = struct{}{}
	}
	if s.dataBuild == "" && strings.TrimSpace(build) != "" {
		s.dataBuild = strings.TrimSpace(build)
	}
}

func (s *protocolSession) validateOTP(ctx context.Context, code string, proof SentinelProof) error {
	_, err := s.validateOTPResponse(ctx, code, proof)
	return err
}

func (s *protocolSession) validateOTPAndGetCallback(ctx context.Context, code string, proof SentinelProof) (string, error) {
	reply, err := s.validateOTPResponse(ctx, code, proof)
	if err != nil {
		return "", err
	}

	callback := strings.TrimSpace(reply.response.Header.Get("Location"))
	if callback == "" {
		if candidate, parseErr := responseURL(reply.body); parseErr == nil {
			callback = candidate
		}
	}
	if callback == "" {
		return "", nil
	}
	return s.normalizeCallbackURL(callback, endpointURL(s.flow.authBaseURL, "/api/accounts/email-otp/validate"))
}

func (s *protocolSession) validateOTPResponse(ctx context.Context, code string, proof SentinelProof) (protocolResponse, error) {
	target := endpointURL(s.flow.authBaseURL, "/api/accounts/email-otp/validate")
	body, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		return protocolResponse{}, wrapProtocol("otp/validate", ErrProtocolOTP, target, err)
	}
	headers, err := s.authHeaders(proof, s.emailURL)
	if err != nil {
		return protocolResponse{}, wrapProtocol("otp/validate", ErrProtocolOTP, target, err)
	}
	headers.Set("Content-Type", "application/json")
	reply, err := s.request(ctx, http.MethodPost, target, headers, body)
	if err != nil {
		return protocolResponse{}, wrapProtocol("otp/validate", ErrProtocolOTP, target, err)
	}
	if accountDeactivatedResponse(reply) {
		return protocolResponse{}, &ProtocolError{
			Stage:      "otp/validate",
			URL:        safeURL(target),
			StatusCode: reply.response.StatusCode,
			Message:    "account is deactivated",
			Cause:      ErrAccountDeactivated,
		}
	}
	if err = require2xx("otp/validate", ErrProtocolOTP, target, reply); err != nil {
		return protocolResponse{}, err
	}
	return reply, nil
}

func (s *protocolSession) openAboutYou(ctx context.Context) error {
	if strings.TrimSpace(s.aboutURL) == "" {
		return wrapProtocol("account/about-you", ErrProtocolAccount, s.emailURL, errors.New("about-you URL is not initialized"))
	}
	headers := s.headers()
	headers.Set("Accept", "text/html,application/xhtml+xml")
	headers.Set("Referer", s.emailURL)
	reply, err := s.request(ctx, http.MethodGet, s.aboutURL, headers, nil)
	if err != nil {
		return wrapProtocol("account/about-you", ErrProtocolAccount, s.aboutURL, err)
	}
	if err = require2xx("account/about-you", ErrProtocolAccount, s.aboutURL, reply); err != nil {
		return err
	}
	s.observeSentinelResources(string(reply.body))
	return nil
}

func (s *protocolSession) createAccount(ctx context.Context, name, birthdate string, proof SentinelProof) (string, error) {
	target := endpointURL(s.flow.authBaseURL, "/api/accounts/create_account")
	body, err := json.Marshal(map[string]string{
		"name":      name,
		"birthdate": birthdate,
	})
	if err != nil {
		return "", wrapProtocol("account/create", ErrProtocolAccount, target, err)
	}
	headers, err := s.authHeaders(proof, s.aboutURL)
	if err != nil {
		return "", wrapProtocol("account/create", ErrProtocolAccount, target, err)
	}
	headers.Set("Content-Type", "application/json")
	reply, err := s.request(ctx, http.MethodPost, target, headers, body)
	if err != nil {
		return "", wrapProtocol("account/create", ErrProtocolAccount, target, err)
	}
	if err = require2xx("account/create", ErrProtocolAccount, target, reply); err != nil {
		return "", err
	}
	callback, err := responseURL(reply.body)
	if err != nil {
		return "", wrapProtocol("account/create", ErrProtocolAccount, target, err)
	}
	return s.normalizeCallbackURL(callback, target)
}

func (s *protocolSession) normalizeCallbackURL(raw, target string) (string, error) {
	callbackURL, err := resolveURL(s.flow.baseURL, raw)
	if err != nil {
		return "", wrapProtocol("oauth/callback-url", ErrProtocolOAuth, target, err)
	}
	if !sameOrigin(callbackURL, s.flow.baseURL) {
		return "", &ProtocolError{
			Stage:   "oauth/callback-url",
			URL:     safeURL(target),
			Message: "callback URL host is not chatgpt.com: " + safeURL(callbackURL.String()),
			Cause:   ErrProtocolOAuth,
		}
	}
	query := callbackURL.Query()
	if strings.TrimSpace(query.Get("code")) == "" || strings.TrimSpace(query.Get("state")) == "" {
		return "", &ProtocolError{
			Stage:   "oauth/callback-url",
			URL:     safeURL(callbackURL.String()),
			Message: "callback URL is missing code or state",
			Cause:   ErrProtocolOAuth,
		}
	}
	return callbackURL.String(), nil
}

func (s *protocolSession) followCallback(ctx context.Context, callbackURL string) error {
	current := callbackURL
	for i := 0; i < 5; i++ {
		headers := s.headers()
		headers.Set("Accept", "text/html,application/xhtml+xml")
		headers.Set("Referer", endpointURL(s.flow.baseURL, "/"))
		reply, err := s.request(ctx, http.MethodGet, current, headers, nil)
		if err != nil {
			return wrapProtocol("oauth/callback", ErrProtocolOAuth, current, err)
		}
		status := reply.response.StatusCode
		if status >= 300 && status < 400 {
			location := reply.response.Header.Get("Location")
			next, err := resolveURL(current, location)
			if err != nil {
				return wrapProtocol("oauth/callback", ErrProtocolOAuth, current, err)
			}
			if !sameOrigin(next, s.flow.baseURL) {
				return &ProtocolError{
					Stage:   "oauth/callback",
					URL:     safeURL(current),
					Message: "callback redirect host is not chatgpt.com",
					Cause:   ErrProtocolOAuth,
				}
			}
			current = next.String()
			continue
		}
		if err = require2xx("oauth/callback", ErrProtocolOAuth, current, reply); err != nil {
			return err
		}
		return nil
	}
	return &ProtocolError{
		Stage:   "oauth/callback",
		URL:     safeURL(callbackURL),
		Message: "too many callback redirects",
		Cause:   ErrProtocolOAuth,
	}
}

func (s *protocolSession) accessToken(ctx context.Context) (string, error) {
	target := endpointURL(s.flow.baseURL, "/api/auth/session/")
	headers := s.headers()
	headers.Set("Accept", "application/json")
	headers.Set("Referer", endpointURL(s.flow.baseURL, "/"))
	headers.Set("OAI-Client-Version", s.flow.clientVersion)
	headers.Set("OAI-Client-Build-Number", s.flow.clientBuild)
	headers.Set("OAI-Device-ID", s.deviceID)
	headers.Set("OAI-Session-ID", s.sessionID)
	headers.Set("X-OpenAI-Target-Path", "/api/auth/session/")
	headers.Set("X-OpenAI-Target-Route", "/api/auth/session/")
	reply, err := s.request(ctx, http.MethodGet, target, headers, nil)
	if err != nil {
		return "", wrapProtocol("session/get", ErrProtocolSession, target, err)
	}
	if err = require2xx("session/get", ErrProtocolSession, target, reply); err != nil {
		return "", err
	}
	sessionToken, err := jsonStringField(reply.body, "accessToken")
	if err != nil {
		return "", wrapProtocol("session/parse", ErrProtocolSession, target, err)
	}
	if strings.TrimSpace(sessionToken) == "" {
		return "", &ProtocolError{
			Stage:   "session/parse",
			URL:     safeURL(target),
			Message: "accessToken is missing",
			Cause:   ErrProtocolSession,
		}
	}
	return sessionToken, nil
}

func (s *protocolSession) authHeaders(proof SentinelProof, referer string) (http.Header, error) {
	invocationID, err := newUUID()
	if err != nil {
		return nil, err
	}
	headers := s.headers()
	headers.Set("Accept", "application/json")
	headers.Set("Origin", originURL(s.flow.authBaseURL))
	headers.Set("Referer", referer)
	headers.Set("OpenAI-Sentinel-Token", proof.Token)
	if strings.TrimSpace(proof.SOToken) != "" {
		headers.Set("OpenAI-Sentinel-SO-Token", proof.SOToken)
	}
	headers.Set("X-Access-Flow-Invocation-ID", invocationID)
	headers.Set("X-OpenAI-Document-Navigation-ID", s.documentNavigationID)
	return headers, nil
}

func (s *protocolSession) headers() http.Header {
	headers := make(http.Header)
	headers.Set("User-Agent", s.flow.userAgent)
	headers.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	headers.Set("Cache-Control", "no-cache")
	headers.Set("Pragma", "no-cache")
	return headers
}

func (s *protocolSession) request(ctx context.Context, method, target string, headers http.Header, body []byte) (protocolResponse, error) {
	return s.requestWithChallengeRecovery(ctx, method, target, headers, body, true)
}

func (s *protocolSession) requestWithChallengeRecovery(ctx context.Context, method, target string, headers http.Header, body []byte, allowChallengeRecovery bool) (protocolResponse, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return protocolResponse{}, err
	}
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	if request.Header.Get("User-Agent") == "" {
		request.Header.Set("User-Agent", s.flow.userAgent)
	}
	if request.Header.Get("Accept") == "" {
		request.Header.Set("Accept", "*/*")
	}

	response, err := s.client.Do(request)
	if err != nil {
		return protocolResponse{}, fmt.Errorf("http %s %s: %w", method, safeURL(target), err)
	}
	if response.Body == nil {
		response.Body = io.NopCloser(strings.NewReader(""))
	}
	responseCookies := response.Cookies()
	if len(responseCookies) > 0 {
		s.jar.SetCookies(request.URL, responseCookies)
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, protocolResponseMaxBytes+1))
	closeErr := response.Body.Close()
	if readErr != nil {
		return protocolResponse{}, fmt.Errorf("read http response: %w", readErr)
	}
	if closeErr != nil {
		return protocolResponse{}, fmt.Errorf("close http response: %w", closeErr)
	}
	if len(responseBody) > protocolResponseMaxBytes {
		return protocolResponse{}, fmt.Errorf("http response exceeds %d bytes", protocolResponseMaxBytes)
	}
	if isChallengeResponse(response, responseBody) {
		if allowChallengeRecovery {
			if provider, ok := s.flow.sentinel.(sentinelChallengeProvider); ok {
				targetURL, parseErr := url.Parse(target)
				if parseErr == nil {
					logAuthWarning("协议", "恢复上游 Challenge", slog.String("url", target))
					scopes, recoverErr := provider.RecoverChallenge(ctx, target, s.sentinelCookieScopes(targetURL, nil))
					if recoverErr == nil {
						for _, scope := range scopes {
							scopeURL, scopeErr := url.Parse(scope.URL)
							if scopeErr != nil || scopeURL.Scheme == "" || scopeURL.Host == "" {
								return protocolResponse{}, errors.New("apply challenge recovery cookies: invalid cookie scope")
							}
							if len(scope.Cookies) > 0 {
								s.client.Jar.SetCookies(scopeURL, scope.Cookies)
							}
						}
						return s.requestWithChallengeRecovery(ctx, method, target, headers, body, false)
					}
					return protocolResponse{}, &ProtocolError{
						URL:        safeURL(target),
						StatusCode: response.StatusCode,
						Message:    "browser challenge recovery failed",
						Cause:      errors.Join(ErrProtocolChallenge, recoverErr),
					}
				}
			}
		}
		return protocolResponse{}, &ProtocolError{
			URL:        safeURL(target),
			StatusCode: response.StatusCode,
			Message:    "upstream returned a challenge response",
			Cause:      ErrProtocolChallenge,
		}
	}
	return protocolResponse{response: response, body: responseBody}, nil
}

func require2xx(stage string, category error, target string, reply protocolResponse) error {
	if reply.response.StatusCode >= 200 && reply.response.StatusCode < 300 {
		return nil
	}
	return statusError(stage, category, target, reply.response.StatusCode)
}

func requireStatus(stage string, category error, target string, reply protocolResponse, wanted ...int) error {
	for _, status := range wanted {
		if reply.response.StatusCode == status {
			return nil
		}
	}
	return statusError(stage, category, target, reply.response.StatusCode)
}

func statusError(stage string, category error, target string, status int) error {
	return &ProtocolError{
		Stage:      stage,
		URL:        safeURL(target),
		StatusCode: status,
		Message:    "unexpected HTTP status",
		Cause:      category,
	}
}

func wrapProtocol(stage string, category error, target string, err error) error {
	if err == nil {
		return nil
	}
	statusCode := 0
	message := err.Error()
	var protocolErr *ProtocolError
	if errors.As(err, &protocolErr) {
		statusCode = protocolErr.StatusCode
		if protocolErr.Message != "" {
			message = protocolErr.Message
		}
	}
	return &ProtocolError{
		Stage:      stage,
		URL:        safeURL(target),
		StatusCode: statusCode,
		Message:    message,
		Cause:      errors.Join(category, err),
	}
}

func jsonStringField(body []byte, field string) (string, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		return "", err
	}
	raw, ok := object[field]
	if !ok {
		return "", fmt.Errorf("%s is missing", field)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s is not a string: %w", field, err)
	}
	return value, nil
}

func responseURL(body []byte) (string, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err == nil {
		for _, field := range []string{
			"url", "redirect", "redirect_url", "redirectUrl", "callback_url", "callbackUrl",
			"next_url", "nextUrl", "continue_url", "continueUrl",
		} {
			raw, ok := object[field]
			if !ok {
				continue
			}
			var value string
			if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value), nil
			}
		}
		if page, ok := object["page"]; ok {
			var pageObject map[string]json.RawMessage
			if json.Unmarshal(page, &pageObject) == nil {
				if payload, ok := pageObject["payload"]; ok {
					var payloadObject map[string]json.RawMessage
					if json.Unmarshal(payload, &payloadObject) == nil {
						if raw, ok := payloadObject["url"]; ok {
							var value string
							if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" {
								return strings.TrimSpace(value), nil
							}
						}
					}
				}
			}
		}
	}
	var value string
	if err := json.Unmarshal(body, &value); err == nil && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), nil
	}
	return "", errors.New("response does not contain a redirect URL")
}

func resolveURL(base, raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("redirect Location is empty")
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	relative, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	resolved := baseURL.ResolveReference(relative)
	if resolved.Scheme == "" || resolved.Host == "" {
		return nil, errors.New("redirect URL is incomplete")
	}
	return resolved, nil
}

func sameOrigin(target *url.URL, configured string) bool {
	base, err := url.Parse(configured)
	if err != nil || target == nil {
		return false
	}
	return strings.EqualFold(target.Scheme, base.Scheme) && strings.EqualFold(target.Host, base.Host)
}

func endpointURL(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

func originURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	return parsed.Scheme + "://" + parsed.Host
}

func safeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "<invalid-url>"
	}
	return parsed.Scheme + "://" + parsed.Host + parsed.EscapedPath()
}

func isChallengeResponse(response *http.Response, body []byte) bool {
	if response == nil {
		return false
	}
	if strings.Contains(strings.ToLower(response.Header.Get("cf-mitigated")), "challenge") {
		return true
	}
	text := strings.ToLower(string(body))
	if strings.Contains(text, "<title>just a moment") ||
		strings.Contains(text, "<title>attention required! | cloudflare") ||
		strings.Contains(text, "cf-chl-") ||
		strings.Contains(text, "__cf_chl_") ||
		strings.Contains(text, "cf-browser-verification") {
		return true
	}
	if response.StatusCode != http.StatusForbidden && response.StatusCode != http.StatusServiceUnavailable {
		return false
	}
	return strings.Contains(strings.ToLower(response.Header.Get("server")), "cloudflare")
}
func accountDeactivatedResponse(reply protocolResponse) bool {
	if reply.response != nil {
		location := strings.ToLower(reply.response.Header.Get("Location"))
		if strings.Contains(location, "account_deactivated") || strings.Contains(location, "account-deactivated") {
			return true
		}
	}
	body := strings.ToLower(string(reply.body))
	return strings.Contains(body, "account_deactivated") || strings.Contains(body, "account deactivated")
}

func newUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	var encoded [36]byte
	hex.Encode(encoded[0:8], raw[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], raw[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], raw[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], raw[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], raw[10:16])
	return string(encoded[:]), nil
}
