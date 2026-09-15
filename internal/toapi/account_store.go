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

	"autobro/internal/chatgpt"
)

const accountsFile = "accounts.jsonl"

// LoadStoredAccounts 读取默认账号文件；文件不存在时返回空列表。
func LoadStoredAccounts() ([]chatgpt.Account, error) {
	return loadAccounts(accountsFile)
}

func appendAccount(path string, account *chatgpt.Account) error {
	if account == nil || strings.TrimSpace(account.Email) == "" {
		return errors.New("账号缺少邮箱地址")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("打开账号文件：%w", err)
	}
	defer f.Close()
	if err = f.Chmod(0o600); err != nil {
		return fmt.Errorf("设置账号文件权限：%w", err)
	}
	if err = repairTail(f); err != nil {
		return err
	}
	if _, err = f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("定位账号文件写入位置：%w", err)
	}
	if err = json.NewEncoder(f).Encode(account); err != nil {
		return fmt.Errorf("写入账号：%w", err)
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
		return fmt.Errorf("创建账号临时文件：%w", err)
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
		return fmt.Errorf("写入账号文件：%w", err)
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("替换账号文件：%w", err)
	}
	return nil
}

func repairTail(f *os.File) error {
	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("读取账号文件末尾：%w", err)
	}
	trimmed := bytes.TrimRight(data, "\r\n")
	if len(trimmed) == 0 {
		return nil
	}
	lineStart := bytes.LastIndexByte(trimmed, '\n') + 1
	if json.Valid(bytes.TrimSpace(trimmed[lineStart:])) {
		if len(data) == len(trimmed) {
			if _, err = f.Write([]byte{'\n'}); err != nil {
				return fmt.Errorf("补全账号记录：%w", err)
			}
		}
		return nil
	}
	if err = f.Truncate(int64(lineStart)); err != nil {
		return fmt.Errorf("丢弃不完整的账号记录：%w", err)
	}
	return nil
}

func loadAccounts(path string) ([]chatgpt.Account, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取账号文件：%w", err)
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
			return nil, fmt.Errorf("解析第 %d 行账号记录：%w", i+1, err)
		}
		email := strings.TrimSpace(account.Email)
		if email == "" {
			return nil, fmt.Errorf("第 %d 行账号记录缺少邮箱地址", i+1)
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
