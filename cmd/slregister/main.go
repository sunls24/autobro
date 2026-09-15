package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"time"

	"autobro/internal/logging"
	"autobro/internal/slregister"
)

func main() {
	verbose := flag.Bool("v", false, "输出详细日志")
	envPath := flag.String("env", ".env.local", "环境文件路径")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logging.Configure(level)

	if err := run(*envPath); err != nil && !errors.Is(err, context.Canceled) {
		logging.Failure("SimpleLogin", "注册流程", err)
		os.Exit(1)
	}
}

func run(envPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	result, err := slregister.Register(ctx, slregister.Options{
		EnvPath:       envPath,
		SunMailAPIKey: os.Getenv("SUNMAIL_API_KEY"),
	})
	if err != nil {
		return err
	}

	logging.Done("SimpleLogin", "本轮", slog.String("email", result.Email))
	return nil
}
