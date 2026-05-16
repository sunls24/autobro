package mail

import (
	"context"
	"fmt"
	"strings"
)

const (
	AddressProviderSimpleLogin = "sl"
	AddressProviderSunMail     = "sun"
)

func NewAddressProvider(ctx context.Context, provider string, slAPIKeys []string) (IMailAddress, error) {
	switch normalizeAddressProvider(provider) {
	case AddressProviderSimpleLogin:
		return NewSimpleLogin(ctx, slAPIKeys)
	case AddressProviderSunMail:
		return NewSunMail(), nil
	default:
		return nil, fmt.Errorf("unsupported mail address provider %q, use %q or %q", provider, AddressProviderSimpleLogin, AddressProviderSunMail)
	}
}

func normalizeAddressProvider(provider string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" {
		return AddressProviderSimpleLogin
	}
	return p
}
