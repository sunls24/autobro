package chatgpt

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	protocolSentinelFlowEmailOTPValidate   = "email_otp_validate"
	protocolSentinelFlowOAuthCreateAccount = "oauth_create_account"
	protocolSentinelVersion                = "20260810913b"
	protocolScreenWidth                    = 1470
	protocolScreenHeight                   = 956
	protocolHeapSizeLimit                  = uint64(4395630592)
	protocolHardware                       = 8
	protocolSentinelMaxTry                 = 500000
	protocolTimezone                       = "CST"
	protocolTimezoneOffsetMinutes          = -480
)

var (
	protocolScriptSourcePattern    = regexp.MustCompile(`<script[^>]+src=["']([^"']+)["']`)
	protocolScriptBuildPattern     = regexp.MustCompile(`c/[^/]*/_`)
	protocolDataBuildPattern       = regexp.MustCompile(`<html[^>]*data-build=["']([^"']*)["']`)
	protocolSentinelVersionPattern = regexp.MustCompile(`/sentinel/([^/]+)/sdk\.js(?:[?#]|$)`)
)

var (
	protocolNavigatorValues = []string{
		"appCodeName−Mozilla",
		"webdriver−false",
		"vendor−Google Inc.",
		"cookieEnabled−true",
		"product−Gecko",
		"language−zh-CN",
		"onLine−true",
		"pdfViewerEnabled−true",
		"hardwareConcurrency−8",
		"deprecatedRunAdAuctionEnforcesKAnonymity−false",
	}
	protocolDocumentValues = []string{
		"__reactContainer$fzelfjyxej8",
		"_reactListening5dehydibo78",
		"location",
		"onmouseout",
	}
	protocolWindowValues = []string{
		"window",
		"document",
		"location",
		"innerWidth",
		"innerHeight",
		"screen",
		"chrome",
		"navigator",
		"performance",
		"crypto",
		"indexedDB",
		"localStorage",
		"fetch",
	}
)

// httpSentinelProvider implements the HTTP part of the public Sentinel SDK
// protocol. The response may still require browser-runtime proofs; those are
// reported explicitly instead of being replaced with stale or synthetic data.
type httpSentinelProvider struct {
	client *http.Client
}

func (p *httpSentinelProvider) setHTTPClient(client *http.Client) {
	if p != nil {
		p.client = newSentinelHTTPClient(client)
	}
}

func newHTTPSentinelProvider() SentinelProvider {
	return &httpSentinelProvider{client: newSentinelHTTPClient(nil)}
}

func (p *httpSentinelProvider) Prepare(ctx context.Context, request SentinelRequest) (SentinelProof, error) {
	if strings.TrimSpace(request.Endpoint) == "" {
		return SentinelProof{}, errors.New("sentinel endpoint is required")
	}
	if strings.TrimSpace(request.Flow) == "" {
		return SentinelProof{}, errors.New("sentinel flow is required")
	}

	id := sentinelRequestID(request)
	if id == "" {
		return SentinelProof{}, errors.New("sentinel oai-did is missing")
	}
	payload := strings.TrimSpace(request.Payload)
	if payload == "" {
		var err error
		payload, err = newSentinelRequirementsToken(request)
		if err != nil {
			return SentinelProof{}, fmt.Errorf("generate sentinel requirements token: %w", err)
		}
	}
	cookies, resourceCookies, err := p.prefetchResources(ctx, request)
	if err != nil {
		return SentinelProof{}, err
	}

	body, err := json.Marshal(map[string]string{
		"p":    payload,
		"id":   id,
		"flow": request.Flow,
	})
	if err != nil {
		return SentinelProof{}, fmt.Errorf("encode sentinel request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, request.Endpoint, bytes.NewReader(body))
	if err != nil {
		return SentinelProof{}, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("Origin", originURL(request.Endpoint))
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Referer", sentinelFrameURL(request.Endpoint, request.ScriptSources))
	if request.UserAgent != "" {
		req.Header.Set("User-Agent", request.UserAgent)
	}
	for _, cookie := range cookies {
		if cookie != nil && cookie.Name != "" {
			req.AddCookie(cookie)
		}
	}

	client := p.client
	if client == nil {
		client = newSentinelHTTPClient(nil)
	}
	response, err := client.Do(req)
	if err != nil {
		return SentinelProof{}, fmt.Errorf("sentinel http request: %w", err)
	}
	if response.Body == nil {
		response.Body = io.NopCloser(strings.NewReader(""))
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, protocolResponseMaxBytes+1))
	closeErr := response.Body.Close()
	if readErr != nil {
		return SentinelProof{}, fmt.Errorf("read sentinel response: %w", readErr)
	}
	if closeErr != nil {
		return SentinelProof{}, fmt.Errorf("close sentinel response: %w", closeErr)
	}
	if len(responseBody) > protocolResponseMaxBytes {
		return SentinelProof{}, fmt.Errorf("sentinel response exceeds %d bytes", protocolResponseMaxBytes)
	}
	if isChallengeResponse(response, responseBody) {
		return SentinelProof{}, &ProtocolError{
			URL:        safeURL(request.Endpoint),
			StatusCode: response.StatusCode,
			Message:    "sentinel returned a challenge response",
			Cause:      ErrProtocolChallenge,
		}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return SentinelProof{}, statusError("sentinel/req", ErrSentinelUnsupported, request.Endpoint, response.StatusCode)
	}

	var prepared sentinelResponse
	if err = json.Unmarshal(responseBody, &prepared); err != nil {
		return SentinelProof{}, fmt.Errorf("decode sentinel response: %w", err)
	}
	if strings.TrimSpace(prepared.Token) == "" {
		return SentinelProof{}, errors.New("sentinel response token is missing")
	}
	proof := ""
	if prepared.ProofOfWork.Required {
		proof, err = solveSentinelProofOfWork(ctx, request, prepared.ProofOfWork.Seed, prepared.ProofOfWork.Difficulty)
		if err != nil {
			return SentinelProof{}, fmt.Errorf("solve sentinel proof of work: %w", err)
		}
	}
	if prepared.Turnstile.Required || prepared.SO.Required {
		return SentinelProof{}, &sentinelRuntimeProofError{
			turnstile:       prepared.Turnstile.Required,
			sessionObserver: prepared.SO.Required,
			cookies:         append(resourceCookies, response.Cookies()...),
		}
	}

	token, err := json.Marshal(map[string]any{
		"p":    proof,
		"t":    nil,
		"c":    prepared.Token,
		"id":   id,
		"flow": request.Flow,
	})
	if err != nil {
		return SentinelProof{}, fmt.Errorf("encode sentinel token: %w", err)
	}
	return SentinelProof{
		Token:   string(token),
		Cookies: append(resourceCookies, response.Cookies()...),
	}, nil
}

func (p *httpSentinelProvider) prefetchResources(ctx context.Context, request SentinelRequest) ([]*http.Cookie, []*http.Cookie, error) {
	resources := []string{
		endpointURL(originURL(request.Endpoint), "/backend-api/sentinel/sdk.js"),
		sentinelScriptResourceURL(request.Endpoint, request.ScriptSources),
		sentinelVersionResourceURL(request.Endpoint, request.ScriptSources),
		sentinelFrameURL(request.Endpoint, request.ScriptSources),
	}
	seen := make(map[string]struct{}, len(resources))
	cookies := append([]*http.Cookie(nil), request.Cookies...)
	resourceCookies := make([]*http.Cookie, 0)
	client := p.client
	if client == nil {
		client = newSentinelHTTPClient(nil)
	}
	for _, resource := range resources {
		if strings.TrimSpace(resource) == "" {
			continue
		}
		if _, ok := seen[resource]; ok {
			continue
		}
		seen[resource] = struct{}{}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, resource, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("build sentinel resource request: %w", err)
		}
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Cache-Control", "no-cache")
		req.Header.Set("Pragma", "no-cache")
		req.Header.Set("Referer", originURL(request.Endpoint)+"/")
		if request.UserAgent != "" {
			req.Header.Set("User-Agent", request.UserAgent)
		}
		for _, cookie := range cookies {
			if cookie != nil && cookie.Name != "" {
				req.AddCookie(cookie)
			}
		}
		response, err := client.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("sentinel resource request: %w", err)
		}
		if response.Body == nil {
			response.Body = io.NopCloser(strings.NewReader(""))
		}
		readBytes, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, protocolResponseMaxBytes+1))
		closeErr := response.Body.Close()
		if readErr != nil {
			return nil, nil, fmt.Errorf("read sentinel resource response: %w", readErr)
		}
		if closeErr != nil {
			return nil, nil, fmt.Errorf("close sentinel resource response: %w", closeErr)
		}
		if readBytes > protocolResponseMaxBytes {
			return nil, nil, fmt.Errorf("sentinel resource response exceeds %d bytes", protocolResponseMaxBytes)
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return nil, nil, statusError("sentinel/resource", ErrSentinelUnsupported, resource, response.StatusCode)
		}
		setCookies := response.Cookies()
		cookies = append(cookies, setCookies...)
		resourceCookies = append(resourceCookies, setCookies...)
	}
	return cookies, resourceCookies, nil
}

type sentinelResponse struct {
	Token       string `json:"token"`
	ProofOfWork struct {
		Required   bool   `json:"required"`
		Seed       string `json:"seed"`
		Difficulty string `json:"difficulty"`
	} `json:"proofofwork"`
	Turnstile struct {
		Required bool `json:"required"`
	} `json:"turnstile"`
	SO struct {
		Required bool `json:"required"`
	} `json:"so"`
}

type sentinelRuntimeProofError struct {
	turnstile       bool
	sessionObserver bool
	cookies         []*http.Cookie
}

func (e *sentinelRuntimeProofError) Error() string {
	return fmt.Sprintf("sentinel runtime proof required (turnstile=%t, session_observer=%t)", e.turnstile, e.sessionObserver)
}

func (e *sentinelRuntimeProofError) Unwrap() error {
	return errors.Join(ErrSentinelUnsupported, ErrSentinelRuntimeProof)
}

func sentinelRequestID(request SentinelRequest) string {
	for _, cookie := range request.Cookies {
		if cookie != nil && cookie.Name == "oai-did" && strings.TrimSpace(cookie.Value) != "" {
			return strings.TrimSpace(cookie.Value)
		}
	}
	return strings.TrimSpace(request.DeviceID)
}

func sentinelFrameURL(endpoint string, sources []string) string {
	version := protocolSentinelVersion
	for _, source := range sources {
		match := protocolSentinelVersionPattern.FindStringSubmatch(source)
		if len(match) == 2 && strings.TrimSpace(match[1]) != "" {
			version = strings.TrimSpace(match[1])
			break
		}
	}
	return endpointURL(originURL(endpoint), "/backend-api/sentinel/frame.html?sv="+version)
}

func newSentinelRequirementsToken(request SentinelRequest) (string, error) {
	seed, err := randomDecimal()
	if err != nil {
		return "", err
	}
	config, err := newSentinelConfig(request)
	if err != nil {
		return "", err
	}
	start := time.Now()
	for nonce := 0; nonce < protocolSentinelMaxTry; nonce++ {
		config[3] = nonce
		config[9] = time.Since(start).Milliseconds()
		encoded, err := marshalBase64(config)
		if err != nil {
			return "", err
		}
		if sentinelHash(seed + encoded)[:1] <= "0" {
			return "gAAAAAC" + encoded + "~S", nil
		}
	}
	return "", errors.New("sentinel requirements token could not be generated")
}

func solveSentinelProofOfWork(ctx context.Context, request SentinelRequest, seed, difficulty string) (string, error) {
	if strings.TrimSpace(seed) == "" {
		return "", errors.New("proof seed is missing")
	}
	difficulty = strings.TrimSpace(strings.ToLower(difficulty))
	if difficulty == "" || len(difficulty) > 8 {
		return "", errors.New("invalid proof difficulty")
	}
	for _, char := range difficulty {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return "", errors.New("invalid proof difficulty")
		}
	}
	config, err := newSentinelConfig(request)
	if err != nil {
		return "", err
	}
	start := time.Now()
	for nonce := 0; nonce < protocolSentinelMaxTry; nonce++ {
		if nonce%256 == 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
			}
		}
		config[3] = nonce
		config[9] = time.Since(start).Milliseconds()
		encoded, err := marshalBase64(config)
		if err != nil {
			return "", err
		}
		if sentinelHash(seed + encoded)[:len(difficulty)] <= difficulty {
			return "gAAAAAB" + encoded + "~S", nil
		}
	}
	return "", errors.New("sentinel proof could not be solved")
}

func newSentinelConfig(request SentinelRequest) ([]any, error) {
	createdAt := request.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	userAgent := request.UserAgent
	if userAgent == "" {
		userAgent = protocolUserAgent
	}
	powID := strings.TrimSpace(request.SentinelID)
	if powID == "" {
		powID = strings.TrimSpace(request.DeviceID)
	}
	if powID == "" {
		generatedID, err := newUUID()
		if err != nil {
			return nil, err
		}
		powID = generatedID
	}
	navigatorProbe, documentProbe, windowProbe, err := newSentinelProbeValues()
	if err != nil {
		return nil, fmt.Errorf("sentinel probe values: %w", err)
	}
	var dataBuild any
	if strings.TrimSpace(request.DataBuild) != "" {
		dataBuild = strings.TrimSpace(request.DataBuild)
	}
	now := time.Now()
	// 下标 3 与 9 由调用方在重试循环内分别写入 nonce 与已耗时毫秒，这里只占位。
	return []any{
		protocolScreenWidth + protocolScreenHeight,
		formatSentinelDate(now),
		protocolHeapSizeLimit,
		0,
		userAgent,
		sentinelScriptSource(request.ScriptSources),
		dataBuild,
		"zh-CN",
		"zh-CN,zh",
		0,
		navigatorProbe,
		documentProbe,
		windowProbe,
		float64(now.Sub(createdAt).Microseconds()) / 1000,
		powID,
		"",
		protocolHardware,
		float64(createdAt.UnixMicro()) / 1000,
		0, 0, 0, 0, 0, 0, 0,
	}, nil
}

func newSentinelHTTPClient(source *http.Client) *http.Client {
	if source == nil {
		return &http.Client{
			Timeout: time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	client := *source
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &client
}

func marshalBase64(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(encoded), nil
}

func sentinelHash(value string) string {
	state := uint32(2166136261)
	for index := 0; index < len(value); index++ {
		state ^= uint32(value[index])
		state *= 16777619
	}
	state ^= state >> 16
	state *= 2246822507
	state ^= state >> 13
	state *= 3266489909
	state ^= state >> 16
	return fmt.Sprintf("%08x", state)
}

func sentinelScriptSource(sources []string) string {
	defaultSource := "https://sentinel.openai.com/sentinel/" + protocolSentinelVersion + "/sdk.js"
	for _, source := range sources {
		source = strings.TrimSpace(source)
		lower := strings.ToLower(source)
		if source != "" && strings.Contains(lower, "sentinel.openai.com") && strings.Contains(lower, "/sentinel/") {
			return source
		}
	}
	return defaultSource
}

func sentinelScriptResourceURL(endpoint string, sources []string) string {
	for _, source := range sources {
		source = strings.TrimSpace(source)
		lower := strings.ToLower(source)
		if source != "" && strings.Contains(lower, "sentinel.openai.com") && strings.Contains(lower, "/sentinel/") {
			return source
		}
	}
	return endpointURL(originURL(endpoint), "/sentinel/"+protocolSentinelVersion+"/sdk.js")
}

func sentinelVersionResourceURL(endpoint string, sources []string) string {
	version := protocolSentinelVersion
	for _, source := range sources {
		match := protocolSentinelVersionPattern.FindStringSubmatch(strings.TrimSpace(source))
		if len(match) == 2 && strings.TrimSpace(match[1]) != "" {
			version = strings.TrimSpace(match[1])
			break
		}
	}
	return endpointURL(originURL(endpoint), "/sentinel/"+version+"/sdk.js")
}

func newSentinelProbeValues() (string, string, string, error) {
	navigator, err := randomChoice(protocolNavigatorValues)
	if err != nil {
		return "", "", "", err
	}
	document, err := randomChoice(protocolDocumentValues)
	if err != nil {
		return "", "", "", err
	}
	window, err := randomChoice(protocolWindowValues)
	if err != nil {
		return "", "", "", err
	}
	return navigator, document, window, nil
}

func randomChoice(values []string) (string, error) {
	index, err := randomIndex(len(values))
	if err != nil {
		return "", err
	}
	return values[index], nil
}

func randomDecimal() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	value := uint64(raw[0]) | uint64(raw[1])<<8 | uint64(raw[2])<<16 | uint64(raw[3])<<24 |
		uint64(raw[4])<<32 | uint64(raw[5])<<40 | uint64(raw[6])<<48 | uint64(raw[7])<<56
	return strconv.FormatFloat(float64(value>>11)/float64(uint64(1)<<53), 'f', -1, 64), nil
}

// randomIndex 返回 [0, size) 的随机下标。随机源失败时返回错误，不以时间派生值代替。
func randomIndex(size int) (int, error) {
	if size <= 1 {
		return 0, nil
	}
	var value uint64
	if err := binary.Read(rand.Reader, binary.LittleEndian, &value); err != nil {
		return 0, fmt.Errorf("read crypto random: %w", err)
	}
	return int(value % uint64(size)), nil
}

func formatSentinelDate(value time.Time) string {
	zone := time.FixedZone(protocolTimezone, -protocolTimezoneOffsetMinutes*60)
	return value.In(zone).Format("Mon Jan 02 2006 15:04:05") + " GMT+0800 (中国标准时间)"
}

func parseSentinelResources(document string) ([]string, string) {
	matches := protocolScriptSourcePattern.FindAllStringSubmatch(document, -1)
	sources := make([]string, 0, len(matches))
	build := ""
	for _, match := range matches {
		if len(match) == 2 && strings.TrimSpace(match[1]) != "" {
			sources = append(sources, match[1])
			if build == "" {
				build = protocolScriptBuildPattern.FindString(match[1])
			}
		}
	}
	if build == "" {
		if match := protocolDataBuildPattern.FindStringSubmatch(document); len(match) == 2 {
			build = match[1]
		}
	}
	return sources, build
}
