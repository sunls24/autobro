package mail

import (
	"context"
	"strings"
	"testing"
)

func TestNewAddressProviderManyMe(t *testing.T) {
	t.Parallel()

	provider, err := NewAddressProvider(context.Background(), " mm ", AddressProviderConfig{
		ManyMeUsername:       "username",
		ManyMeForwardAddress: "target@example.com",
	})
	if err != nil {
		t.Fatalf("NewAddressProvider() error = %v", err)
	}

	forwardAddress, err := provider.ForwardAddress(context.Background(), "anything@manyme.com")
	if err != nil {
		t.Fatalf("ForwardAddress() error = %v", err)
	}
	if forwardAddress != "target@example.com" {
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
