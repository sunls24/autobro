package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sunls24/gox"
)

var _ IMailAddress = (*uumail)(nil)

const (
	defaultUumailAPIBase     = "https://api.uu.me"
	defaultUumailSSOBase     = "https://commonapi.mxfast.com"
	defaultUumailSessionPath = "uumail_sessions.json"
	uumailAppName            = "uumail"
	uumailCookieName         = "uumail_ut"
	uumailUserAgent          = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"
	uumailSendCodeRetryWait  = 65 * time.Second
	uumailLoginTimeout       = 5 * time.Minute
	uumailAliasMaxRetries    = 3
)

// ErrUumailSessionExpired 表示会话 cookie 失效，需要重新走邮箱验证码登录。
var ErrUumailSessionExpired = errors.New("Uumail：会话已过期")

// UumailSession 是一个 Uumail 账号（以 SunMail 地址为身份）的会话信息。
type UumailSession struct {
	Cookie   string `json:"cookie"` // uumail_ut=...，永不出现在日志
	Username string `json:"username"`
	Domain   string `json:"domain"`
	SavedAt  int64  `json:"saved_at"`
}

// UumailUserInfo 是 /v1/user/info 的关键字段。
type UumailUserInfo struct {
	Username    string
	Domain      string
	AliasCount  int64
	AliasLimits int64 // -1 表示不限
	Level       string
}

// UumailConfig 配置 Uumail 地址提供方。APIBase/SSOBase/SessionPath 留空时取默认值。
type UumailConfig struct {
	Accounts      []string // SunMail 身份地址池（UUMAIL_ACCOUNTS）
	SunMailAPIKey string   // 懒登录收码使用
	SessionPath   string
	APIBase       string
	SSOBase       string
	HTTPClient    *http.Client
}

// UumailClient 封装 Uumail 与 Maxthon SSO 的 HTTP 接口；提供方懒登录与注册流程共用。
type UumailClient struct {
	apiBase string
	ssoBase string
	http    *http.Client
}

func NewUumailClient(apiBase, ssoBase string, httpClient *http.Client) *UumailClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &UumailClient{
		apiBase: strings.TrimRight(defaultOr(apiBase, defaultUumailAPIBase), "/"),
		ssoBase: strings.TrimRight(defaultOr(ssoBase, defaultUumailSSOBase), "/"),
		http:    httpClient,
	}
}

func defaultOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (c *UumailClient) newRequest(ctx context.Context, method, url string, body any, cookie string) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", uumailUserAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	return req, nil
}

// do 执行一次 JSON 请求并解析响应。错误契约：HTTP 非 2xx，或 2xx 但响应含
// 非空 err/message 字段；401/403 统一归一为 ErrUumailSessionExpired。
func (c *UumailClient) do(ctx context.Context, method, url string, body any, cookie string) (map[string]any, error) {
	req, err := c.newRequest(ctx, method, url, body, cookie)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	// 认证失败必须先于响应体解析判断：401/403 的响应体可能是非 JSON 文本。
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrUumailSessionExpired
	}
	parsed := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("Uumail：解析响应失败（HTTP %d）：%w", resp.StatusCode, err)
		}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return parsed, fmt.Errorf("Uumail：HTTP %d：%s", resp.StatusCode, uumailErrOrBody(parsed, raw))
	}
	// 成功响应只认 err 字段；message 仅在非 2xx 时作为错误文本，避免成功
	// 响应携带提示性 message 字段时被误判。
	if errText := uumailErrField(parsed, "err"); errText != "" {
		return parsed, errors.New("Uumail：" + errText)
	}
	return parsed, nil
}

// uumailErrField 读取响应中的指定错误文本字段；无则返回空串。
func uumailErrField(parsed map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := parsed[key]; ok {
			if text := strings.TrimSpace(fmt.Sprint(value)); text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func uumailErrOrBody(parsed map[string]any, raw []byte) string {
	if text := uumailErrField(parsed, "err", "message"); text != "" {
		return text
	}
	// 非 JSON 的错误响应（如网关 HTML 页）截断，避免错误文本撑爆日志。
	body := strings.TrimSpace(string(raw))
	if len(body) > 200 {
		body = body[:200] + "…"
	}
	return body
}

// SendCode 请求 SSO 向邮箱发送 6 位验证码；服务端频控（约 60 秒）时等待后重试一次。
func (c *UumailClient) SendCode(ctx context.Context, email string) error {
	send := func() error {
		_, err := c.do(ctx, http.MethodPost, c.ssoBase+"/auth/email/send-code",
			map[string]string{"email": email, "app": uumailAppName}, "")
		return err
	}
	err := send()
	if err == nil || ctx.Err() != nil || !strings.Contains(err.Error(), "too-frequent") {
		return err
	}
	logMailWarning("Uumail", "验证码发送过于频繁，等待重试",
		slogEmail(email), slog.Duration("wait", uumailSendCodeRetryWait))
	select {
	case <-time.After(uumailSendCodeRetryWait):
	case <-ctx.Done():
		return ctx.Err()
	}
	return send()
}

// VerifyCode 用验证码换取 OTT 一次性令牌。
func (c *UumailClient) VerifyCode(ctx context.Context, email, code string) (string, error) {
	parsed, err := c.do(ctx, http.MethodPost, c.ssoBase+"/auth/email/verify-code",
		map[string]string{"email": email, "code": code, "app": uumailAppName}, "")
	if err != nil {
		return "", err
	}
	result, _ := parsed["result"].(map[string]any)
	ott, _ := result["OTT"].(string)
	if strings.TrimSpace(ott) == "" {
		return "", errors.New("Uumail：验证码校验未返回 OTT")
	}
	return ott, nil
}

// Login 用 OTT 换取会话 cookie（uumail_ut=...，有效期 30 天）。
func (c *UumailClient) Login(ctx context.Context, ott string) (string, error) {
	req, err := c.newRequest(ctx, http.MethodPost, c.apiBase+"/v1/user/login",
		map[string]string{"OTT": ott}, "")
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", ErrUumailSessionExpired
	}
	parsed := map[string]any{}
	_ = json.Unmarshal(raw, &parsed)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("Uumail：登录失败 HTTP %d：%s", resp.StatusCode, uumailErrOrBody(parsed, raw))
	}
	if errText := uumailErrField(parsed, "err"); errText != "" {
		return "", errors.New("Uumail：" + errText)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == uumailCookieName && strings.TrimSpace(ck.Value) != "" {
			return uumailCookieName + "=" + ck.Value, nil
		}
	}
	return "", errors.New("Uumail：登录响应未包含会话 cookie")
}

// LoginByEmail 走完整邮箱验证码登录链路；wait 负责从身份邮箱收取验证码。
func (c *UumailClient) LoginByEmail(ctx context.Context, email string, wait IMailWait) (string, error) {
	if wait == nil {
		return "", errors.New("Uumail：缺少验证码接收通道")
	}
	loginCtx, cancel := context.WithTimeout(ctx, uumailLoginTimeout)
	defer cancel()
	// 先启动验证码监听再发码：SunMail 以监听启动时间为 since，顺序颠倒会漏掉送达过快的邮件。
	codeCh := wait.WaitMailCode(loginCtx, email)
	if err := c.SendCode(loginCtx, email); err != nil {
		return "", fmt.Errorf("Uumail：发送登录验证码：%w", err)
	}
	code, err := waitUumailCode(loginCtx, codeCh)
	if err != nil {
		return "", err
	}
	ott, err := c.VerifyCode(loginCtx, email, code)
	if err != nil {
		return "", fmt.Errorf("Uumail：校验登录验证码：%w", err)
	}
	cookie, err := c.Login(loginCtx, ott)
	if err != nil {
		return "", err
	}
	logMailDebug("Uumail", "邮箱登录成功", slogEmail(email))
	return cookie, nil
}

// waitUumailCode 从验证码通道读取 6 位数字；通道错误立即返回。
func waitUumailCode(ctx context.Context, codeCh <-chan gox.Result[string]) (string, error) {
	select {
	case result, ok := <-codeCh:
		if !ok {
			return "", errors.New("Uumail：验证码通道已关闭")
		}
		if result.Err != nil {
			return "", fmt.Errorf("Uumail：收取登录验证码：%w", result.Err)
		}
		code := strings.TrimSpace(result.Value)
		if code == "" {
			return "", errors.New("Uumail：登录验证码为空")
		}
		return code, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// UserInfo 查询账号资料与配额；会话失效时返回 ErrUumailSessionExpired。
func (c *UumailClient) UserInfo(ctx context.Context, cookie string) (UumailUserInfo, error) {
	parsed, err := c.do(ctx, http.MethodGet, c.apiBase+"/v1/user/info", nil, cookie)
	if err != nil {
		return UumailUserInfo{}, err
	}
	result, _ := parsed["result"].(map[string]any)
	info := UumailUserInfo{
		Username:    stringField(result, "name"),
		Domain:      stringField(result, "domain"),
		AliasCount:  intField(result, "aliasCount"),
		AliasLimits: -1,
		Level:       stringField(result, "level"),
	}
	if limits, ok := result["limits"].(map[string]any); ok {
		info.AliasLimits = intField(limits, "aliasLimits")
	}
	if info.Username == "" || info.Domain == "" {
		return UumailUserInfo{}, errors.New("Uumail：用户信息缺少用户名或域名")
	}
	return info, nil
}

// UpdateProfile 完善首次登录后的资料（用户名 + 转发收件箱）并返回最新资料。
func (c *UumailClient) UpdateProfile(ctx context.Context, cookie, username, realInbox string) (UumailUserInfo, error) {
	if _, err := c.do(ctx, http.MethodPost, c.apiBase+"/v1/user/update",
		map[string]string{"name": username, "real": realInbox}, cookie); err != nil {
		return UumailUserInfo{}, err
	}
	return c.UserInfo(ctx, cookie)
}

// CreateAlias 创建别名（prefix 需满足 ^[a-zA-Z0-9-_]+$ 且 ≤64 字符）。
func (c *UumailClient) CreateAlias(ctx context.Context, cookie, prefix string) error {
	_, err := c.do(ctx, http.MethodPost, c.apiBase+"/v1/addr/update",
		map[string]string{"alias": prefix}, cookie)
	return err
}

// DeleteAliases 删除别名；入参为完整别名地址，服务端要求的是 @ 前的本地
// 部分，这里统一归一化（真实服务端对完整地址返回 invalidChars 错误）。
func (c *UumailClient) DeleteAliases(ctx context.Context, cookie string, emails []string) error {
	if len(emails) == 0 {
		return nil
	}
	prefixes := make([]string, 0, len(emails))
	for _, email := range emails {
		email = strings.TrimSpace(email)
		if at := strings.LastIndex(email, "@"); at > 0 {
			email = email[:at]
		}
		if email != "" {
			prefixes = append(prefixes, email)
		}
	}
	if len(prefixes) == 0 {
		return nil
	}
	_, err := c.do(ctx, http.MethodPost, c.apiBase+"/v1/addr/delete",
		map[string]string{"aliases": strings.Join(prefixes, ",")}, cookie)
	return err
}

func stringField(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return strings.TrimSpace(value)
}

func intField(m map[string]any, key string) int64 {
	value, _ := m[key].(float64)
	return int64(value)
}

// uumailNameToAddress 生成 Uumail 别名 prefix：只允许 [a-zA-Z0-9-_]，不允许点号。
func uumailNameToAddress(name string) string {
	if strings.TrimSpace(name) == "" {
		return strings.ToLower(gox.RandStr(8))
	}
	address := strings.ReplaceAll(strings.TrimSpace(name), " ", "-")
	if r := rand.IntN(200) + 1; r < 100 {
		address += strconv.Itoa(r)
	}
	address += "-" + gox.RandStr(2)
	return strings.ToLower(address)
}

type uumail struct {
	client      *UumailClient
	accounts    []string
	index       int
	sessionPath string
	sessions    map[string]UumailSession
	wait        IMailWait
	mu          sync.Mutex
}

// NewUumailWithConfig 创建 Uumail 地址提供方；会话缓存懒加载自 SessionPath。
func NewUumailWithConfig(ctx context.Context, config UumailConfig) (IMailAddress, error) {
	accounts := make([]string, 0, len(config.Accounts))
	for _, account := range config.Accounts {
		account = strings.ToLower(strings.TrimSpace(account))
		if account != "" {
			accounts = append(accounts, account)
		}
	}
	if len(accounts) == 0 {
		return nil, errors.New("Uumail：UUMAIL_ACCOUNTS 为空，请先运行 make reguu 注册账号")
	}
	if strings.TrimSpace(config.SunMailAPIKey) == "" {
		return nil, errors.New("Uumail：SUNMAIL_API_KEY 为空，无法懒登录收取验证码")
	}
	provider := &uumail{
		client:      NewUumailClient(config.APIBase, config.SSOBase, config.HTTPClient),
		accounts:    accounts,
		sessionPath: defaultOr(config.SessionPath, defaultUumailSessionPath),
		sessions:    map[string]UumailSession{},
		wait: NewSunMailWithConfig(SunMailConfig{
			APIKey:     config.SunMailAPIKey,
			HTTPClient: config.HTTPClient,
		}),
	}
	provider.loadSessions()
	return provider, nil
}

// loadSessions 读取会话缓存文件；文件缺失或损坏时按空处理。
func (u *uumail) loadSessions() {
	data, err := os.ReadFile(u.sessionPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logMailWarning("Uumail", "会话缓存读取失败，按空处理", slog.String("path", u.sessionPath), slog.Any("err", err))
		}
		return
	}
	sessions := map[string]UumailSession{}
	if err := json.Unmarshal(data, &sessions); err != nil {
		logMailWarning("Uumail", "会话缓存解析失败，按空处理",
			slog.String("path", u.sessionPath), slog.Any("err", err))
		return
	}
	u.sessions = sessions
	logMailDebug("Uumail", "会话缓存已加载", slog.Int("count", len(sessions)))
}

func (u *uumail) saveSessions() error {
	return writeUumailSessions(u.sessionPath, u.sessions)
}

// writeUumailSessions 以"独占锁 + 临时文件 rename"原子更新会话缓存：
// 先合并磁盘上其他进程写入的条目（含注册流程新增账号），再整体替换，
// 避免丢失更新或让读者看到半截内容；Chmod 保证权限收紧为 0600。
func writeUumailSessions(path string, overlay map[string]UumailSession) error {
	release, err := lockUumailSessions(path)
	if err != nil {
		return err
	}
	defer release()

	sessions := map[string]UumailSession{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &sessions)
	}
	for email, session := range overlay {
		sessions[email] = session
	}
	encoded, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".uumail-sessions-*.tmp")
	if err != nil {
		return fmt.Errorf("Uumail：创建会话缓存临时文件：%w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(encoded); err != nil {
		_ = temp.Close()
		return fmt.Errorf("Uumail：写入会话缓存：%w", err)
	}
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("Uumail：设置会话缓存权限：%w", err)
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("Uumail：替换会话缓存：%w", err)
	}
	return nil
}

// lockUumailSessions 用独占锁文件串行化跨进程的会话缓存更新；
// 锁文件超过 1 分钟视为进程残留，直接接管。返回释放函数。
func lockUumailSessions(path string) (func(), error) {
	lockPath := path + ".lock"
	deadline := time.Now().Add(5 * time.Second)
	for {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = file.WriteString(strconv.Itoa(os.Getpid()))
			_ = file.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("Uumail：创建会话缓存锁失败：%w", err)
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > time.Minute {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errors.New("Uumail：等待会话缓存锁超时")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// StoreUumailSession 把一个账号的会话写入缓存文件（注册流程与提供方共用）。
func StoreUumailSession(path, email string, session UumailSession) error {
	session.SavedAt = time.Now().Unix()
	return writeUumailSessions(defaultOr(path, defaultUumailSessionPath),
		map[string]UumailSession{strings.ToLower(strings.TrimSpace(email)): session})
}

// ensureSession 返回可用会话：优先缓存，cookie 缺失时走邮箱验证码登录。
func (u *uumail) ensureSession(ctx context.Context, email string) (UumailSession, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	session, ok := u.sessions[email]
	if ok && strings.TrimSpace(session.Cookie) != "" {
		return session, nil
	}
	logMailStep("Uumail", "登录账号", slogEmail(email))
	cookie, err := u.client.LoginByEmail(ctx, email, u.wait)
	if err != nil {
		return UumailSession{}, err
	}
	info, err := u.client.UserInfo(ctx, cookie)
	if err != nil {
		return UumailSession{}, err
	}
	session = UumailSession{Cookie: cookie, Username: info.Username, Domain: info.Domain}
	u.sessions[email] = session
	if err := u.saveSessions(); err != nil {
		logMailWarning("Uumail", "会话缓存写入失败", slogEmail(email), slog.Any("err", err))
	}
	return session, nil
}

// refreshSession 校验并刷新会话，返回最新用户信息；失效时自动重登一次。
func (u *uumail) refreshSession(ctx context.Context, email string) (UumailSession, UumailUserInfo, error) {
	session, err := u.ensureSession(ctx, email)
	if err != nil {
		return UumailSession{}, UumailUserInfo{}, err
	}
	info, err := u.client.UserInfo(ctx, session.Cookie)
	if errors.Is(err, ErrUumailSessionExpired) {
		u.forgetSession(email)
		if session, err = u.ensureSession(ctx, email); err != nil {
			return UumailSession{}, UumailUserInfo{}, err
		}
		if info, err = u.client.UserInfo(ctx, session.Cookie); err != nil {
			return UumailSession{}, UumailUserInfo{}, err
		}
	} else if err != nil {
		return UumailSession{}, UumailUserInfo{}, err
	}
	session.Username, session.Domain = info.Username, info.Domain
	u.mu.Lock()
	u.sessions[email] = session
	u.mu.Unlock()
	return session, info, nil
}

func (u *uumail) forgetSession(email string) {
	u.mu.Lock()
	delete(u.sessions, email)
	u.mu.Unlock()
}

// withSession 用账号会话执行 fn；会话失效时重登一次后重试。
func (u *uumail) withSession(ctx context.Context, email string, fn func(session UumailSession) error) error {
	session, err := u.ensureSession(ctx, email)
	if err != nil {
		return err
	}
	err = fn(session)
	if !errors.Is(err, ErrUumailSessionExpired) {
		return err
	}
	u.forgetSession(email)
	if session, err = u.ensureSession(ctx, email); err != nil {
		return err
	}
	return fn(session)
}

// resolveOwner 通过别名地址的 {username}.{domain} 后缀定位归属账号。
func (u *uumail) resolveOwner(ctx context.Context, email string) (string, UumailSession, error) {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return "", UumailSession{}, fmt.Errorf("Uumail：%s 不是合法的别名地址", email)
	}
	suffix := strings.ToLower(email[at+1:])
	// 先查已加载的会话，避免为定位一个别名触发全部账号登录。
	u.mu.Lock()
	for account, session := range u.sessions {
		if session.Username != "" && strings.EqualFold(session.Username+"."+session.Domain, suffix) {
			u.mu.Unlock()
			return account, session, nil
		}
	}
	u.mu.Unlock()
	for _, account := range u.accounts {
		session, _, err := u.refreshSession(ctx, account)
		if err != nil {
			// 单个账号刷新失败（如临时网络错误）不应中止归属查找，跳过继续。
			logMailWarning("Uumail", "刷新账号会话失败，跳过", slogEmail(account), slog.Any("err", err))
			continue
		}
		if strings.EqualFold(session.Username+"."+session.Domain, suffix) {
			return account, session, nil
		}
	}
	return "", UumailSession{}, fmt.Errorf("Uumail：未找到 %s 的归属账号", email)
}

func (u *uumail) NewAddress(ctx context.Context, name string) (string, error) {
	for attempt := 0; attempt < len(u.accounts); attempt++ {
		email := u.current()
		_, info, err := u.refreshSession(ctx, email)
		if err != nil {
			return "", err
		}
		if info.AliasLimits >= 0 && info.AliasCount >= info.AliasLimits {
			logMailWarning("Uumail", "账号别名配额已用尽，轮换下一账号",
				slogEmail(email), slog.Int64("limit", info.AliasLimits))
			u.rotate()
			continue
		}
		address, err := u.createAliasWithRetry(ctx, email, name)
		if err != nil {
			return "", err
		}
		u.rotate()
		return address, nil
	}
	return "", errors.New("Uumail：所有账号别名配额均已用尽，请运行 make reguu 注册新账号")
}

// current 返回当前选中的账号地址；读取与 rotate 同样加锁，保证并发安全。
func (u *uumail) current() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.accounts[u.index]
}

// createAliasWithRetry 创建别名：前缀冲突时换新前缀重试；会话过期时刷新会话重试
// 一次（不占用冲突重试次数）。
func (u *uumail) createAliasWithRetry(ctx context.Context, email, name string) (string, error) {
	var lastErr error
	expiredRetried := false
	for attempt := 0; attempt < uumailAliasMaxRetries; attempt++ {
		session, err := u.ensureSession(ctx, email)
		if err != nil {
			return "", err
		}
		prefix := uumailNameToAddress(name)
		err = u.client.CreateAlias(ctx, session.Cookie, prefix)
		if err == nil {
			address := prefix + "@" + session.Username + "." + session.Domain
			logMailDebug("Uumail", "别名已创建", slog.String("address", address))
			return address, nil
		}
		if errors.Is(err, ErrUumailSessionExpired) && !expiredRetried {
			expiredRetried = true
			u.forgetSession(email)
			lastErr = err
			attempt--
			continue
		}
		lastErr = err
		logMailWarning("Uumail", "创建别名失败，重试", slog.String("prefix", prefix), slog.Any("err", err))
	}
	return "", lastErr
}

func (u *uumail) rotate() {
	u.mu.Lock()
	u.index = (u.index + 1) % len(u.accounts)
	u.mu.Unlock()
}

func (u *uumail) DelAddressByMetadata(ctx context.Context, metadata AddressMetadata) error {
	email := normalizeAddress(metadata.Email)
	if metadata.Provider != "" && !strings.EqualFold(metadata.Provider, AddressProviderUumail) {
		return fmt.Errorf("Uumail：不支持的元数据提供方 %q", metadata.Provider)
	}
	ownerEmail, _, err := u.resolveOwner(ctx, email)
	if err != nil {
		return err
	}
	return u.withSession(ctx, ownerEmail, func(session UumailSession) error {
		if err := u.client.DeleteAliases(ctx, session.Cookie, []string{email}); err != nil {
			return err
		}
		logMailDebug("Uumail", "别名已删除", slog.String("address", email))
		return nil
	})
}

func (u *uumail) ForgetAddress(address string) {}

func (u *uumail) ForwardAddress(ctx context.Context, address string) (string, error) {
	ownerEmail, _, err := u.resolveOwner(ctx, normalizeAddress(address))
	if err != nil {
		return "", err
	}
	return ownerEmail, nil
}

func (u *uumail) Metadata(address string) AddressMetadata {
	return AddressMetadata{Email: address, Provider: AddressProviderUumail}
}

func slogEmail(email string) slog.Attr {
	return slog.String("email", email)
}
