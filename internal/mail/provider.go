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
	SLReuseExisting      bool
	SLUsedAddresses      []string
	SunMailAPIKey        string
	SunMailDomains       []string
	ManyMeUsername       string
	ManyMeForwardAddress string
}

func NewAddressProvider(ctx context.Context, provider string, cfg AddressProviderConfig) (IMailAddress, error) {
	switch NormalizeAddressProvider(provider) {
	case AddressProviderSimpleLogin:
		return NewSimpleLogin(ctx, cfg.SLAPIKeys, SimpleLoginOptions{
			ReuseExisting: cfg.SLReuseExisting,
			UsedAddresses: cfg.SLUsedAddresses,
		})
	case AddressProviderSunMail:
		return NewSunMail(cfg.SunMailAPIKey, cfg.SunMailDomains...), nil
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

func NormalizeAddressProvider(provider string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" {
		return AddressProviderSimpleLogin
	}
	return p
}
