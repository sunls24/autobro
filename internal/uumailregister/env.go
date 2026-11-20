package uumailregister

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"autobro/internal/envfile"
	"autobro/internal/mail"
)

type pendingAccountSwitch struct {
	SessionPath string
	Previous    []string
	NewAccount  string
	Username    string `json:",omitempty"` // 用于确认缓存会话属于本次新注册。
}

var errPendingSessionMissing = errors.New("缺少候选账号的本地会话")

func activateAccount(options Options, previous []string, email string) error {
	record, err := saveSwitch(options, previous, email, "")
	if err != nil {
		return err
	}
	return commitAccountSwitch(options.EnvPath, record, false)
}

func saveSwitch(options Options, previous []string, email, username string) (pendingAccountSwitch, error) {
	sessionPath, err := filepath.Abs(mail.UumailSessionPath(options.SessionPath))
	if err != nil {
		return pendingAccountSwitch{}, err
	}
	record := pendingAccountSwitch{
		SessionPath: sessionPath, Previous: previous, NewAccount: email, Username: username,
	}
	path := options.EnvPath + ".uu-retire"
	temp, err := os.CreateTemp(filepath.Dir(path), ".uu-retire-*.tmp")
	if err != nil {
		return pendingAccountSwitch{}, err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err := json.NewEncoder(temp).Encode(record); err != nil {
		return pendingAccountSwitch{}, err
	}
	if err := temp.Sync(); err != nil {
		return pendingAccountSwitch{}, err
	}
	if err := temp.Close(); err != nil {
		return pendingAccountSwitch{}, err
	}
	return record, os.Rename(temp.Name(), path)
}

// ResumeAccountSwitch completes a local switch using a saved session.
func ResumeAccountSwitch(envPath string) (string, error) {
	path := envPath + ".uu-retire"
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var record pendingAccountSwitch
	if err := json.Unmarshal(data, &record); err != nil {
		return "", fmt.Errorf("读取 UU 切换记录：%w", err)
	}
	if !uumailAccountEmail.MatchString(record.NewAccount) || !filepath.IsAbs(record.SessionPath) {
		return "", errors.New("UU 切换记录无效")
	}
	if err := commitAccountSwitch(envPath, record, false); err != nil {
		// A new registration writes the record before saving the session.
		// If the session was never saved, the next attempt can start fresh.
		if record.Username != "" && errors.Is(err, errPendingSessionMissing) {
			return "", os.Remove(path)
		}
		return "", err
	}
	return record.NewAccount, nil
}

func commitAccountSwitch(envPath string, record pendingAccountSwitch, sessionJustSaved bool) error {
	path := envPath + ".uu-retire"
	snapshot, current, err := readAccountConfig(envPath)
	if err != nil {
		return err
	}
	if len(current) != 1 || !strings.EqualFold(current[0], record.NewAccount) {
		if !sessionJustSaved {
			ok, err := mail.HasUumailSession(record.SessionPath, record.NewAccount, record.Username)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%w，请核对切换记录 %s", errPendingSessionMissing, path)
			}
		}
		if !slices.Equal(current, record.Previous) {
			return fmt.Errorf("当前 UU 配置已更改，请核对切换记录 %s", path)
		}
		if err := envfile.UpdateIfUnchanged(envPath, envUumailAccountsKey, record.NewAccount, snapshot); err != nil {
			return fmt.Errorf("提交 UU 配置，切换记录保留在 %s：%w", path, err)
		}
	}
	if len(record.Previous) > 0 {
		if err := mail.RetireUumailSessions(record.SessionPath, record.Previous, record.NewAccount); err != nil {
			return fmt.Errorf("新账号已启用，清理旧会话待重试：%w", err)
		}
	}
	return os.Remove(path)
}

func readAccountConfig(path string) (string, []string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil, errors.New("Uumail：.env.local 路径为空")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", nil, fmt.Errorf("读取 %s：%w", path, err)
	}
	snapshot := string(content)
	value, err := envfile.ParseValue(snapshot, envUumailAccountsKey)
	if err != nil {
		return "", nil, err
	}
	var accounts []string
	for part := range strings.SplitSeq(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			if !uumailAccountEmail.MatchString(strings.ToLower(part)) {
				return "", nil, errors.New("UUMAIL_ACCOUNTS 请直接填写有效邮箱，多个邮箱用逗号分隔")
			}
			accounts = append(accounts, part)
		}
	}
	return snapshot, accounts, nil
}
