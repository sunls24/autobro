package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/sunls24/gox"
)

type fakeWait struct {
	code string
}

func (f *fakeWait) WaitMailCode(ctx context.Context, address string) <-chan gox.Result[string] {
	ch := make(chan gox.Result[string], 1)
	ch <- gox.Result[string]{Value: f.code}
	return ch
}

// uumailTestServer 模拟 Maxthon SSO 与 Uumail API：
//   - 验证码固定 123456，OTT 编码请求邮箱（ott-<email>）；
//   - 登录后 cookie 为 uumail_ut=tok-<email>；
//   - 用户名取自邮箱本地部分（a1@chato.eu.org → a1），别名计数按账号累计。
type uumailTestServer struct {
	sso *httptest.Server
	api *httptest.Server

	mu          sync.Mutex
	aliasLimits int64
	logins      int
	createCalls int
	created     []string // "cookie|prefix"
	deleted     []string // "cookie|email"
	counts      map[string]int64
	failCreates int // 前 N 次 addr/update 返回 err，用于重试测试
}

func newUumailTestServer(t *testing.T) *uumailTestServer {
	t.Helper()
	s := &uumailTestServer{counts: map[string]int64{}, aliasLimits: -1}
	s.sso = httptest.NewServer(http.HandlerFunc(s.handleSSO))
	s.api = httptest.NewServer(http.HandlerFunc(s.handleAPI))
	t.Cleanup(func() {
		s.sso.Close()
		s.api.Close()
	})
	return s
}

func (s *uumailTestServer) handleSSO(w http.ResponseWriter, r *http.Request) {
	requestBody := readBody(r)
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/auth/email/send-code":
		_, _ = w.Write([]byte(`{"msg":"验证码已发送"}`))
	case "/auth/email/verify-code":
		email := requestBody["email"]
		_, _ = fmt.Fprintf(w, `{"type":"email","result":{"email":%q,"OTT":"ott-%s"}}`, email, email)
	default:
		http.NotFound(w, r)
	}
}

func (s *uumailTestServer) handleAPI(w http.ResponseWriter, r *http.Request) {
	cookie := r.Header.Get("Cookie")
	email := strings.TrimPrefix(cookie, uumailCookieName+"=tok-")
	username := uumailTestUsername(email)
	requestBody := readBody(r)
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/user/login":
		ottEmail := strings.TrimPrefix(requestBody["OTT"], "ott-")
		s.mu.Lock()
		s.logins++
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: uumailCookieName, Value: "tok-" + ottEmail, Path: "/"})
		_, _ = w.Write([]byte(`{"result":{"uid":42}}`))
	case "/v1/user/info":
		if cookie == uumailCookieName+"=stale" {
			// 故意返回非 JSON 文本，覆盖 401 响应体无法解析时的归一化路径。
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("unauthorized"))
			return
		}
		s.mu.Lock()
		count := s.counts[username]
		limits := s.aliasLimits
		s.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"code":0,"result":{"name":%q,"domain":"uu.me","aliasCount":%d,"level":"plus","limits":{"aliasLimits":%d,"dailylimits":100}}}`,
			username, count, limits)
	case "/v1/addr/update":
		s.mu.Lock()
		defer s.mu.Unlock()
		s.createCalls++
		if s.failCreates > 0 {
			s.failCreates--
			_, _ = w.Write([]byte(`{"err":"alias exists"}`))
			return
		}
		s.counts[username]++
		s.created = append(s.created, cookie+"|"+requestBody["alias"])
		_, _ = w.Write([]byte(`{"code":0}`))
	case "/v1/addr/delete":
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, alias := range strings.Split(requestBody["aliases"], ",") {
			// 复现真实服务端行为：删除要求 alias 前缀，完整地址报 invalidChars。
			if strings.Contains(alias, "@") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"err":"aliases.createModal.errors.invalidChars"}`))
				return
			}
			s.deleted = append(s.deleted, cookie+"|"+alias)
			s.counts[username]--
		}
		_, _ = w.Write([]byte(`{"code":0}`))
	default:
		http.NotFound(w, r)
	}
}

func uumailTestUsername(email string) string {
	local := email
	if at := strings.LastIndex(email, "@"); at >= 0 {
		local = email[:at]
	}
	return strings.ReplaceAll(local, ".", "-")
}

func readBody(r *http.Request) map[string]string {
	raw, _ := io.ReadAll(r.Body)
	parsed := map[string]any{}
	_ = json.Unmarshal(raw, &parsed)
	result := map[string]string{}
	for key, value := range parsed {
		result[key] = fmt.Sprint(value)
	}
	return result
}

func newUumailTestProvider(t *testing.T, s *uumailTestServer, accounts ...string) IMailAddress {
	t.Helper()
	provider, err := NewUumailWithConfig(context.Background(), UumailConfig{
		Accounts:      accounts,
		SunMailAPIKey: "test-sunmail-key",
		SessionPath:   filepath.Join(t.TempDir(), "sessions.json"),
		APIBase:       s.api.URL,
		SSOBase:       s.sso.URL,
	})
	if err != nil {
		t.Fatalf("NewUumailWithConfig() error = %v", err)
	}
	u := provider.(*uumail)
	u.wait = &fakeWait{code: "123456"}
	return provider
}

func TestUumailNameToAddress(t *testing.T) {
	t.Parallel()

	pattern := regexp.MustCompile(`^[a-z0-9-_]+$`)
	for _, name := range []string{"", "Emma Smith", "Liam Noah 42"} {
		address := uumailNameToAddress(name)
		if address == "" {
			t.Fatalf("uumailNameToAddress(%q) 为空", name)
		}
		if strings.Contains(address, ".") {
			t.Fatalf("uumailNameToAddress(%q) = %q，包含点号", name, address)
		}
		if len(address) > 64 || !pattern.MatchString(address) {
			t.Fatalf("uumailNameToAddress(%q) = %q，不满足 prefix 规则", name, address)
		}
	}
}

func TestNewUumailRequiresAccountsAndKey(t *testing.T) {
	t.Parallel()

	if _, err := NewUumailWithConfig(context.Background(), UumailConfig{}); err == nil ||
		!strings.Contains(err.Error(), "UUMAIL_ACCOUNTS") {
		t.Fatalf("空账号列表应报 UUMAIL_ACCOUNTS 错误，got %v", err)
	}
	if _, err := NewUumailWithConfig(context.Background(), UumailConfig{Accounts: []string{"a@chato.eu.org"}}); err == nil ||
		!strings.Contains(err.Error(), "SUNMAIL_API_KEY") {
		t.Fatalf("缺少 SunMail Key 应报错，got %v", err)
	}
}

func TestUumailNewAddressRotatesAndDeletes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newUumailTestServer(t)
	provider := newUumailTestProvider(t, s, "a1@chato.eu.org", "a2@chato.eu.org")

	first, err := provider.NewAddress(ctx, "Emma Smith")
	if err != nil {
		t.Fatalf("NewAddress() error = %v", err)
	}
	if !strings.HasSuffix(first, "@a1.uu.me") {
		t.Fatalf("NewAddress() = %q，应归属 a1", first)
	}
	second, err := provider.NewAddress(ctx, "Liam Noah")
	if err != nil {
		t.Fatalf("NewAddress() error = %v", err)
	}
	if !strings.HasSuffix(second, "@a2.uu.me") {
		t.Fatalf("NewAddress() = %q，轮换后应归属 a2", second)
	}

	forward, err := provider.ForwardAddress(ctx, first)
	if err != nil {
		t.Fatalf("ForwardAddress() error = %v", err)
	}
	if forward != "a1@chato.eu.org" {
		t.Fatalf("ForwardAddress() = %q，应为 a1 身份地址", forward)
	}

	metadata := provider.Metadata(first)
	if metadata.Provider != AddressProviderUumail {
		t.Fatalf("Metadata().Provider = %q", metadata.Provider)
	}
	if err := provider.DelAddressByMetadata(ctx, metadata); err != nil {
		t.Fatalf("DelAddressByMetadata() error = %v", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	firstPrefix := first[:strings.Index(first, "@")]
	if len(s.deleted) != 1 || s.deleted[0] != uumailCookieName+"=tok-a1@chato.eu.org|"+firstPrefix {
		t.Fatalf("删除记录应为「a1 会话|别名前缀」：%v", s.deleted)
	}
}

func TestUumailAliasQuotaRotatesAndExhausts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newUumailTestServer(t)
	s.aliasLimits = 1
	provider := newUumailTestProvider(t, s, "a1@chato.eu.org", "a2@chato.eu.org")

	for i := 0; i < 2; i++ {
		if _, err := provider.NewAddress(ctx, "Emma Smith"); err != nil {
			t.Fatalf("NewAddress() #%d error = %v", i+1, err)
		}
	}
	if _, err := provider.NewAddress(ctx, "Emma Smith"); err == nil ||
		!strings.Contains(err.Error(), "配额均已用尽") {
		t.Fatalf("配额耗尽应报错，got %v", err)
	}
}

func TestUumailExpiredSessionRelogin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newUumailTestServer(t)
	sessionPath := filepath.Join(t.TempDir(), "sessions.json")
	if err := StoreUumailSession(sessionPath, "a1@chato.eu.org", UumailSession{Cookie: uumailCookieName + "=stale"}); err != nil {
		t.Fatalf("StoreUumailSession() error = %v", err)
	}

	provider, err := NewUumailWithConfig(ctx, UumailConfig{
		Accounts:      []string{"a1@chato.eu.org"},
		SunMailAPIKey: "test-sunmail-key",
		SessionPath:   sessionPath,
		APIBase:       s.api.URL,
		SSOBase:       s.sso.URL,
	})
	if err != nil {
		t.Fatalf("NewUumailWithConfig() error = %v", err)
	}
	u := provider.(*uumail)
	u.wait = &fakeWait{code: "123456"}

	address, err := provider.NewAddress(ctx, "Emma Smith")
	if err != nil {
		t.Fatalf("NewAddress() error = %v", err)
	}
	if !strings.HasSuffix(address, "@a1.uu.me") {
		t.Fatalf("NewAddress() = %q", address)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logins != 1 {
		t.Fatalf("过期会话应触发一次重登，logins = %d", s.logins)
	}
}

func TestUumailCreateAliasRetries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newUumailTestServer(t)
	s.failCreates = 1
	provider := newUumailTestProvider(t, s, "a1@chato.eu.org")

	address, err := provider.NewAddress(ctx, "Emma Smith")
	if err != nil {
		t.Fatalf("NewAddress() error = %v", err)
	}
	if !strings.HasSuffix(address, "@a1.uu.me") {
		t.Fatalf("NewAddress() = %q", address)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createCalls != 2 || len(s.created) != 1 {
		t.Fatalf("首次冲突后应重试一次（createCalls=%d, created=%v）", s.createCalls, s.created)
	}
}

func TestUumailSessionFilePersisted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newUumailTestServer(t)
	sessionPath := filepath.Join(t.TempDir(), "sessions.json")
	provider, err := NewUumailWithConfig(ctx, UumailConfig{
		Accounts:      []string{"a1@chato.eu.org"},
		SunMailAPIKey: "test-sunmail-key",
		SessionPath:   sessionPath,
		APIBase:       s.api.URL,
		SSOBase:       s.sso.URL,
	})
	if err != nil {
		t.Fatalf("NewUumailWithConfig() error = %v", err)
	}
	u := provider.(*uumail)
	u.wait = &fakeWait{code: "123456"}

	if _, err = provider.NewAddress(ctx, "Emma Smith"); err != nil {
		t.Fatalf("NewAddress() error = %v", err)
	}
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("会话缓存未写入：%v", err)
	}
	if !strings.Contains(string(data), `"a1@chato.eu.org"`) || strings.Contains(string(data), "stale") {
		t.Fatalf("会话缓存内容异常：%s", data)
	}
	info, err := os.Stat(sessionPath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("会话缓存权限 = %v，应为 0600", perm)
	}
}

func TestUumailSessionMergeKeepsOtherProcessEntries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newUumailTestServer(t)
	sessionPath := filepath.Join(t.TempDir(), "sessions.json")
	// 模拟另一进程（如 make reguu）已写入的其他账号会话。
	if err := StoreUumailSession(sessionPath, "other@chato.eu.org", UumailSession{Cookie: "uumail_ut=other"}); err != nil {
		t.Fatalf("StoreUumailSession() error = %v", err)
	}
	// 本进程持有 a1 的过期会话，NewAddress 触发重登并回写缓存。
	if err := StoreUumailSession(sessionPath, "a1@chato.eu.org", UumailSession{Cookie: uumailCookieName + "=stale"}); err != nil {
		t.Fatalf("StoreUumailSession() error = %v", err)
	}
	provider, err := NewUumailWithConfig(ctx, UumailConfig{
		Accounts:      []string{"a1@chato.eu.org"},
		SunMailAPIKey: "test-sunmail-key",
		SessionPath:   sessionPath,
		APIBase:       s.api.URL,
		SSOBase:       s.sso.URL,
	})
	if err != nil {
		t.Fatalf("NewUumailWithConfig() error = %v", err)
	}
	u := provider.(*uumail)
	u.wait = &fakeWait{code: "123456"}
	if _, err = provider.NewAddress(ctx, "Emma Smith"); err != nil {
		t.Fatalf("NewAddress() error = %v", err)
	}

	data, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("读取会话缓存失败：%v", err)
	}
	if !strings.Contains(string(data), "other@chato.eu.org") {
		t.Fatalf("回写不应覆盖其他进程写入的会话：%s", data)
	}
}

func TestUumailSessionConcurrentWriters(t *testing.T) {
	t.Parallel()
	sessionPath := filepath.Join(t.TempDir(), "sessions.json")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			email := fmt.Sprintf("writer%d@chato.eu.org", n)
			for j := 0; j < 10; j++ {
				if err := StoreUumailSession(sessionPath, email, UumailSession{Cookie: "uumail_ut=x"}); err != nil {
					t.Errorf("StoreUumailSession(%s) error = %v", email, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("读取会话缓存失败：%v", err)
	}
	for i := 0; i < 8; i++ {
		if !strings.Contains(string(data), fmt.Sprintf("writer%d@chato.eu.org", i)) {
			t.Fatalf("并发写入丢失条目 writer%d：%s", i, data)
		}
	}
}

func TestUumailSuccessWithMessageFieldNotError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 成功响应携带提示性 message 与 msg 字段时不应被误判为错误。
		_, _ = w.Write([]byte(`{"code": 0, "message": "ok", "msg": "done"}`))
	}))
	t.Cleanup(server.Close)

	client := NewUumailClient(server.URL, server.URL, server.Client())
	if _, err := client.do(context.Background(), http.MethodGet, server.URL+"/x", nil, ""); err != nil {
		t.Fatalf("成功响应带 message 字段不应报错：%v", err)
	}
}

func TestUumailErrorBodyTruncated(t *testing.T) {
	t.Parallel()
	bigHTML := strings.Repeat("<html>gateway error</html>", 200)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(bigHTML))
	}))
	t.Cleanup(server.Close)

	client := NewUumailClient(server.URL, server.URL, server.Client())
	_, err := client.do(context.Background(), http.MethodGet, server.URL+"/x", nil, "")
	if err == nil {
		t.Fatal("非 2xx 应报错")
	}
	if len(err.Error()) > 300 {
		t.Fatalf("错误文本未截断（长度 %d）：%s...", len(err.Error()), err.Error()[:100])
	}
}
