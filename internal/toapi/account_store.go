package toapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"codex-free/internal/chatgpt"
)

const accountsFile = "accounts.jsonl"

const AccountsFile = accountsFile

func LoadAccounts(path string) ([]chatgpt.Account, error) {
	return loadAccounts(path)
}

func appendAccount(path string, account *chatgpt.Account) error {
	if account == nil || strings.TrimSpace(account.Email) == "" {
		return errors.New("email is required")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open accounts file: %w", err)
	}
	defer f.Close()
	if err = f.Chmod(0o600); err != nil {
		return fmt.Errorf("set accounts file permissions: %w", err)
	}
	if err = repairTail(f); err != nil {
		return err
	}
	if _, err = f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek accounts file: %w", err)
	}
	if err = json.NewEncoder(f).Encode(account); err != nil {
		return fmt.Errorf("write account: %w", err)
	}
	return nil
}

func removeAccount(path, email string) error {
	accounts, err := loadAccounts(path)
	if err != nil {
		return err
	}
	remaining := accounts[:0]
	for i := range accounts {
		if !strings.EqualFold(strings.TrimSpace(accounts[i].Email), strings.TrimSpace(email)) {
			remaining = append(remaining, accounts[i])
		}
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".accounts-*.jsonl")
	if err != nil {
		return fmt.Errorf("create temporary accounts file: %w", err)
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)
	if err = f.Chmod(0o600); err == nil {
		encoder := json.NewEncoder(f)
		for i := range remaining {
			if err = encoder.Encode(&remaining[i]); err != nil {
				break
			}
		}
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write accounts file: %w", err)
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace accounts file: %w", err)
	}
	return nil
}

func repairTail(f *os.File) error {
	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("read accounts file tail: %w", err)
	}
	trimmed := bytes.TrimRight(data, "\r\n")
	if len(trimmed) == 0 {
		return nil
	}
	lineStart := bytes.LastIndexByte(trimmed, '\n') + 1
	if json.Valid(bytes.TrimSpace(trimmed[lineStart:])) {
		if len(data) == len(trimmed) {
			if _, err = f.Write([]byte{'\n'}); err != nil {
				return fmt.Errorf("complete account record: %w", err)
			}
		}
		return nil
	}
	if err = f.Truncate(int64(lineStart)); err != nil {
		return fmt.Errorf("discard incomplete account record: %w", err)
	}
	return nil
}

func loadAccounts(path string) ([]chatgpt.Account, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read accounts file: %w", err)
	}

	latest := make(map[string]chatgpt.Account)
	order := make([]string, 0)
	lines := bytes.Split(data, []byte{'\n'})
	lastRecord := -1
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) > 0 {
			lastRecord = i
		}
	}
	for i, record := range lines {
		if len(bytes.TrimSpace(record)) == 0 {
			continue
		}
		var account chatgpt.Account
		if err = json.Unmarshal(record, &account); err != nil {
			if i == lastRecord {
				break
			}
			return nil, fmt.Errorf("decode account at line %d: %w", i+1, err)
		}
		email := strings.TrimSpace(account.Email)
		if email == "" {
			return nil, fmt.Errorf("account at line %d has no email", i+1)
		}
		if _, ok := latest[email]; !ok {
			order = append(order, email)
		}
		latest[email] = account
	}
	accounts := make([]chatgpt.Account, 0, len(order))
	for _, email := range order {
		accounts = append(accounts, latest[email])
	}
	return accounts, nil
}
