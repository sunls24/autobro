package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"strings"
	"time"

	"autobro/internal/logging"
	"autobro/internal/uumailregister"
)

func main() {
	verbose := flag.Bool("v", false, "输出详细日志")
	envPath := flag.String("env", ".env.local", "环境文件路径")
	sessionPath := flag.String("sessions", "", "Uumail 会话缓存路径（默认 uumail_sessions.json）")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logging.Configure(level)

	if err := run(*envPath, *sessionPath); err != nil && !errors.Is(err, context.Canceled) {
		logging.Failure("Uumail", "注册流程", err)
		os.Exit(1)
	}
}

func run(envPath, sessionPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	apiKey := os.Getenv("SUNMAIL_API_KEY")
	if strings.TrimSpace(apiKey) == "" {
		data, err := os.ReadFile(envPath)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			line = strings.TrimPrefix(line, "export ")
			if strings.HasPrefix(line, "SUNMAIL_API_KEY=") {
				apiKey = strings.TrimSpace(strings.TrimPrefix(line, "SUNMAIL_API_KEY="))
				apiKey = strings.Trim(apiKey, "\"'")
				if i := strings.Index(apiKey, " #"); i >= 0 {
					apiKey = strings.TrimSpace(apiKey[:i])
				}
				break
			}
		}
	}
	result, err := uumailregister.Register(ctx, uumailregister.Options{
		EnvPath:       envPath,
		SessionPath:   sessionPath,
		SunMailAPIKey: apiKey,
	})
	if err != nil {
		return err
	}

	logging.Done("Uumail", "本轮",
		slog.String("email", result.Email),
		slog.String("aliasDomain", result.AliasDomain),
		slog.String("level", result.Level),
	)
	return nil
}
