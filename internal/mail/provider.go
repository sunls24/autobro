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
)

type AddressProviderConfig struct {
	SLAPIKeys            []string
	ManyMeUsername       string
	ManyMeForwardAddress string
}

func NewAddressProvider(ctx context.Context, provider string, cfg AddressProviderConfig) (IMailAddress, error) {
	switch normalizeAddressProvider(provider) {
	case AddressProviderSimpleLogin:
		return NewSimpleLogin(ctx, cfg.SLAPIKeys)
	case AddressProviderSunMail:
		return NewSunMail(), nil
	case AddressProviderManyMe:
		return NewManyMe(cfg.ManyMeUsername, cfg.ManyMeForwardAddress)
	default:
		return nil, fmt.Errorf(
			"unsupported mail address provider %q, use %q, %q or %q",
			provider,
			AddressProviderSimpleLogin,
			AddressProviderSunMail,
			AddressProviderManyMe,
		)
	}
}

func normalizeAddressProvider(provider string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" {
		return AddressProviderSimpleLogin
	}
	return p
}
