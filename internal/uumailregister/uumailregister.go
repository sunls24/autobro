package uumailregister

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

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

// Options 描述一次 Uumail 账号注册。
type Options struct {
	EnvPath        string
	SessionPath    string
	SunMailAPIKey  string
	SunMailBaseURL string
	APIBase        string
	SSOBase        string
	HTTPClient     *http.Client
}

// Result 包含注册完成的账号信息；cookie 写入会话缓存，不进入日志。
type Result struct {
	Email       string
	Username    string
	AliasDomain string
	Level       string
}

// Register 注册一个 Uumail 账号：用 SunMail 地址完成邮箱验证码登录，完善资料，
// 把会话写入缓存文件，并把账号地址前插去重更新到 envPath 的 UUMAIL_ACCOUNTS。
func Register(ctx context.Context, options Options) (Result, error) {
	if strings.TrimSpace(options.SunMailAPIKey) == "" {
		return Result{}, errors.New("Uumail 注册失败：SUNMAIL_API_KEY 为空")
	}
	if strings.TrimSpace(options.EnvPath) == "" {
		return Result{}, errors.New("Uumail 注册失败：.env.local 路径为空")
	}
	if err := checkEnvWritable(options.EnvPath); err != nil {
		return Result{}, fmt.Errorf("检查 %s 写入能力失败：%w", options.EnvPath, err)
	}

	sunMail := mail.NewSunMailWithConfig(mail.SunMailConfig{
		APIKey:     options.SunMailAPIKey,
		BaseURL:    options.SunMailBaseURL,
		HTTPClient: options.HTTPClient,
		Domains:    []string{registrationSunMailDomain},
	})
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
		return Result{}, fmt.Errorf("Uumail 邮箱验证码登录失败：%w", err)
	}
	logging.Done("Uumail", "账号登录")

	info, err := client.UpdateProfile(ctx, cookie, username, email)
	if err != nil {
		return Result{}, fmt.Errorf("Uumail 完善账号资料失败：%w", err)
	}
	logging.Done("Uumail", "资料已完善", slog.String("username", info.Username), slog.String("level", info.Level))

	if err := mail.StoreUumailSession(options.SessionPath, email, mail.UumailSession{
		Cookie:   cookie,
		Username: info.Username,
		Domain:   info.Domain,
	}); err != nil {
		return Result{}, err
	}

	if err := SetEnvListValue(options.EnvPath, envUumailAccountsKey, email); err != nil {
		return Result{}, fmt.Errorf("更新 %s 失败（Uumail 账号已创建，邮箱 %s；可手动加入 UUMAIL_ACCOUNTS）：%w", options.EnvPath, email, err)
	}
	logging.Done("Uumail", "写入账号列表", loggingFields(name, email, username)...)

	return Result{
		Email:       email,
		Username:    info.Username,
		AliasDomain: info.Username + "." + info.Domain,
		Level:       info.Level,
	}, nil
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
