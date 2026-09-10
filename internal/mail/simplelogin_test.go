package mail

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestSimpleLoginReusesAliasAndDeletesByMetadata(t *testing.T) {
	var gotDeleteAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/aliases":
			if r.URL.Query().Get("page_id") != "0" {
				http.Error(w, "unexpected page", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"aliases":[{"id":7,"email":"free@example.com","enabled":true,"mailboxes":[{"id":22}]}]}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/aliases/7":
			gotDeleteAuth = r.Header.Get("Authentication")
			_, _ = w.Write([]byte(`{"deleted":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldBase := simpleAPIBase
	simpleAPIBase = server.URL
	defer func() { simpleAPIBase = oldBase }()

	sl := &simpleLogin{
		accounts: []account{
			{mailboxId: 11, apiKey: "first-key", forward: "first@example.com"},
			{mailboxId: 22, apiKey: "owner-key", forward: "owner@example.com"},
		},
		aliases:       make(map[string]aliasRecord),
		usedAddresses: map[string]struct{}{"used@example.com": {}},
		reuseExisting: true,
	}

	address, err := sl.NewAddress(context.Background(), "ignored")
	if err != nil {
		t.Fatalf("NewAddress() error = %v", err)
	}
	if address != "free@example.com" {
		t.Fatalf("NewAddress() = %q, want free@example.com", address)
	}
	metadata := sl.Metadata(address)
	if metadata.AddressID != 7 || metadata.OwnerID != 22 {
		t.Fatalf("Metadata() = %+v, want address=7 owner=22", metadata)
	}
	if got, err := sl.ForwardAddress(context.Background(), address); err != nil || got != "owner@example.com" {
		t.Fatalf("ForwardAddress() = %q, %v, want owner@example.com", got, err)
	}
	// 删除只依赖持久化的 ID，不依赖当前进程里的 alias 缓存。
	sl.aliases = make(map[string]aliasRecord)
	if err := sl.DelAddressByMetadata(context.Background(), metadata); err != nil {
		t.Fatalf("DelAddressByMetadata() error = %v", err)
	}
	if gotDeleteAuth != "owner-key" {
		t.Fatalf("delete Authentication = %q, want owner-key", gotDeleteAuth)
	}
	if _, ok := sl.usedAddresses["free@example.com"]; ok {
		t.Fatal("deleted alias remains marked used")
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
