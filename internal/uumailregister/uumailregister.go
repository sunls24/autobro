package uumailregister

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"autobro/internal/envfile"
	"autobro/internal/logging"
	"autobro/internal/mail"

	"github.com/sunls24/gox"
)

const (
	// registrationSunMailDomain 与 SimpleLogin 注册保持一致：用该 SunMail 域名
	// 的地址作为 Uumail 账号身份与转发收件箱。
	registrationSunMailDomain = "chato.eu.org"
	envUumailAccountsKey      = "UUMAIL_ACCOUNTS"
)

var uumailAccountEmail = regexp.MustCompile(`^[a-z0-9._+-]+@[a-z0-9-]+(?:\.[a-z0-9-]+)+$`)

// Options 配置 Uumail 账号注册和本地替换。
type Options struct {
	EnvPath        string
	SessionPath    string
	SunMailAPIKey  string
	SunMailBaseURL string
	APIBase        string
	SSOBase        string
	HTTPClient     *http.Client
	PrepareOnly    bool // 只保存候选会话，由 UseAccount 确认后再切换配置。
}

// Result 包含注册完成的账号信息；cookie 写入会话缓存，不进入日志。
type Result struct {
	Email       string
	Username    string
	AliasDomain string
	Level       string
}

// UseAccount 验证已登录账号的会员和配额，再替换本地配置。
func UseAccount(ctx context.Context, options Options, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if !uumailAccountEmail.MatchString(email) {
		return errors.New("Uumail：请提供有效的账号邮箱")
	}
	_, accounts, err := readAccountConfig(options.EnvPath)
	if err != nil {
		return err
	}
	client := mail.NewUumailClient(options.APIBase, options.SSOBase, options.HTTPClient)
	info, err := client.CachedUserInfo(ctx, options.SessionPath, email)
	if err != nil {
		return fmt.Errorf("检查候选 UU 账号：%w", err)
	}
	if err := info.CheckAliasAvailability(); err != nil {
		return err
	}
	if err := activateAccount(options, accounts, email); err != nil {
		return err
	}
	logging.Done("Uumail", "切换当前账号", slog.String("email", email), slog.String("level", info.Level))
	return nil
}

// EnsureAccount 确保本地只有一个当前可用的 Uumail 账号。账号缺失、会员权益失效
// 或别名配额耗尽时注册新账号，验证可用后原子替换旧账号；临时查询错误不会触发换号。
func EnsureAccount(ctx context.Context, options Options) error {
	_, accounts, err := readAccountConfig(options.EnvPath)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		logging.Step("Uumail", "未配置账号，注册首个账号")
		return registerReplacement(ctx, options, accounts)
	}

	email := strings.ToLower(accounts[0])

	logging.Step("Uumail", "检查当前账号", slog.String("email", email))
	info, err := inspectAccount(ctx, options, email)
	if err != nil {
		return fmt.Errorf("检查当前 Uumail 账号 %s：%w", email, err)
	}
	if err := info.CheckAliasAvailability(); err != nil {
		if errors.Is(err, mail.ErrUumailUnavailable) {
			logging.Warning("Uumail", "当前账号不可用，注册替换账号",
				slog.String("email", email), slog.Any("err", err))
			return registerReplacement(ctx, options, accounts)
		}
		return fmt.Errorf("检查当前 Uumail 账号 %s：%w", email, err)
	}

	if len(accounts) > 1 {
		if err := activateAccount(options, accounts, email); err != nil {
			return err
		}
	}
	logging.Done("Uumail", "当前账号可用", slog.String("email", email), slog.String("level", info.Level))
	return nil
}

func registerReplacement(ctx context.Context, options Options, previous []string) error {
	options.PrepareOnly = false
	_, err := register(ctx, options, previous)
	return err
}

// inspectAccount 优先使用本地会话；缓存缺失或失效时才走邮箱验证码重登，
// 避免把“会话过期”误判为“会员权益过期”。
func inspectAccount(ctx context.Context, options Options, email string) (mail.UumailUserInfo, error) {
	client := mail.NewUumailClient(options.APIBase, options.SSOBase, options.HTTPClient)
	info, err := client.CachedUserInfo(ctx, options.SessionPath, email)
	if err == nil {
		return info, nil
	}
	if !errors.Is(err, mail.ErrUumailSessionMissing) && !errors.Is(err, mail.ErrUumailSessionExpired) {
		return mail.UumailUserInfo{}, err
	}
	sunMail, err := registrationMailbox(options)
	if err != nil {
		return mail.UumailUserInfo{}, err
	}
	logging.Step("Uumail", "重新登录当前账号", slog.String("email", email))
	cookie, err := client.LoginByEmail(ctx, email, sunMail)
	if err != nil {
		return mail.UumailUserInfo{}, fmt.Errorf("重新登录失败：%w", err)
	}
	info, err = client.UserInfo(ctx, cookie)
	if err != nil {
		return mail.UumailUserInfo{}, err
	}
	if err := mail.StoreUumailSession(options.SessionPath, email, mail.UumailSession{
		Cookie:   cookie,
		Username: info.Username,
		Domain:   info.Domain,
	}); err != nil {
		logging.Warning("Uumail", "会话缓存写入失败", slog.String("email", email), slog.Any("err", err))
	}
	return info, nil
}

// Register 注册一个 Uumail 账号：用 SunMail 地址完成邮箱验证码登录，完善资料，
// 把会话写入缓存文件。准备候选模式只保存会话；普通模式验证权益后把
// UUMAIL_ACCOUNTS 原子替换为这一个账号。
func Register(ctx context.Context, options Options) (Result, error) {
	if options.PrepareOnly {
		return register(ctx, options, nil)
	}
	_, accounts, err := readAccountConfig(options.EnvPath)
	if err != nil {
		return Result{}, err
	}
	return register(ctx, options, accounts)
}

func register(ctx context.Context, options Options, previous []string) (Result, error) {
	sunMail, err := registrationMailbox(options)
	if err != nil {
		return Result{}, err
	}
	if !options.PrepareOnly {
		if err := envfile.CheckWritable(options.EnvPath); err != nil {
			return Result{}, fmt.Errorf("检查 %s 写入能力失败：%w", options.EnvPath, err)
		}
	}
	if err := mail.CheckUumailSessions(options.SessionPath); err != nil {
		return Result{}, fmt.Errorf("注册前检查 UU 会话缓存：%w", err)
	}

	name := mail.GenerateName()
	email, err := sunMail.NewAddress(ctx, name)
	if err != nil {
		return Result{}, fmt.Errorf("生成 SunMail 地址失败：%w", err)
	}
	username := uumailUsername(name)

	logging.Step("Uumail", "注册账号", loggingFields(name, email, username)...)
	client := mail.NewUumailClient(options.APIBase, options.SSOBase, options.HTTPClient)
	cookie, err := client.LoginByEmail(ctx, email, sunMail)
	if err != nil {
		return Result{}, fmt.Errorf("Uumail 账号 %s 邮箱验证码登录失败：%w", email, err)
	}
	logging.Done("Uumail", "账号登录")

	info, err := client.UpdateProfile(ctx, cookie, username, email)
	if err != nil {
		return Result{}, fmt.Errorf("Uumail 账号 %s 完善资料失败：%w", email, err)
	}
	logging.Done("Uumail", "资料已完善", slog.String("username", info.Username), slog.String("level", info.Level))

	var record pendingAccountSwitch
	if !options.PrepareOnly {
		if err := info.CheckAliasAvailability(); err != nil {
			return Result{}, fmt.Errorf("新 Uumail 账号 %s 不可用：%w", email, err)
		}
		record, err = saveSwitch(options, previous, email, info.Username)
		if err != nil {
			return Result{}, fmt.Errorf("Uumail 账号 %s 已创建，保存切换记录失败：%w", email, err)
		}
	}
	if err := mail.StoreUumailSession(options.SessionPath, email, mail.UumailSession{
		Cookie:   cookie,
		Username: info.Username,
		Domain:   info.Domain,
	}); err != nil {
		return Result{}, fmt.Errorf("Uumail 账号 %s 保存会话失败：%w", email, err)
	}

	if options.PrepareOnly {
		logging.Done("Uumail", "候选会话已保存", slog.String("email", email))
	} else {
		if err := commitAccountSwitch(options.EnvPath, record, true); err != nil {
			return Result{}, fmt.Errorf("Uumail 账号 %s 已创建：%w", email, err)
		}
		logging.Done("Uumail", "切换当前账号", loggingFields(name, email, info.Username)...)
	}

	return Result{
		Email:       email,
		Username:    info.Username,
		AliasDomain: info.Username + "." + info.Domain,
		Level:       info.Level,
	}, nil
}

func registrationMailbox(options Options) (mail.IMail, error) {
	apiKey := options.SunMailAPIKey
	if strings.TrimSpace(apiKey) == "" {
		value, err := envfile.Value(options.EnvPath, "SUNMAIL_API_KEY")
		if err != nil {
			return nil, err
		}
		apiKey = value
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("Uumail：SUNMAIL_API_KEY 为空")
	}
	return mail.NewSunMailWithConfig(mail.SunMailConfig{
		APIKey:     apiKey,
		BaseURL:    options.SunMailBaseURL,
		HTTPClient: options.HTTPClient,
		Domains:    []string{registrationSunMailDomain},
	}), nil
}

// uumailUsername 生成 3-20 位、[a-zA-Z0-9_-] 的用户名：名字 + 随机后缀。
func uumailUsername(name string) string {
	base := strings.ToLower(strings.SplitN(strings.TrimSpace(name), " ", 2)[0])
	if base == "" {
		base = "uu"
	}
	return base + "-" + strings.ToLower(gox.RandStr(6))
}

func loggingFields(name, email, username string) []any {
	return []any{
		// 邮箱是本轮流程的定位信息；cookie 永远不进入日志。
		slog.String("name", name),
		slog.String("email", email),
		slog.String("username", username),
	}
}
