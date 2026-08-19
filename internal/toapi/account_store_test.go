package toapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codex-free/internal/chatgpt"
)

func TestAccountStoreKeepsLatestAccountPerEmail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.jsonl")
	for _, account := range []*chatgpt.Account{{Email: "first@example.com", Password: "old-password", ForwardMail: "first-forward@example.com"}, {Email: "second@example.com", Password: "second-password"}, {Email: "first@example.com", Password: "new-password", ForwardMail: "new-forward@example.com"}} {
		if err := appendAccount(path, account); err != nil {
			t.Fatal(err)
		}
	}
	accounts, err := loadAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 || accounts[0].Password != "new-password" || accounts[0].ForwardMail != "new-forward@example.com" {
		t.Fatalf("accounts = %#v", accounts)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "access_token") {
		t.Fatalf("accounts file contains access_token")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("accounts file permissions = %o", info.Mode().Perm())
	}
}

func TestAppendAccountRequiresEmail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.jsonl")
	for _, account := range []*chatgpt.Account{nil, {Password: "password"}} {
		if err := appendAccount(path, account); err == nil {
			t.Fatalf("appendAccount(%#v) returned nil error", account)
		}
	}
}

func TestAppendAccountAllowsEmptyPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.jsonl")
	if err := appendAccount(path, &chatgpt.Account{Email: "email-only@example.com"}); err != nil {
		t.Fatalf("appendAccount() returned error: %v", err)
	}
	accounts, err := loadAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].Email != "email-only@example.com" || accounts[0].Password != "" {
		t.Fatalf("accounts = %#v", accounts)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"password"`) {
		t.Fatalf("accounts file contains an empty password field: %s", data)
	}
}

func TestAccountStoreRepairsIncompleteTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.jsonl")
	data := "{\"email\":\"first@example.com\",\"password\":\"first\"}\n{\"email\":\"broken\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendAccount(path, &chatgpt.Account{Email: "second@example.com", Password: "second"}); err != nil {
		t.Fatal(err)
	}
	accounts, err := loadAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("loadAccounts() returned %d accounts", len(accounts))
	}
}

func TestAccountStoreCompletesValidTailWithoutNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.jsonl")
	if err := os.WriteFile(path, []byte("{\"email\":\"first@example.com\",\"password\":\"first\"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendAccount(path, &chatgpt.Account{Email: "second@example.com", Password: "second"}); err != nil {
		t.Fatal(err)
	}
	accounts, err := loadAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("loadAccounts() returned %d accounts", len(accounts))
	}
}
