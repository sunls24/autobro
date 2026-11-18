package toapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"time"

	"autobro/internal/logging"
)

const relogChecksFile = "relog-checks.json"
const relogCheckInterval = 12 * time.Hour

type relogChecks map[string]time.Time

func loadRelogChecks() relogChecks {
	data, err := os.ReadFile(relogChecksFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var checks relogChecks
	if err == nil {
		err = json.Unmarshal(data, &checks)
	}
	if err != nil {
		logging.Warning("续期", "读取查询冷却缓存失败", slog.Any("err", err))
		return nil
	}
	return checks
}

func (updates relogChecks) save() {
	if len(updates) == 0 {
		return
	}
	now := time.Now()
	for email, nextCheck := range loadRelogChecks() {
		if _, updated := updates[email]; !updated && nextCheck.After(now) {
			updates[email] = nextCheck
		}
	}
	if err := updates.write(); err != nil {
		logging.Warning("续期", "保存查询冷却缓存失败", slog.Any("err", err))
	}
}

func (checks relogChecks) write() error {
	data, err := json.Marshal(checks)
	if err != nil {
		return err
	}
	return writeRelogFile(relogChecksFile, data)
}

func writeRelogFile(path string, data []byte) error {
	file, err := os.CreateTemp(".", ".relog-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
