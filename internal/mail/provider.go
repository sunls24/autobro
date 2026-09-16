package mail

import (
	"context"
	"fmt"
	"strings"
)

const (
	AddressProviderSimpleLogin = "sl"
	AddressProviderSunMail     = "sun"
	AddressProviderManyMe      = "mm"
	AddressProviderUumail      = "uu"
)

type AddressProviderConfig struct {
	SLAPIKeys            []string
	SunMailAPIKey        string
	SunMailDomains       []string
	ManyMeUsername       string
	ManyMeForwardAddress string
	UumailAccounts       []string
}

func NewAddressProvider(ctx context.Context, provider string, cfg AddressProviderConfig) (IMailAddress, error) {
	switch NormalizeAddressProvider(provider) {
	case AddressProviderSimpleLogin:
		return NewSimpleLogin(ctx, cfg.SLAPIKeys)
	case AddressProviderSunMail:
		return NewSunMail(cfg.SunMailAPIKey, cfg.SunMailDomains...), nil
	case AddressProviderManyMe:
		return NewManyMe(cfg.ManyMeUsername, cfg.ManyMeForwardAddress)
	case AddressProviderUumail:
		return NewUumailWithConfig(ctx, UumailConfig{
			Accounts:      cfg.UumailAccounts,
			SunMailAPIKey: cfg.SunMailAPIKey,
		})
	default:
		return nil, fmt.Errorf(
			"unsupported mail address provider %q, use %q, %q, %q or %q",
			provider,
			AddressProviderSimpleLogin,
			AddressProviderSunMail,
			AddressProviderManyMe,
			AddressProviderUumail,
		)
	}
}

// NormalizeAddressProvider 归一化邮箱地址实现名。空值与 CLI 的 -m 默认值一致，
// 取 SunMail，避免显式传入空值时静默切换到别的实现。
func NormalizeAddressProvider(provider string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" {
		return AddressProviderSunMail
	}
	return p
}
