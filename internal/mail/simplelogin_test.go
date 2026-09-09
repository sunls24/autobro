package mail

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSimpleLoginDelAddressUsesOwnerAccountAndRotates(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authentication")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	oldBase := simpleAPIBase
	simpleAPIBase = server.URL
	defer func() { simpleAPIBase = oldBase }()

	sl := &simpleLogin{
		accounts: []account{
			{apiKey: "owner-key", forward: "owner@example.com"},
			{apiKey: "current-key", forward: "current@example.com"},
		},
		currentIndex: 1,
		aliases: map[string]aliasRecord{
			"alias@example.com": {id: 42, accountIndex: 0},
		},
	}

	if err := sl.DelAddress(context.Background(), "alias@example.com"); err != nil {
		t.Fatalf("DelAddress() error = %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/aliases/42" {
		t.Fatalf("path = %q, want /aliases/42", gotPath)
	}
	if gotAuth != "owner-key" {
		t.Fatalf("Authentication = %q, want owner-key", gotAuth)
	}
	if sl.currentIndex != 1 {
		t.Fatalf("currentIndex = %d, want 1", sl.currentIndex)
	}
	if _, ok := sl.aliases["alias@example.com"]; ok {
		t.Fatal("deleted alias remains tracked")
	}
}

func TestSimpleLoginForwardAddressUsesAliasOwner(t *testing.T) {
	sl := &simpleLogin{
		accounts: []account{
			{forward: "owner@example.com"},
			{forward: "current@example.com"},
		},
		currentIndex: 1,
		aliases: map[string]aliasRecord{
			"alias@example.com": {id: 42, accountIndex: 0},
		},
	}

	got, err := sl.ForwardAddress(context.Background(), "alias@example.com")
	if err != nil {
		t.Fatalf("ForwardAddress() error = %v", err)
	}
	if got != "owner@example.com" {
		t.Fatalf("ForwardAddress() = %q, want owner@example.com", got)
	}
}

func TestSimpleLoginForgetAddressReleasesRecord(t *testing.T) {
	sl := &simpleLogin{
		aliases: map[string]aliasRecord{
			"alias@example.com": {id: 42, accountIndex: 0},
		},
	}

	sl.ForgetAddress("alias@example.com")
	if _, ok := sl.aliases["alias@example.com"]; ok {
		t.Fatal("forgotten alias remains tracked")
	}
}

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
