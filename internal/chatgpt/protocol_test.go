package chatgpt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"autobro/internal/mail"

	"github.com/sunls24/gox"
)

type protocolTestMail struct {
	mu           sync.Mutex
	forgotten    string
	deleted      bool
	newCalls     int
	forwardCalls int
	waitDelay    time.Duration
}

func TestProtocolAuthErrorIncludesLastStep(t *testing.T) {
	flow := &ProtocolFlow{lastStep: "等待邮箱验证码"}
	err := flow.wrapAuthError(errors.New("连接失败"))
	if got, want := err.Error(), "协议认证步骤“等待邮箱验证码”失败：连接失败"; got != want {
		t.Fatalf("wrapAuthError() = %q, want %q", got, want)
	}
}

func (m *protocolTestMail) NewAddress(context.Context, string) (string, error) {
	m.mu.Lock()
	m.newCalls++
	m.mu.Unlock()
	return "signup@example.com", nil
}

func (m *protocolTestMail) DelAddressByMetadata(context.Context, mail.AddressMetadata) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = true
	return nil
}

func (m *protocolTestMail) ForgetAddress(address string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forgotten = address
}

func (m *protocolTestMail) ForwardAddress(context.Context, string) (string, error) {
	m.mu.Lock()
	m.forwardCalls++
	m.mu.Unlock()
	return "forward@example.com", nil
}

func (m *protocolTestMail) Metadata(address string) mail.AddressMetadata {
	return mail.AddressMetadata{
		Email:    address,
		Provider: mail.AddressProviderSimpleLogin,
	}
}

func (m *protocolTestMail) WaitMailCode(ctx context.Context, _ string) <-chan gox.Result[string] {
	result := make(chan gox.Result[string], 1)
	if m.waitDelay > 0 {
		go func() {
			timer := time.NewTimer(m.waitDelay)
			defer timer.Stop()
			defer close(result)
			select {
			case <-timer.C:
				result <- gox.Result[string]{Value: "123456"}
			case <-ctx.Done():
			}
		}()
		return result
	}
	select {
	case result <- gox.Result[string]{Value: "123456"}:
	case <-ctx.Done():
	}
	close(result)
	return result
}

func TestProtocolWaitContinuesAfterResendError(t *testing.T) {
	mailProvider := &protocolTestMail{waitDelay: 3 * time.Millisecond}
	flow := NewProtocol(
		WithProtocolIMail(mailProvider),
		WithProtocolMailCodeInterval(time.Millisecond),
	)
	code, err := flow.waitProtocolMailCode(context.Background(), "forward@example.com", func(context.Context) error {
		return errors.New("resend rejected")
	})
	if err != nil {
		t.Fatalf("waitProtocolMailCode() error = %v", err)
	}
	if code != "123456" {
		t.Fatalf("code = %q, want 123456", code)
	}
}

type protocolTestSentinel struct {
	mu       sync.Mutex
	requests []SentinelRequest
}

func (p *protocolTestSentinel) Prepare(_ context.Context, request SentinelRequest) (SentinelProof, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.mu.Unlock()
	return SentinelProof{
		Token:   "fixture-token",
		SOToken: "fixture-so-token",
	}, nil
}

func TestProtocolRegistrationSequence(t *testing.T) {
	var (
		server  *httptest.Server
		mu      sync.Mutex
		paths   []string
		otpBody string
	)
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()

		switch r.URL.Path {
		case "/":
			http.SetCookie(w, &http.Cookie{Name: "root-cookie", Value: "root", Path: "/"})
			w.Header().Set("Location", server.URL+"/about-you")
			w.WriteHeader(http.StatusOK)
		case "/unauth-mweb/auth/handoff":
			w.WriteHeader(http.StatusNoContent)
		case "/auth/login_with":
			w.WriteHeader(http.StatusOK)
		case "/api/auth/providers":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"openai": map[string]string{"id": "openai"}})
		case "/api/auth/csrf":
			http.SetCookie(w, &http.Cookie{Name: "csrf-cookie", Value: "csrf", Path: "/"})
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"csrfToken": "fixture-csrf"})
		case "/api/auth/signin/openai":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"url": server.URL + "/api/accounts/authorize?state=fixture",
			})
		case "/api/accounts/authorize":
			http.SetCookie(w, &http.Cookie{Name: "authorize-cookie", Value: "authorize", Path: "/"})
			http.Redirect(w, r, "/email-verification", http.StatusFound)
		case "/email-verification":
			w.Header().Set("x-openai-document-navigation-id", "fixture-navigation")
			w.WriteHeader(http.StatusOK)
		case "/about-you":
			w.WriteHeader(http.StatusOK)
		case "/api/accounts/email-otp/validate":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read body", http.StatusInternalServerError)
				return
			}
			mu.Lock()
			otpBody = string(body)
			mu.Unlock()
			if r.Header.Get("OpenAI-Sentinel-Token") != "fixture-token" ||
				r.Header.Get("OpenAI-Sentinel-SO-Token") != "fixture-so-token" ||
				r.Header.Get("X-OpenAI-Document-Navigation-ID") != "fixture-navigation" {
				http.Error(w, "missing auth headers", http.StatusBadRequest)
				return
			}
			w.Header().Set("Location", server.URL+"/about-you")
			w.WriteHeader(http.StatusOK)
		case "/api/accounts/create_account":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid body", http.StatusBadRequest)
				return
			}
			if body["name"] == "" || body["birthdate"] == "" {
				http.Error(w, "missing account fields", http.StatusBadRequest)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "session-candidate", Value: "candidate", Path: "/"})
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"page": map[string]any{
					"type": "external_url",
					"payload": map[string]string{
						"url": server.URL + "/api/auth/callback/openai?code=fixture-code&scope=openid&state=fixture-state",
					},
				},
			})
		case "/api/auth/callback/openai":
			http.SetCookie(w, &http.Cookie{Name: "callback-cookie", Value: "callback", Path: "/"})
			http.Redirect(w, r, "/", http.StatusFound)
		case "/api/auth/session/":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"accessToken": "fixture-access-token"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	mailProvider := &protocolTestMail{}
	sentinel := &protocolTestSentinel{}
	flow := NewProtocol(
		WithProtocolIMail(mailProvider),
		WithProtocolEndpoints(server.URL, server.URL, server.URL),
		WithProtocolSentinelProvider(sentinel),
		WithProtocolMailCodeTimeout(time.Second),
	)
	account, err := flow.RegisterOrLogin(context.Background(), nil)
	if err != nil {
		t.Fatalf("RegisterOrLogin() error = %v", err)
	}
	if account.Email != "signup@example.com" || account.AccessToken != "fixture-access-token" {
		t.Fatalf("account = %+v, want fixture email and token", account)
	}
	if strings.TrimSpace(otpBody) != "{\"code\":\"123456\"}" {
		t.Fatalf("otp body = %q", otpBody)
	}

	wantPaths := []string{
		"/",
		"/unauth-mweb/auth/handoff",
		"/auth/login_with",
		"/api/auth/providers",
		"/api/auth/csrf",
		"/api/auth/signin/openai",
		"/api/accounts/authorize",
		"/email-verification",
		"/api/accounts/email-otp/validate",
		"/about-you",
		"/api/accounts/create_account",
		"/api/auth/callback/openai",
		"/",
		"/api/auth/session/",
	}
	mu.Lock()
	gotPaths := append([]string(nil), paths...)
	mu.Unlock()
	if len(gotPaths) != len(wantPaths) {
		t.Fatalf("request count = %d, want %d (%v)", len(gotPaths), len(wantPaths), gotPaths)
	}
	for i := range wantPaths {
		if gotPaths[i] != wantPaths[i] {
			t.Fatalf("request %d = %q, want %q; all paths = %v", i, gotPaths[i], wantPaths[i], gotPaths)
		}
	}
	if mailProvider.forgotten != account.Email || mailProvider.deleted {
		t.Fatalf("mail lifecycle = forgotten %q, deleted %v", mailProvider.forgotten, mailProvider.deleted)
	}
	sentinel.mu.Lock()
	sentinelCount := len(sentinel.requests)
	sentinelFlows := make([]string, 0, sentinelCount)
	for _, request := range sentinel.requests {
		sentinelFlows = append(sentinelFlows, request.Flow)
	}
	sentinel.mu.Unlock()
	if sentinelCount != 2 {
		t.Fatalf("sentinel calls = %d, want 2", sentinelCount)
	}
	if strings.Join(sentinelFlows, ",") != protocolSentinelFlowEmailOTPValidate+","+protocolSentinelFlowOAuthCreateAccount {
		t.Fatalf("sentinel flows = %v, want email_otp_validate,oauth_create_account", sentinelFlows)
	}
}

type protocolRuntimeTokenSentinel struct {
	request SentinelRequest
}

func (p *protocolRuntimeTokenSentinel) Prepare(_ context.Context, request SentinelRequest) (SentinelProof, error) {
	p.request = request
	return SentinelProof{Token: "runtime-token", SOToken: "runtime-so-token"}, nil
}

type directSentinelRuntimeProof struct{}

func (directSentinelRuntimeProof) Prepare(context.Context, SentinelRequest) (SentinelProof, error) {
	return SentinelProof{}, ErrSentinelRuntimeProof
}

func TestProtocolClassifiesDirectSentinelRuntimeProof(t *testing.T) {
	flow := NewProtocol(WithProtocolSentinelProvider(directSentinelRuntimeProof{}))
	session, err := flow.newProtocolSession()
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.prepareSentinel(context.Background(), protocolSentinelFlowEmailOTPValidate)
	if !errors.Is(err, ErrSentinelRuntimeProof) {
		t.Fatalf("prepareSentinel() error = %v, want ErrSentinelRuntimeProof", err)
	}
	if errors.Is(err, ErrSentinelUnsupported) {
		t.Fatalf("prepareSentinel() error = %v, should not add ErrSentinelUnsupported", err)
	}
	if !strings.Contains(err.Error(), "dynamic Sentinel runtime proof is required") {
		t.Fatalf("prepareSentinel() error = %v, want runtime proof message", err)
	}
}

func TestHTTPSentinelProviderBuildsTokenEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (strings.HasSuffix(r.URL.Path, ".js") || r.URL.Path == "/backend-api/sentinel/frame.html") {
			if r.URL.Path == "/backend-api/sentinel/frame.html" {
				http.SetCookie(w, &http.Cookie{Name: "prefetch-cookie", Value: "yes", Path: "/"})
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/backend-api/sentinel/req" {
			t.Fatalf("request = %s %s, want POST /backend-api/sentinel/req", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "text/plain;charset=UTF-8" {
			t.Fatalf("content type = %q", got)
		}
		if got := r.Header.Get("Origin"); got != "http://"+r.Host {
			t.Fatalf("origin = %q", got)
		}
		wantReferer := "http://" + r.Host + "/backend-api/sentinel/frame.html?sv=" + protocolSentinelVersion
		if got := r.Header.Get("Referer"); got != wantReferer {
			t.Fatalf("referer = %q, want %q", got, wantReferer)
		}
		if cookie, err := r.Cookie("oai-did"); err != nil || cookie.Value != "fixture-did" {
			t.Fatalf("oai-did cookie = %v, want fixture-did", err)
		}
		if cookie, err := r.Cookie("prefetch-cookie"); err != nil || cookie.Value != "yes" {
			t.Fatalf("prefetch cookie = %v, want yes", err)
		}
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request["id"] != "fixture-did" || request["flow"] != "fixture-flow" || request["p"] != "fixture-payload" {
			t.Fatalf("sentinel request fields = %v", request)
		}
		http.SetCookie(w, &http.Cookie{Name: "sentinel-cookie", Value: "updated", Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"token":"fixture-server-token","proofofwork":{"required":false},"turnstile":{"required":false},"so":{"required":false}}`)
	}))
	defer server.Close()

	provider := &httpSentinelProvider{client: server.Client()}
	proof, err := provider.Prepare(context.Background(), SentinelRequest{
		Endpoint:  server.URL + "/backend-api/sentinel/req",
		Flow:      "fixture-flow",
		DeviceID:  "fixture-device",
		Payload:   "fixture-payload",
		UserAgent: "fixture-agent",
		Cookies: []*http.Cookie{{
			Name:  "oai-did",
			Value: "fixture-did",
		}},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if proof.SOToken != "" || len(proof.Cookies) != 2 {
		t.Fatalf("proof metadata = %+v", proof)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(proof.Token), &envelope); err != nil {
		t.Fatalf("decode token envelope: %v", err)
	}
	if envelope["id"] != "fixture-did" || envelope["flow"] != "fixture-flow" || envelope["c"] != "fixture-server-token" {
		t.Fatalf("token envelope = %v", envelope)
	}
	if value, ok := envelope["t"]; !ok || value != nil {
		t.Fatalf("token envelope turnstile = %#v, want null", value)
	}
}

func TestSentinelFrameURLUsesObservedVersion(t *testing.T) {
	got := sentinelFrameURL("https://sentinel.openai.com/backend-api/sentinel/req", []string{
		"https://sentinel.openai.com/sentinel/observed-version/sdk.js",
	})
	want := "https://sentinel.openai.com/backend-api/sentinel/frame.html?sv=observed-version"
	if got != want {
		t.Fatalf("sentinelFrameURL() = %q, want %q", got, want)
	}
}

func TestHTTPSentinelProviderStopsAtRuntimeProofBoundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"token":"fixture-server-token","proofofwork":{"required":false},"turnstile":{"required":true,"dx":"fixture-dx"},"so":{"required":true}}`)
	}))
	defer server.Close()

	provider := &httpSentinelProvider{client: server.Client()}
	_, err := provider.Prepare(context.Background(), SentinelRequest{
		Endpoint: server.URL,
		Flow:     "fixture-flow",
		DeviceID: "fixture-device",
		Payload:  "fixture-payload",
		Cookies:  []*http.Cookie{{Name: "oai-did", Value: "fixture-did"}},
	})
	if !errors.Is(err, ErrSentinelUnsupported) {
		t.Fatalf("Prepare() error = %v, want ErrSentinelUnsupported", err)
	}
	if !errors.Is(err, ErrSentinelRuntimeProof) {
		t.Fatalf("Prepare() error = %v, want ErrSentinelRuntimeProof", err)
	}
	var runtimeErr *sentinelRuntimeProofError
	if !errors.As(err, &runtimeErr) || !runtimeErr.turnstile || !runtimeErr.sessionObserver {
		t.Fatalf("runtime proof details = %+v, want Turnstile and Session Observer", runtimeErr)
	}
}

func TestHybridSentinelProviderCarriesRuntimeRequirements(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sentinel-cookie", Value: "updated", Path: "/"})
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"token":"fixture-server-token","turnstile":{"required":true},"so":{"required":true}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	runtime := &protocolRuntimeTokenSentinel{}
	provider := &hybridSentinelProvider{
		http:    &httpSentinelProvider{client: server.Client()},
		runtime: runtime,
	}
	proof, err := provider.Prepare(context.Background(), SentinelRequest{
		Endpoint: server.URL + "/backend-api/sentinel/req",
		PageURL:  server.URL + "/email-verification",
		Flow:     "fixture-flow",
		DeviceID: "fixture-device",
		Payload:  "fixture-payload",
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if proof.Token != "runtime-token" || proof.SOToken != "runtime-so-token" {
		t.Fatalf("runtime proof = %+v", proof)
	}
	if len(proof.Cookies) == 0 || proof.Cookies[0].Name != "sentinel-cookie" {
		t.Fatalf("runtime cookies = %+v", proof.Cookies)
	}
	if len(runtime.request.CookieScopes) == 0 {
		t.Fatalf("runtime cookie scopes = %+v, want preflight scope", runtime.request.CookieScopes)
	}
}

type protocolChallengeRecoverySentinel struct {
	calls int
}

func (p *protocolChallengeRecoverySentinel) Prepare(context.Context, SentinelRequest) (SentinelProof, error) {
	return SentinelProof{Token: "fixture-token"}, nil
}

func (p *protocolChallengeRecoverySentinel) RecoverChallenge(_ context.Context, target string, _ []SentinelCookieScope) ([]SentinelCookieScope, error) {
	p.calls++
	return []SentinelCookieScope{{
		URL: target,
		Cookies: []*http.Cookie{{
			Name:  "challenge-cookie",
			Value: "recovered",
		}},
	}}, nil
}

func TestProtocolRecoversExplicitChallengeOnce(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("cf-mitigated", "challenge")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if cookie, err := r.Cookie("challenge-cookie"); err != nil || cookie.Value != "recovered" {
			http.Error(w, "challenge cookie missing", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider := &protocolChallengeRecoverySentinel{}
	flow := NewProtocol(
		WithProtocolEndpoints(server.URL, server.URL, server.URL),
		WithProtocolSentinelProvider(provider),
	)
	session, err := flow.newProtocolSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.request(context.Background(), http.MethodGet, server.URL+"/", nil, nil); err != nil {
		t.Fatalf("request() error = %v", err)
	}
	if attempts != 2 || provider.calls != 1 {
		t.Fatalf("attempts = %d, recovery calls = %d, want 2 and 1", attempts, provider.calls)
	}
}

func TestSentinelRequirementsTokenShape(t *testing.T) {
	scriptSource := "https://sentinel.openai.com/sentinel/fixture/sdk.js"
	token, err := newSentinelRequirementsToken(SentinelRequest{
		Flow:          protocolSentinelFlowOAuthCreateAccount,
		SentinelID:    "fixture-sentinel-id",
		UserAgent:     "fixture-agent",
		ScriptSources: []string{scriptSource},
		DataBuild:     "fixture-build",
		CreatedAt:     time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatalf("newSentinelRequirementsToken() error = %v", err)
	}
	if !strings.HasPrefix(token, "gAAAAAC") || !strings.HasSuffix(token, "~S") {
		t.Fatalf("token prefix/suffix = %q", token[:min(len(token), 16)])
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(token, "gAAAAAC"), "~S")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode requirements token: %v", err)
	}
	var config []any
	if err := json.Unmarshal(decoded, &config); err != nil {
		t.Fatalf("decode requirements config: %v", err)
	}
	if len(config) != 25 || config[5] != scriptSource || config[6] != "fixture-build" || config[14] != "fixture-sentinel-id" {
		t.Fatalf("requirements config shape = len %d, script %#v, build %#v, id %#v", len(config), config[5], config[6], config[14])
	}
	if !containsString(protocolNavigatorValues, config[10].(string)) ||
		!containsString(protocolDocumentValues, config[11].(string)) ||
		!containsString(protocolWindowValues, config[12].(string)) {
		t.Fatalf("requirements probe fields = %#v", config[10:13])
	}
}

func TestSentinelProofOfWorkShape(t *testing.T) {
	proof, err := solveSentinelProofOfWork(context.Background(), SentinelRequest{
		SentinelID: "fixture-sentinel-id",
		UserAgent:  "fixture-agent",
		CreatedAt:  time.Now().Add(-time.Second),
	}, "fixture-seed", "ff")
	if err != nil {
		t.Fatalf("solveSentinelProofOfWork() error = %v", err)
	}
	if !strings.HasPrefix(proof, "gAAAAAB") || !strings.HasSuffix(proof, "~S") {
		t.Fatalf("proof prefix/suffix = %q", proof[:min(len(proof), 16)])
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestProtocolChallengeDoesNotExposeQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("CF-Mitigated", "challenge")
		http.Error(w, "challenge body must not be logged", http.StatusForbidden)
	}))
	defer server.Close()

	flow := NewProtocol(WithProtocolEndpoints(server.URL, server.URL, server.URL))
	session, err := flow.newProtocolSession()
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.request(context.Background(), http.MethodGet, server.URL+"/protected?secret=do-not-log", nil, nil)
	if !errors.Is(err, ErrProtocolChallenge) {
		t.Fatalf("request() error = %v, want ErrProtocolChallenge", err)
	}
	if strings.Contains(err.Error(), "do-not-log") || strings.Contains(err.Error(), "challenge body") {
		t.Fatalf("error exposes sensitive response data: %v", err)
	}
}

func TestProtocolChallengeDetectionDoesNotTreatEveryResponseAsChallenge(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		server string
		body   string
		want   bool
	}{
		{name: "ordinary cloudflare html", status: http.StatusOK, server: "cloudflare", body: "<html>normal</html>", want: false},
		{name: "rate limit json", status: http.StatusTooManyRequests, server: "cloudflare", body: `{"error":"rate_limited"}`, want: false},
		{name: "cloudflare forbidden", status: http.StatusForbidden, server: "cloudflare", body: "", want: true},
		{name: "explicit marker", status: http.StatusOK, body: "<title>Just a moment...</title>", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := &http.Response{
				StatusCode: test.status,
				Header:     http.Header{"Server": {test.server}},
			}
			if got := isChallengeResponse(response, []byte(test.body)); got != test.want {
				t.Fatalf("isChallengeResponse() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestProtocolExistingAccountPasswordlessLoginSequence(t *testing.T) {
	var (
		server  *httptest.Server
		mu      sync.Mutex
		paths   []string
		otpBody string
	)
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()

		switch r.URL.Path {
		case "/":
			http.SetCookie(w, &http.Cookie{Name: "root-cookie", Value: "root", Path: "/"})
			w.WriteHeader(http.StatusOK)
		case "/unauth-mweb/auth/handoff":
			w.WriteHeader(http.StatusNoContent)
		case "/auth/login_with":
			if r.URL.Query().Get("screen_hint") != protocolScreenHintLoginOrSignup ||
				r.URL.Query().Get("login_hint") != "existing@example.com" {
				t.Fatalf("login query = %v", r.URL.Query())
			}
			w.WriteHeader(http.StatusOK)
		case "/api/auth/providers":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"openai": map[string]string{"id": "openai"}})
		case "/api/auth/csrf":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"csrfToken": "fixture-csrf"})
		case "/api/auth/signin/openai":
			if r.URL.Query().Get("screen_hint") != protocolScreenHintLoginOrSignup {
				t.Fatalf("signin query = %v", r.URL.Query())
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"url": server.URL + "/api/accounts/authorize?state=login-fixture",
			})
		case "/api/accounts/authorize":
			http.Redirect(w, r, "/email-verification", http.StatusFound)
		case "/email-verification":
			w.Header().Set("x-openai-document-navigation-id", "fixture-navigation")
			w.WriteHeader(http.StatusOK)
		case "/api/accounts/email-otp/validate":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read body", http.StatusInternalServerError)
				return
			}
			mu.Lock()
			otpBody = string(body)
			mu.Unlock()
			if r.Header.Get("OpenAI-Sentinel-Token") != "fixture-token" ||
				r.Header.Get("OpenAI-Sentinel-SO-Token") != "fixture-so-token" ||
				r.Header.Get("X-OpenAI-Document-Navigation-ID") != "fixture-navigation" {
				http.Error(w, "missing auth headers", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"page": map[string]any{
					"type": "external_url",
					"payload": map[string]string{
						"url": server.URL + "/api/auth/callback/openai?code=login-code&scope=openid&state=login-state",
					},
				},
			})
		case "/api/auth/callback/openai":
			http.Redirect(w, r, "/", http.StatusFound)
		case "/api/auth/session/":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"accessToken": "login-access-token"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	mailProvider := &protocolTestMail{}
	sentinel := &protocolTestSentinel{}
	flow := NewProtocol(
		WithProtocolIMail(mailProvider),
		WithProtocolEndpoints(server.URL, server.URL, server.URL),
		WithProtocolSentinelProvider(sentinel),
		WithProtocolMailCodeTimeout(time.Second),
	)
	account, err := flow.RegisterOrLogin(context.Background(), &Account{
		Email:       "existing@example.com",
		ForwardMail: "existing@example.com",
	})
	if err != nil {
		t.Fatalf("RegisterOrLogin() error = %v", err)
	}
	if account.AccessToken != "login-access-token" {
		t.Fatalf("access token = %q", account.AccessToken)
	}
	if strings.TrimSpace(otpBody) != "{\"code\":\"123456\"}" {
		t.Fatalf("otp body = %q", otpBody)
	}

	wantPaths := []string{
		"/",
		"/unauth-mweb/auth/handoff",
		"/auth/login_with",
		"/api/auth/providers",
		"/api/auth/csrf",
		"/api/auth/signin/openai",
		"/api/accounts/authorize",
		"/email-verification",
		"/api/accounts/email-otp/validate",
		"/api/auth/callback/openai",
		"/",
		"/api/auth/session/",
	}
	mu.Lock()
	gotPaths := append([]string(nil), paths...)
	mu.Unlock()
	if len(gotPaths) != len(wantPaths) {
		t.Fatalf("request count = %d, want %d (%v)", len(gotPaths), len(wantPaths), gotPaths)
	}
	for i := range wantPaths {
		if gotPaths[i] != wantPaths[i] {
			t.Fatalf("request %d = %q, want %q; all paths = %v", i, gotPaths[i], wantPaths[i], gotPaths)
		}
	}
	mailProvider.mu.Lock()
	newCalls, forwardCalls := mailProvider.newCalls, mailProvider.forwardCalls
	mailProvider.mu.Unlock()
	if newCalls != 0 || forwardCalls != 0 {
		t.Fatalf("mail lifecycle = new %d, forward %d; existing login must not create or rewire mail", newCalls, forwardCalls)
	}
	sentinel.mu.Lock()
	sentinelCount := len(sentinel.requests)
	sentinelFlows := make([]string, 0, sentinelCount)
	for _, request := range sentinel.requests {
		sentinelFlows = append(sentinelFlows, request.Flow)
	}
	sentinel.mu.Unlock()
	if sentinelCount != 1 || strings.Join(sentinelFlows, ",") != protocolSentinelFlowEmailOTPValidate {
		t.Fatalf("sentinel calls = %d, flows = %v", sentinelCount, sentinelFlows)
	}
}

func TestProtocolMapsDeactivatedAccountResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/email-verification?error=account_deactivated", http.StatusFound)
	}))
	defer server.Close()

	flow := NewProtocol(WithProtocolEndpoints(server.URL, server.URL, server.URL))
	session, err := flow.newProtocolSession()
	if err != nil {
		t.Fatal(err)
	}
	session.emailURL = server.URL + "/email-verification"
	session.documentNavigationID = "fixture-navigation"
	err = session.validateOTP(context.Background(), "123456", SentinelProof{Token: "fixture-token"})
	if !errors.Is(err, ErrAccountDeactivated) {
		t.Fatalf("validateOTP() error = %v, want ErrAccountDeactivated", err)
	}
	if errors.Is(err, ErrProtocolOTP) {
		t.Fatalf("validateOTP() error = %v, should not be classified as OTP failure", err)
	}
}
