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
	if err := writeRelogFile(relogCursorFile, []byte(strings.TrimSpace(email)+"\n")); err != nil {
		logging.Warning("续期", "保存轮转游标失败", slog.Any("err", err))
	}
}
