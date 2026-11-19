package mail

import (
	"context"
	"strings"
	"testing"
)

func TestNewAddressProviderManyMe(t *testing.T) {
	t.Parallel()

	provider, err := NewAddressProvider(context.Background(), " mm ", AddressProviderConfig{
		ManyMeUsername:       " UserName ",
		ManyMeForwardAddress: " Target.User+Inbox@example.com ",
	})
	if err != nil {
		t.Fatalf("NewAddressProvider() error = %v", err)
	}

	address, err := provider.NewAddress(t.Context(), "Alice Smith")
	if err != nil {
		t.Fatalf("NewAddress() error = %v", err)
	}
	if !strings.HasPrefix(address, "username.") || !strings.HasSuffix(address, "@manyme.com") {
		t.Fatalf("NewAddress() = %q, want username prefix and manyme domain", address)
	}
	forwardAddress, err := provider.ForwardAddress(t.Context(), address)
	if err != nil {
		t.Fatalf("ForwardAddress() error = %v", err)
	}
	if forwardAddress != "Target.User+Inbox@example.com" {
		t.Fatalf("ForwardAddress() = %q", forwardAddress)
	}
}

func TestNewAddressProviderUnsupportedIncludesManyMe(t *testing.T) {
	t.Parallel()

	_, err := NewAddressProvider(context.Background(), "invalid", AddressProviderConfig{})
	if err == nil {
		t.Fatal("NewAddressProvider() error = nil, want unsupported provider error")
	}
	if !strings.Contains(err.Error(), AddressProviderManyMe) {
		t.Fatalf("NewAddressProvider() error = %q, want %q", err, AddressProviderManyMe)
	}
}

func TestNewAddressProviderUumailRequiresConfig(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		missing string
		config  AddressProviderConfig
	}{
		{"UUMAIL_ACCOUNTS", AddressProviderConfig{SunMailAPIKey: "test-sunmail-key"}},
		{"SUNMAIL_API_KEY", AddressProviderConfig{UumailAccounts: []string{"a@chato.eu.org"}}},
	} {
		t.Run(tc.missing, func(t *testing.T) {
			_, err := NewAddressProvider(t.Context(), AddressProviderUumail, tc.config)
			if err == nil || !strings.Contains(err.Error(), tc.missing) {
				t.Fatalf("NewAddressProvider() error = %v, want %q", err, tc.missing)
			}
		})
	}
}
