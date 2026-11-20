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
	"github.com/gofrs/flock"
)

func main() {
	verbose := flag.Bool("v", false, "输出详细日志")
	envPath := flag.String("env", ".env.local", "环境文件路径")
	sessionPath := flag.String("sessions", "", "Uumail 会话缓存路径（默认 uumail_sessions.json）")
	prepare := flag.Bool("prepare", false, "准备候选账号并保存会话，等待单独启用")
	ensure := flag.Bool("ensure", false, "仅在当前账号缺失或不可用时注册并替换")
	var useEmail string
	flag.Func("use", "验证已登录的 Uumail 邮箱并将其设为唯一的本地账号", func(value string) error {
		useEmail = strings.TrimSpace(value)
		if useEmail == "" {
			return errors.New("请提供要切换的 Uumail 账号邮箱")
		}
		return nil
	})
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logging.Configure(level)

	step := "注册流程"
	if useEmail != "" {
		step = "切换账号"
	} else if *ensure {
		step = "确保当前账号"
	}
	if err := run(*envPath, *sessionPath, useEmail, *prepare, *ensure); err != nil && !errors.Is(err, context.Canceled) {
		logging.Failure("Uumail", step, err)
		os.Exit(1)
	}
}

func run(envPath, sessionPath, useEmail string, prepare, ensure bool) error {
	if useEmail != "" && (prepare || ensure) {
		return errors.New("-use 不能与 -prepare 或 -ensure 同时使用")
	}
	if prepare && ensure {
		return errors.New("-prepare 和 -ensure 不能同时使用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	lock := flock.New(envPath + ".uu.lock")
	ok, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("已有 UU 账号操作正在执行")
	}
	defer lock.Close()
	options := uumailregister.Options{
		EnvPath: envPath, SessionPath: sessionPath, SunMailAPIKey: os.Getenv("SUNMAIL_API_KEY"), PrepareOnly: prepare,
	}
	if !prepare {
		recovered, err := uumailregister.ResumeAccountSwitch(envPath)
		if err != nil {
			return err
		}
		if recovered != "" {
			logging.Done("Uumail", "恢复账号切换", slog.String("email", recovered))
			if !ensure && (useEmail == "" || strings.EqualFold(recovered, useEmail)) {
				return nil
			}
		}
	}
	if useEmail != "" {
		return uumailregister.UseAccount(ctx, options, useEmail)
	}
	if ensure {
		return uumailregister.EnsureAccount(ctx, options)
	}
	result, err := uumailregister.Register(ctx, options)
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
