package mail

import (
	"context"
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
