package toapi

import (
	"errors"
	"log/slog"
	"os"
	"strings"

	"autobro/internal/chatgpt"
	"autobro/internal/logging"
)

const relogCursorFile = "relog-cursor"

func relogStartIndex(accounts []chatgpt.Account) int {
	data, err := os.ReadFile(relogCursorFile)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logging.Warning("续期", "读取轮转游标失败", slog.Any("err", err))
		}
		return 0
	}
	email := strings.TrimSpace(string(data))
	for index, account := range accounts {
		if strings.TrimSpace(account.Email) == email {
			return index
		}
	}
	return 0
}

func saveRelogCursor(email string) {
	if err := writeRelogCursor(email); err != nil {
		logging.Warning("续期", "保存轮转游标失败", slog.Any("err", err))
	}
}

func writeRelogCursor(email string) error {
	file, err := os.CreateTemp(".", ".relog-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.WriteString(strings.TrimSpace(email) + "\n")
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), relogCursorFile)
}
