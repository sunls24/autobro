package mail

import (
	"errors"
	"testing"
)

func TestSimpleLoginNewAddressRotatesRestrictedAccounts(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "rate limited",
			err:  errors.New("429 TOO MANY REQUESTS"),
		},
		{
			name: "free alias limit",
			err:  errors.New(`400 BAD REQUEST: {"error":"You have reached the limitation of a free account with the maximum of 10 aliases, please upgrade your plan to create more aliases"}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sl := &simpleLogin{accounts: []account{{apiKey: "first"}, {apiKey: "second"}}}
			attempts := 0

			address, err := sl.newAddress(func() (string, error) {
				attempts++
				if sl.currentIndex == 0 {
					return "", tt.err
				}
				return "created@example.com", nil
			})
			if err != nil {
				t.Fatalf("newAddress() error = %v", err)
			}
			if address != "created@example.com" {
				t.Fatalf("newAddress() address = %q", address)
			}
			if attempts != 2 {
				t.Fatalf("newAddress() attempts = %d, want 2", attempts)
			}
			if sl.currentIndex != 1 {
				t.Fatalf("currentIndex = %d, want 1", sl.currentIndex)
			}
		})
	}
}

func TestSimpleLoginNewAddressDoesNotRotateOtherErrors(t *testing.T) {
	sl := &simpleLogin{accounts: []account{{apiKey: "first"}, {apiKey: "second"}}}
	wantErr := errors.New("400 BAD REQUEST: invalid alias")
	attempts := 0

	_, err := sl.newAddress(func() (string, error) {
		attempts++
		return "", wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("newAddress() error = %v, want %v", err, wantErr)
	}
	if attempts != 1 {
		t.Fatalf("newAddress() attempts = %d, want 1", attempts)
	}
	if sl.currentIndex != 0 {
		t.Fatalf("currentIndex = %d, want 0", sl.currentIndex)
	}
}
