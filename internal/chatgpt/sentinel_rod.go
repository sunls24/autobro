package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const (
	rodSentinelReadyTimeout = 30 * time.Second
	rodSentinelTokenTimeout = 90 * time.Second
)

// rodSentinelProvider runs the public Sentinel SDK inside the account's
// isolated browser context and returns only the runtime proof and cookies. It never
// sends business requests, fills authentication forms, or owns OTP/mail/account
// state.
type rodSentinelProvider struct {
	browser *rod.Browser
	mu      sync.Mutex
}

// NewRodSentinelProvider creates a SentinelProvider for the protocol flow.
// The supplied browser must be an account-scoped browser context. Pages are
// created and disposed for each proof request while the context remains owned
// by the account session.
func NewRodSentinelProvider(browser *rod.Browser) SentinelProvider {
	return &rodSentinelProvider{browser: browser}
}

// NewLazyHybridSentinelProvider creates the browser runtime only when the HTTP
// Sentinel response or a protected request actually requires it.
func NewLazyHybridSentinelProvider(browserFactory func() (*rod.Browser, error)) SentinelProvider {
	return &hybridSentinelProvider{
		http: newHTTPSentinelProvider().(*httpSentinelProvider),
		runtimeFactory: func() (SentinelProvider, error) {
			if browserFactory == nil {
				return nil, errors.New("browser factory is not initialized")
			}
			browser, err := browserFactory()
			if err != nil {
				return nil, err
			}
			return NewRodSentinelProvider(browser), nil
		},
	}
}

type hybridSentinelProvider struct {
	http           *httpSentinelProvider
	runtime        SentinelProvider
	runtimeFactory func() (SentinelProvider, error)
	runtimeMu      sync.Mutex
}

func (p *hybridSentinelProvider) getRuntime() (SentinelProvider, error) {
	p.runtimeMu.Lock()
	defer p.runtimeMu.Unlock()
	if p.runtime != nil {
		return p.runtime, nil
	}
	if p.runtimeFactory == nil {
		return nil, errors.New("browser runtime provider is not initialized")
	}
	runtime, err := p.runtimeFactory()
	if err != nil {
		return nil, err
	}
	if runtime == nil {
		return nil, errors.New("browser runtime provider is nil")
	}
	p.runtime = runtime
	return runtime, nil
}

func (p *hybridSentinelProvider) setHTTPClient(client *http.Client) {
	if p != nil && p.http != nil {
		p.http.client = newSentinelHTTPClient(client)
	}
}

func (p *hybridSentinelProvider) Prepare(ctx context.Context, request SentinelRequest) (SentinelProof, error) {
	proof, err := p.http.Prepare(ctx, request)
	if err == nil || !errors.Is(err, ErrSentinelRuntimeProof) {
		return proof, err
	}
	var runtimeErr *sentinelRuntimeProofError
	if errors.As(err, &runtimeErr) {
		runtimeRequest := request
		runtimeRequest.Cookies = append(append([]*http.Cookie(nil), request.Cookies...), runtimeErr.cookies...)
		runtimeRequest.CookieScopes = append([]SentinelCookieScope(nil), request.CookieScopes...)
		if len(runtimeErr.cookies) > 0 {
			runtimeRequest.CookieScopes = append(runtimeRequest.CookieScopes, SentinelCookieScope{
				URL:     request.Endpoint,
				Cookies: runtimeErr.cookies,
			})
		}
		runtimeProvider, providerErr := p.getRuntime()
		if providerErr != nil {
			return SentinelProof{}, providerErr
		}
		runtimeProof, runtimePrepareErr := runtimeProvider.Prepare(ctx, runtimeRequest)
		if runtimePrepareErr != nil {
			return SentinelProof{}, runtimePrepareErr
		}
		runtimeProof.Cookies = append(runtimeProof.Cookies, runtimeErr.cookies...)
		// The browser is used only to obtain the proof. The caller sends the
		// protected request with the protocol client after cookies are imported.
		return runtimeProof, nil
	}
	return SentinelProof{}, err
}

func (p *hybridSentinelProvider) RecoverChallenge(ctx context.Context, target string, scopes []SentinelCookieScope) ([]SentinelCookieScope, error) {
	runtime, err := p.getRuntime()
	if err != nil {
		return nil, err
	}
	provider, ok := runtime.(sentinelChallengeProvider)
	if !ok {
		return nil, errors.New("browser challenge recovery is not available")
	}
	return provider.RecoverChallenge(ctx, target, scopes)
}

func (p *rodSentinelProvider) Prepare(ctx context.Context, request SentinelRequest) (SentinelProof, error) {
	if p == nil || p.browser == nil {
		return SentinelProof{}, &rodSentinelProviderError{
			kind:  ErrSentinelUnsupported,
			stage: "browser-init",
			cause: errors.New("browser is not initialized"),
		}
	}
	if strings.TrimSpace(request.PageURL) == "" {
		return SentinelProof{}, &rodSentinelProviderError{
			kind:  ErrSentinelUnsupported,
			stage: "browser-page",
			cause: errors.New("sentinel page URL is missing"),
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	page, err := p.openRuntimePage(ctx, request)
	if err != nil {
		return SentinelProof{}, err
	}
	defer func() { _ = page.Close() }()
	runtime, err := readRuntimeProof(page, request.Flow)
	if err != nil {
		return SentinelProof{}, &rodSentinelProviderError{
			kind:  ErrSentinelRuntimeProof,
			stage: "sdk-token",
			cause: err,
		}
	}
	if strings.TrimSpace(runtime.Token) == "" {
		return SentinelProof{}, &rodSentinelProviderError{
			kind:  ErrSentinelRuntimeProof,
			stage: "sdk-result",
			cause: errors.New("SentinelSDK.token returned an empty proof"),
		}
	}

	cookies, err := page.Cookies([]string{request.Endpoint})
	if err != nil {
		return SentinelProof{}, &rodSentinelProviderError{
			kind:  ErrSentinelRuntimeProof,
			stage: "browser-cookies-export",
			cause: err,
		}
	}
	cookieScopes, err := browserCookieScopes(page, request.CookieScopes)
	if err != nil {
		return SentinelProof{}, &rodSentinelProviderError{
			kind:  ErrSentinelRuntimeProof,
			stage: "browser-cookies-export",
			cause: err,
		}
	}
	return SentinelProof{
		Token:        runtime.Token,
		SOToken:      runtime.SOToken,
		Cookies:      networkCookiesToHTTP(cookies),
		CookieScopes: cookieScopes,
	}, nil
}

func (p *rodSentinelProvider) RecoverChallenge(ctx context.Context, target string, scopes []SentinelCookieScope) ([]SentinelCookieScope, error) {
	if p == nil || p.browser == nil {
		return nil, errors.New("browser is not initialized")
	}
	if strings.TrimSpace(target) == "" {
		return nil, errors.New("challenge URL is missing")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	page, err := p.openChallengePage(ctx, target, scopes)
	if err != nil {
		return nil, err
	}
	defer func() { _ = page.Close() }()

	if err = page.Timeout(rodSentinelReadyTimeout).Wait(rod.Eval(`() => {
		const text = (document.title + " " + (document.body?.innerText || "")).toLowerCase();
		return document.readyState === "complete" &&
			!text.includes("just a moment") &&
			!text.includes("checking your browser") &&
			!text.includes("enable javascript and cookies");
	}`)); err != nil {
		return nil, fmt.Errorf("browser challenge did not clear: %w", err)
	}
	return browserCookieScopes(page, scopes)
}

func (p *rodSentinelProvider) openRuntimePage(ctx context.Context, request SentinelRequest) (*rod.Page, error) {
	if cookieParams := browserCookieParams(request.CookieScopes); len(cookieParams) > 0 {
		if err := p.browser.SetCookies(cookieParams); err != nil {
			return nil, &rodSentinelProviderError{
				kind:  ErrSentinelUnsupported,
				stage: "browser-cookies",
				cause: err,
			}
		}
	}
	page, err := p.browser.Context(ctx).Page(proto.TargetCreateTarget{
		Background: true,
	})
	if err != nil {
		return nil, &rodSentinelProviderError{
			kind:  ErrSentinelUnsupported,
			stage: "browser-page",
			cause: err,
		}
	}
	closePage := true
	defer func() {
		if closePage {
			_ = page.Close()
		}
	}()
	if strings.TrimSpace(request.UserAgent) != "" {
		if err = page.SetUserAgent(&proto.NetworkSetUserAgentOverride{
			UserAgent:      request.UserAgent,
			AcceptLanguage: "zh-CN,zh;q=0.9,en;q=0.8",
			Platform:       "MacIntel",
		}); err != nil {
			return nil, &rodSentinelProviderError{
				kind:  ErrSentinelUnsupported,
				stage: "browser-user-agent",
				cause: err,
			}
		}
	}
	if err = page.Navigate(request.PageURL); err != nil {
		return nil, &rodSentinelProviderError{
			kind:  ErrSentinelUnsupported,
			stage: "browser-navigate",
			cause: err,
		}
	}
	if err = page.WaitLoad(); err != nil {
		return nil, &rodSentinelProviderError{
			kind:  ErrSentinelUnsupported,
			stage: "browser-page-load",
			cause: err,
		}
	}
	if err = page.Timeout(rodSentinelReadyTimeout).Wait(rod.Eval(`() => {
		const attempts = window.__sentinel_script_loads?.attempts || [];
		return attempts.some((attempt) => attempt.state === "loaded") &&
			typeof window.SentinelSDK?.token === "function";
	}`)); err != nil {
		return nil, &rodSentinelProviderError{
			kind:  ErrSentinelRuntimeProof,
			stage: "sdk-load",
			cause: err,
		}
	}
	closePage = false
	return page, nil
}

func (p *rodSentinelProvider) openChallengePage(ctx context.Context, target string, scopes []SentinelCookieScope) (*rod.Page, error) {
	if cookieParams := browserCookieParams(scopes); len(cookieParams) > 0 {
		if err := p.browser.SetCookies(cookieParams); err != nil {
			return nil, fmt.Errorf("import challenge cookies: %w", err)
		}
	}
	page, err := p.browser.Context(ctx).Page(proto.TargetCreateTarget{
		Background: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open challenge page: %w", err)
	}
	closePage := true
	defer func() {
		if closePage {
			_ = page.Close()
		}
	}()
	if err = page.Navigate(target); err != nil {
		return nil, fmt.Errorf("navigate challenge page: %w", err)
	}
	if err = page.WaitLoad(); err != nil {
		return nil, fmt.Errorf("load challenge page: %w", err)
	}
	closePage = false
	return page, nil
}

func readRuntimeProof(page *rod.Page, flow string) (browserSentinelResult, error) {
	result, err := page.Timeout(rodSentinelTokenTimeout).Eval(`async (flow) => {
		const sdk = window.SentinelSDK;
		const stringify = (value) => {
			if (value == null) return "";
			return typeof value === "string" ? value : JSON.stringify(value);
		};
		const token = stringify(await sdk.token(flow));
		if (!token) throw new Error("SentinelSDK.token returned an empty proof");
		let so = "";
		if (typeof sdk.sessionObserverToken === "function") {
			const deadline = Date.now() + 10000;
			while (Date.now() < deadline && !so) {
				so = stringify(await sdk.sessionObserverToken(flow));
				if (!so) await new Promise((resolve) => setTimeout(resolve, 250));
			}
		}
		return JSON.stringify({token, so});
	}`, flow)
	if err != nil {
		return browserSentinelResult{}, err
	}
	var runtime browserSentinelResult
	if err = json.Unmarshal([]byte(result.Value.String()), &runtime); err != nil {
		return browserSentinelResult{}, err
	}
	return runtime, nil
}

type browserSentinelResult struct {
	Token   string `json:"token"`
	SOToken string `json:"so"`
}

type rodSentinelProviderError struct {
	kind  error
	stage string
	cause error
}

func (e *rodSentinelProviderError) Error() string {
	if e == nil {
		return ""
	}
	if errors.Is(e.kind, ErrSentinelRuntimeProof) {
		return "browser Sentinel runtime proof failed at " + e.stage
	}
	return "browser Sentinel runtime provider unavailable at " + e.stage
}

func (e *rodSentinelProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return errors.Join(e.kind, e.cause)
}

func browserCookieParams(scopes []SentinelCookieScope) []*proto.NetworkCookieParam {
	params := make([]*proto.NetworkCookieParam, 0)
	seen := make(map[string]struct{})
	for _, scope := range scopes {
		if strings.TrimSpace(scope.URL) == "" {
			continue
		}
		for _, cookie := range scope.Cookies {
			if cookie == nil || strings.TrimSpace(cookie.Name) == "" {
				continue
			}
			key := scope.URL + "\x00" + cookie.Name + "\x00" + cookie.Value
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			param := &proto.NetworkCookieParam{
				Name:     cookie.Name,
				Value:    cookie.Value,
				URL:      scope.URL,
				Path:     cookie.Path,
				Secure:   cookie.Secure,
				HTTPOnly: cookie.HttpOnly,
			}
			if cookie.Domain != "" {
				param.Domain = cookie.Domain
				param.URL = ""
			}
			if !cookie.Expires.IsZero() {
				param.Expires = proto.TimeSinceEpoch(float64(cookie.Expires.Unix()))
			}
			params = append(params, param)
		}
	}
	return params
}

func networkCookiesToHTTP(cookies []*proto.NetworkCookie) []*http.Cookie {
	result := make([]*http.Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie == nil || strings.TrimSpace(cookie.Name) == "" {
			continue
		}
		converted := &http.Cookie{
			Name:     cookie.Name,
			Value:    cookie.Value,
			Domain:   cookie.Domain,
			Path:     cookie.Path,
			Secure:   cookie.Secure,
			HttpOnly: cookie.HTTPOnly,
		}
		if cookie.Expires > 0 {
			converted.Expires = cookie.Expires.Time()
		}
		result = append(result, converted)
	}
	return result
}

func browserCookieScopes(page *rod.Page, scopes []SentinelCookieScope) ([]SentinelCookieScope, error) {
	result := make([]SentinelCookieScope, 0, len(scopes))
	seen := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		if strings.TrimSpace(scope.URL) == "" {
			continue
		}
		if _, ok := seen[scope.URL]; ok {
			continue
		}
		seen[scope.URL] = struct{}{}
		cookies, err := page.Cookies([]string{scope.URL})
		if err != nil {
			return nil, err
		}
		converted := networkCookiesToHTTP(cookies)
		if len(converted) == 0 {
			continue
		}
		result = append(result, SentinelCookieScope{
			URL:     scope.URL,
			Cookies: converted,
		})
	}
	return result, nil
}
