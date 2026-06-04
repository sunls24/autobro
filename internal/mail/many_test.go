package mail

import (
	"context"
	"strings"
	"testing"
)

func TestNewManyMe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		username       string
		forwardAddress string
		wantErr        string
	}{
		{
			name:           "valid config",
			username:       " UserName ",
			forwardAddress: " Target.User+Inbox@example.com ",
		},
		{
			name:           "missing username",
			username:       " ",
			forwardAddress: "target@example.com",
			wantErr:        "MANYME_USERNAME is required",
		},
		{
			name:           "missing forward address",
			username:       "username",
			forwardAddress: " ",
			wantErr:        "MANYME_FORWARD_ADDRESS is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider, err := NewManyMe(tt.username, tt.forwardAddress)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("NewManyMe() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewManyMe() error = %v", err)
			}

			forwardAddress, err := provider.ForwardAddress(context.Background(), "anything@manyme.com")
			if err != nil {
				t.Fatalf("ForwardAddress() error = %v", err)
			}
			if forwardAddress != "Target.User+Inbox@example.com" {
				t.Fatalf("ForwardAddress() = %q", forwardAddress)
			}
		})
	}
}

func TestManyMeNewAddress(t *testing.T) {
	t.Parallel()

	provider, err := NewManyMe("UserName", "target@example.com")
	if err != nil {
		t.Fatalf("NewManyMe() error = %v", err)
	}

	address, err := provider.NewAddress(context.Background(), "Alice Smith")
	if err != nil {
		t.Fatalf("NewAddress() error = %v", err)
	}
	if !strings.HasPrefix(address, "username.") {
		t.Fatalf("NewAddress() = %q, want username prefix", address)
	}
	if !strings.HasSuffix(address, "@manyme.com") {
		t.Fatalf("NewAddress() = %q, want manyme domain", address)
	}
}
