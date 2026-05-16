package main

import (
	"codex-free/internal/browser"
	"codex-free/internal/mail"
	"codex-free/internal/toapi"
	"context"
	"errors"
	"flag"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	//slog.SetLogLoggerLevel(slog.LevelDebug)
	cfg := toapi.MustNew()
	count := flag.Int("c", 10, "注册数量")
	mailProvider := flag.String("m", mail.AddressProviderSimpleLogin, "邮箱地址实现: sl 或 sun")
	flag.Parse()
	if *count <= 0 {
		panic("count must be greater than 0")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bro, err := browser.NewDefault(false)
	if err != nil {
		panic(err)
	}
	defer bro.MustClose()
	address, err := mail.NewAddressProvider(ctx, *mailProvider, cfg.SLAPIKeys)
	if err != nil {
		panic(err)
	}
	worker := toapi.New(mail.From(address, mail.NewSunMail()), bro, cfg)
	err = worker.Start(ctx, *count)
	if err != nil && !errors.Is(err, context.Canceled) {
		panic(err)
	}
}
