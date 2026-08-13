package toapi

import (
	"os"
	"path/filepath"
	"testing"

	"codex-free/internal/chatgpt"
)

func TestAccountStoreKeepsLatestAccountPerEmail(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "accounts.jsonl")
	for _, account := range []*chatgpt.Account{
		{Email: "first@example.com", Password: "password-1", AccessToken: "old-token"},
		{Email: "second@example.com", Password: "password-2", AccessToken: "second-token"},
		{Email: "first@example.com", Password: "password-1", AccessToken: "new-token"},
	} {
		if err := appendAccount(path, account); err != nil {
			t.Fatal(err)
		}
	}

	accounts, err := loadAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("loadAccounts() returned %d accounts, want 2", len(accounts))
	}
	if accounts[0].AccessToken != "new-token" {
		t.Fatalf("first account token = %q, want new-token", accounts[0].AccessToken)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("accounts file permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestAppendAccountAllowsEmptyPassword(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "accounts.jsonl")
	account := &chatgpt.Account{Email: "email-only@example.com", AccessToken: "token"}
	if err := appendAccount(path, account); err != nil {
		t.Fatal(err)
	}
	accounts, err := loadAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].Email != account.Email {
		t.Fatalf("loadAccounts() = %#v, want email-only account", accounts)
	}
}

func TestAccountStoreDiscardsIncompleteTail(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "accounts.jsonl")
	if err := os.WriteFile(path, []byte("{\"email\":\"first@example.com\",\"access_token\":\"first\"}\n{\"email\":\"broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	accounts, err := loadAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 {
		t.Fatalf("loadAccounts() returned %d accounts, want 1", len(accounts))
	}
	if err = appendAccount(path, &chatgpt.Account{Email: "second@example.com", AccessToken: "second"}); err != nil {
		t.Fatal(err)
	}
	accounts, err = loadAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("loadAccounts() after repair returned %d accounts, want 2", len(accounts))
	}
}

func TestAccountStoreDiscardsIncompleteTailWithNewline(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "accounts.jsonl")
	data := "{\"email\":\"first@example.com\",\"access_token\":\"first\"}\n{\"email\":\"broken\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendAccount(path, &chatgpt.Account{Email: "second@example.com", AccessToken: "second"}); err != nil {
		t.Fatal(err)
	}
	accounts, err := loadAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("loadAccounts() returned %d accounts, want 2", len(accounts))
	}
}

func TestAccountStoreKeepsValidTailWithoutNewline(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "accounts.jsonl")
	if err := os.WriteFile(path, []byte("{\"email\":\"first@example.com\",\"access_token\":\"first\"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendAccount(path, &chatgpt.Account{Email: "second@example.com", AccessToken: "second"}); err != nil {
		t.Fatal(err)
	}
	accounts, err := loadAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("loadAccounts() returned %d accounts, want 2", len(accounts))
	}
}
