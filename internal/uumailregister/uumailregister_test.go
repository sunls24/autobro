package uumailregister

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"autobro/internal/envfile"
	"autobro/internal/mail"
	"autobro/internal/slregister"
)

func TestCurrentAccountAssignment(t *testing.T) {
	t.Parallel()

	t.Run("追加不存在的 key", func(t *testing.T) {
		path := writeEnvFile(t, "SUNMAIL_API_KEY=sk\n")
		if err := envfile.Update(path, envUumailAccountsKey, "b@chato.eu.org"); err != nil {
			t.Fatalf("envfile.Update() error = %v", err)
		}
		assertEnvContent(t, path, "SUNMAIL_API_KEY=sk\nUUMAIL_ACCOUNTS=b@chato.eu.org\n")
	})

	t.Run("已有多个账号时只保留新值", func(t *testing.T) {
		path := writeEnvFile(t, "UUMAIL_ACCOUNTS=a@chato.eu.org,b@chato.eu.org\n")
		if err := envfile.Update(path, envUumailAccountsKey, "c@chato.eu.org"); err != nil {
			t.Fatalf("envfile.Update() error = %v", err)
		}
		assertEnvContent(t, path, "UUMAIL_ACCOUNTS=c@chato.eu.org\n")
	})

	t.Run("保留 export 前缀并展开引号值", func(t *testing.T) {
		const prefix = "  export  \tUUMAIL_ACCOUNTS="
		path := writeEnvFile(t, prefix+"\"a@chato.eu.org\" # 当前账号\r\n")
		if got := assertSingletonAccount(t, path); got != "a@chato.eu.org" {
			t.Fatalf("读取已有配置 = %q", got)
		}
		if err := envfile.Update(path, envUumailAccountsKey, "b@chato.eu.org"); err != nil {
			t.Fatalf("envfile.Update() error = %v", err)
		}
		assertEnvContent(t, path, prefix+"b@chato.eu.org\r\n")
	})
}

func TestPrepareAccountKeepsCurrentConfigUntilActivation(t *testing.T) {
	t.Parallel()
	var accountFields atomic.Value
	accountFields.Store(`"level":"free","aliasCount":0,"limits":{"aliasLimits":3}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /auth/email/send-code", "POST /v1/user/update":
			_, _ = io.WriteString(w, `{}`)
		case "GET /fetch":
			_, _ = io.WriteString(w, `{"data":[{"subject":"Verification code 123456"}]}`)
		case "POST /auth/email/verify-code":
			_, _ = io.WriteString(w, `{"result":{"OTT":"fixture-ott"}}`)
		case "POST /v1/user/login":
			http.SetCookie(w, &http.Cookie{Name: "uumail_ut", Value: "fixture"})
			_, _ = io.WriteString(w, `{"result":{"uid":42}}`)
		case "GET /v1/user/info":
			if r.Header.Get("Cookie") != "uumail_ut=fixture" {
				t.Error("account check did not use the prepared session")
				http.Error(w, "missing session", http.StatusUnauthorized)
				return
			}
			_, _ = fmt.Fprintf(w, `{"result":{"name":"candidate","domain":"uu.me",%s}}`, accountFields.Load())
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	const configPrefix = "SUNMAIL_API_KEY=fixture\nexport UUMAIL_ACCOUNTS="
	const previous = configPrefix + "current@example.com\n"
	path := writeEnvFile(t, previous)
	options := Options{
		EnvPath: path, SessionPath: filepath.Join(t.TempDir(), "sessions.json"),
		SunMailAPIKey: "fixture", SunMailBaseURL: server.URL,
		APIBase: server.URL, SSOBase: server.URL, PrepareOnly: true,
	}
	result, err := Register(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	assertEnvContent(t, path, previous)
	if err := UseAccount(t.Context(), options, result.Email); !errors.Is(err, mail.ErrUumailUnavailable) {
		t.Fatalf("UseAccount(free) = %v, want unavailable", err)
	}
	assertEnvContent(t, path, previous)

	for _, tt := range []struct {
		name   string
		fields string
	}{
		{"缺少配额", `"level":"plus","aliasCount":0`},
		{"缺少用量", `"level":"plus","limits":{"aliasLimits":1}`},
		{"无效用量", `"level":"plus","aliasCount":0.5,"limits":{"aliasLimits":1}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			accountFields.Store(tt.fields)
			if err := UseAccount(t.Context(), options, result.Email); err == nil || errors.Is(err, mail.ErrUumailUnavailable) {
				t.Fatalf("UseAccount() = %v, want invalid quota error", err)
			}
			assertEnvContent(t, path, previous)
		})
	}

	accountFields.Store(`"level":"plus","aliasCount":0,"limits":{"aliasLimits":-1}`)
	if err := UseAccount(t.Context(), options, result.Email); err != nil {
		t.Fatal(err)
	}
	assertEnvContent(t, path, configPrefix+result.Email+"\n")
}

func TestEnsureAccountRegistersMissingAccount(t *testing.T) {
	t.Parallel()
	fixture := newEnsureFixture(t, "plus")
	path := writeEnvFile(t, "SUNMAIL_API_KEY=fixture\n")
	options := fixture.options(path)
	fixture.beforeUpdate = func() {
		if err := slregister.SetAPIKey(path, "updated-sl-key"); err != nil {
			t.Error(err)
		}
	}

	if err := EnsureAccount(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	email := assertSingletonAccount(t, path)
	if !strings.HasSuffix(email, "@chato.eu.org") {
		t.Fatalf("注册账号 = %q，want chato.eu.org 身份邮箱", email)
	}
	if fixture.logins.Load() != 1 {
		t.Fatalf("登录次数 = %d，want 1", fixture.logins.Load())
	}
	if key, err := envfile.Value(path, "SL_API_KEY"); err != nil || key != "updated-sl-key" {
		t.Fatalf("提交 UU 配置应保留同期写入的 SL 配置，err = %v", err)
	}
}

func TestUnwritableLocalStateStopsRegistration(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("本地预检失败时发出请求 %s", r.URL.Path)
		http.Error(w, "unexpected request", http.StatusBadRequest)
	}))
	defer server.Close()
	for _, failure := range []string{"损坏的会话缓存", "缺失的会话目录", "损坏的配置锁", "账号引用变量"} {
		t.Run(failure, func(t *testing.T) {
			config := "UUMAIL_ACCOUNTS=\n"
			if failure == "账号引用变量" {
				config = "PRIMARY_UU=old@example.com\nUUMAIL_ACCOUNTS=\"$PRIMARY_UU\"\n"
			}
			path := writeEnvFile(t, config)
			options := Options{EnvPath: path, SessionPath: filepath.Join(filepath.Dir(path), "sessions.json"),
				SunMailAPIKey: "fixture", APIBase: server.URL, SSOBase: server.URL, SunMailBaseURL: server.URL}
			switch failure {
			case "损坏的会话缓存":
				if err := os.WriteFile(options.SessionPath, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "缺失的会话目录":
				options.SessionPath = filepath.Join(filepath.Dir(path), "missing", "sessions.json")
			case "损坏的配置锁":
				lock := path + ".lock"
				if err := os.Symlink(lock, lock); err != nil {
					t.Fatal(err)
				}
			}
			if err := EnsureAccount(t.Context(), options); err == nil {
				t.Fatal("EnsureAccount() 应在预检时报错")
			}
			if failure == "损坏的会话缓存" {
				assertEnvContent(t, options.SessionPath, "{")
			}
			assertEnvContent(t, path, config)
		})
	}
}

func TestAccountSwitchResumesCleanup(t *testing.T) {
	t.Parallel()
	const oldConfig = "UUMAIL_ACCOUNTS=old@example.com\n"
	path := writeEnvFile(t, oldConfig)
	options := Options{EnvPath: path, SessionPath: filepath.Join(filepath.Dir(path), "sessions.json")}
	if err := mail.StoreUumailSession(options.SessionPath, "old@example.com", mail.UumailSession{Cookie: "old-cookie"}); err != nil {
		t.Fatal(err)
	}
	if err := mail.StoreUumailSession(options.SessionPath, "new@example.com", mail.UumailSession{Cookie: "new-cookie"}); err != nil {
		t.Fatal(err)
	}
	lock := path + ".lock"
	if err := os.Symlink(lock, lock); err != nil {
		t.Fatal(err)
	}
	if err := activateAccount(options, []string{"old@example.com"}, "new@example.com"); err == nil {
		t.Fatal("配置写入应失败")
	}
	assertEnvContent(t, path, oldConfig)
	cache, err := os.ReadFile(options.SessionPath)
	if err != nil || !strings.Contains(string(cache), "old-cookie") {
		t.Fatal("配置写入失败后应保留旧凭据", err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(options.SessionPath, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	record, err := os.ReadFile(path + ".uu-retire")
	if err != nil {
		t.Fatal(err)
	}
	if err := envfile.Update(path, envUumailAccountsKey, "new@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeAccountSwitch(path); err == nil || !strings.Contains(err.Error(), "已启用") {
		t.Fatalf("清理失败应显示已完成切换，got %v", err)
	}
	assertEnvContent(t, path+".uu-retire", string(record))
	assertEnvContent(t, path, "UUMAIL_ACCOUNTS=new@example.com\n")
	if err := os.WriteFile(options.SessionPath, cache, 0600); err != nil {
		t.Fatal(err)
	}
	if email, err := ResumeAccountSwitch(path); err != nil || email != "new@example.com" {
		t.Fatalf("ResumeAccountSwitch() = %q, %v", email, err)
	}
	cache, err = os.ReadFile(options.SessionPath)
	if err != nil || strings.Contains(string(cache), "old-cookie") {
		t.Fatal("重试后应清除旧凭据", err)
	}
	if _, err := os.Stat(path + ".uu-retire"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("重试完成后应移除清理记录", err)
	}
}

func TestAccountSwitchResumesFailedConfigCommit(t *testing.T) {
	t.Parallel()
	fixture := newEnsureFixture(t, "plus")
	const config = "UUMAIL_ACCOUNTS=\n"
	path := writeEnvFile(t, config)
	options := fixture.options(path)
	lock := path + ".lock"
	fixture.beforeUpdate = func() {
		if err := os.Remove(lock); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(lock, lock); err != nil {
			t.Error(err)
		}
	}
	if err := EnsureAccount(t.Context(), options); err == nil || !strings.Contains(err.Error(), "锁定环境文件") {
		t.Fatalf("注册完成后应在提交配置时失败，got %v", err)
	}
	assertEnvContent(t, path, config)
	if _, err := ResumeAccountSwitch(path); err == nil {
		t.Fatal("配置锁仍损坏时恢复应失败")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	recovered, err := ResumeAccountSwitch(path)
	if err != nil {
		t.Fatal(err)
	}
	if configured := assertSingletonAccount(t, path); configured != recovered {
		t.Fatalf("配置账号 = %q，恢复账号 = %q", configured, recovered)
	}
	if err := EnsureAccount(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if logins := fixture.logins.Load(); logins != 1 {
		t.Fatalf("注册后重试只应提交配置，登录次数 = %d", logins)
	}
}

func TestAccountSwitchSavesRecordBeforeSession(t *testing.T) {
	t.Parallel()
	fixture := newEnsureFixture(t, "plus")
	const config = "UUMAIL_ACCOUNTS=\n"
	path := writeEnvFile(t, config)
	options := fixture.options(path)
	fixture.beforeUpdate = func() {
		if err := os.Mkdir(path+".uu-retire", 0700); err != nil {
			t.Error(err)
		}
	}
	if err := EnsureAccount(t.Context(), options); err == nil || !strings.Contains(err.Error(), "保存切换记录失败") {
		t.Fatalf("应报告切换记录保存失败，err = %v", err)
	}
	if _, err := os.Stat(options.SessionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("切换记录写入失败时应停止保存候选会话，err = %v", err)
	}
	assertEnvContent(t, path, config)
}

func TestAccountSwitchDiscardsCandidateWithoutSavedSession(t *testing.T) {
	t.Parallel()
	const config = "UUMAIL_ACCOUNTS=\n"
	path := writeEnvFile(t, config)
	options := Options{EnvPath: path, SessionPath: filepath.Join(filepath.Dir(path), "sessions.json")}
	if _, err := saveSwitch(options, nil, "candidate@example.com", "candidate"); err != nil {
		t.Fatal(err)
	}
	if recovered, err := ResumeAccountSwitch(path); err != nil || recovered != "" {
		t.Fatalf("恢复应在本地放弃未保存会话的候选，err = %v", err)
	}
	assertEnvContent(t, path, config)
	if _, err := os.Stat(path + ".uu-retire"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("未完成的切换记录应被移除，err = %v", err)
	}
}

func TestAccountSwitchPreservesConfigEditedDuringAvailabilityCheck(t *testing.T) {
	t.Parallel()
	const oldAccount = "old@example.com"
	const editedAccount = "edited@example.com"
	fixture := newEnsureFixture(t, "free")
	path := writeEnvFile(t, "UUMAIL_ACCOUNTS="+oldAccount+"\n")
	options := fixture.options(path)
	if err := mail.StoreUumailSession(options.SessionPath, oldAccount, mail.UumailSession{
		Cookie: "uumail_ut=old-session",
	}); err != nil {
		t.Fatal(err)
	}
	fixture.beforeOldInfo = func() {
		if err := envfile.Update(path, envUumailAccountsKey, editedAccount); err != nil {
			t.Error(err)
		}
	}
	if err := EnsureAccount(t.Context(), options); err == nil || !strings.Contains(err.Error(), "当前 UU 配置已更改") {
		t.Fatalf("提交应报告配置冲突，err = %v", err)
	}
	assertEnvContent(t, path, "UUMAIL_ACCOUNTS="+editedAccount+"\n")
	if _, err := os.Stat(path + ".uu-retire"); err != nil {
		t.Fatalf("配置冲突应保留切换记录：%v", err)
	}
	if ok, err := mail.HasUumailSession(options.SessionPath, oldAccount, ""); err != nil || !ok {
		t.Fatalf("配置冲突应保留旧会话，err = %v", err)
	}
}

func TestEnsureAccountReplacesUnavailableAccount(t *testing.T) {
	t.Parallel()
	const previous = "old@chato.eu.org\n"
	fixture := newEnsureFixture(t, "free")
	path := writeEnvFile(t, "UUMAIL_ACCOUNTS="+previous)
	options := fixture.options(path)
	if err := mail.StoreUumailSession(options.SessionPath, "old@chato.eu.org", mail.UumailSession{
		Cookie: "uumail_ut=old-session",
	}); err != nil {
		t.Fatal(err)
	}

	if err := EnsureAccount(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	email := assertSingletonAccount(t, path)
	if email == "old@chato.eu.org" {
		t.Fatal("不可用账号不应保留为当前账号")
	}
	client := mail.NewUumailClient(options.APIBase, options.SSOBase, nil)
	if _, err := client.CachedUserInfo(t.Context(), options.SessionPath, "old@chato.eu.org"); !errors.Is(err, mail.ErrUumailSessionMissing) {
		t.Fatalf("旧账号仍有可用凭据：%v", err)
	}
	if fixture.logins.Load() != 1 {
		t.Fatalf("替换账号应只登录新账号一次，logins = %d", fixture.logins.Load())
	}
}

func TestEnsureAccountRelogsWithoutReplacingAvailableAccount(t *testing.T) {
	t.Parallel()
	const account = "old@chato.eu.org"
	fixture := newEnsureFixture(t, "plus")
	const config = "export UUMAIL_ACCOUNTS='" + account + "' # 当前账号\n"
	path := writeEnvFile(t, config)
	options := fixture.options(path)

	if err := EnsureAccount(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	assertEnvContent(t, path, config)
	if fixture.logins.Load() != 1 {
		t.Fatalf("缓存缺失时应重登一次，logins = %d", fixture.logins.Load())
	}
}

func TestConcurrentSLAndUUConfigWrites(t *testing.T) {
	t.Parallel()
	const initial = "SL_API_KEY=old-key\nUUMAIL_ACCOUNTS=old@example.com\n"
	path := writeEnvFile(t, initial)
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		results <- slregister.SetAPIKey(path, "new-key")
	}()
	go func() {
		<-start
		results <- envfile.Update(path, envUumailAccountsKey, "new@example.com")
	}()
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	if err := envfile.UpdateIfUnchanged(path, envUumailAccountsKey, "stale@example.com", initial); err == nil {
		t.Fatal("旧快照不能覆盖已提交的配置")
	}
	assertEnvContent(t, path, "SL_API_KEY=new-key\nUUMAIL_ACCOUNTS=new@example.com\n")
}

func TestUumailUsername(t *testing.T) {
	t.Parallel()

	pattern := regexp.MustCompile(`^[a-zA-Z0-9_-]{3,20}$`)
	for _, name := range []string{"Emma Smith", "Liam Noah", "X"} {
		username := uumailUsername(name)
		if !pattern.MatchString(username) {
			t.Fatalf("uumailUsername(%q) = %q，不满足 3-20 位 [a-zA-Z0-9_-]", name, username)
		}
	}
}

type ensureFixture struct {
	server        *httptest.Server
	logins        atomic.Int32
	beforeUpdate  func()
	beforeOldInfo func()
}

func newEnsureFixture(t *testing.T, oldLevel string) *ensureFixture {
	t.Helper()
	fixture := &ensureFixture{}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /auth/email/send-code":
			_, _ = io.WriteString(w, `{}`)
		case "POST /v1/user/update":
			if fixture.beforeUpdate != nil {
				fixture.beforeUpdate()
			}
			_, _ = io.WriteString(w, `{}`)
		case "GET /fetch":
			_, _ = io.WriteString(w, `{"data":[{"subject":"Verification code 123456"}]}`)
		case "POST /auth/email/verify-code":
			_, _ = io.WriteString(w, `{"result":{"OTT":"fixture-ott"}}`)
		case "POST /v1/user/login":
			fixture.logins.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "uumail_ut", Value: "new-session"})
			_, _ = io.WriteString(w, `{"result":{"uid":42}}`)
		case "GET /v1/user/info":
			level := "plus"
			if r.Header.Get("Cookie") == "uumail_ut=old-session" {
				level = oldLevel
				if fixture.beforeOldInfo != nil {
					fixture.beforeOldInfo()
				}
			}
			// Return a username different from the generated one.
			_, _ = fmt.Fprintf(w,
				`{"result":{"name":"fixture","domain":"uu.me","aliasCount":0,"level":%q,"limits":{"aliasLimits":-1}}}`,
				level)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (fixture *ensureFixture) options(path string) Options {
	return Options{
		EnvPath:        path,
		SessionPath:    filepath.Join(filepath.Dir(path), "sessions.json"),
		SunMailAPIKey:  "fixture",
		SunMailBaseURL: fixture.server.URL,
		APIBase:        fixture.server.URL,
		SSOBase:        fixture.server.URL,
	}
}

func assertSingletonAccount(t *testing.T, path string) string {
	t.Helper()
	_, accounts, err := readAccountConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 {
		t.Fatalf("UUMAIL_ACCOUNTS = %q，want 单账号", accounts)
	}
	return accounts[0]
}

func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env.local")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入 env 文件失败：%v", err)
	}
	return path
}

func assertEnvContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 env 文件失败：%v", err)
	}
	if string(data) != want {
		t.Fatalf("env 内容 = %q，want %q", string(data), want)
	}
}
