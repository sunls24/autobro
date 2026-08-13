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
	"strings"
	"syscall"
)

func main() {
	//slog.SetLogLoggerLevel(slog.LevelDebug)
	cfg := toapi.MustNew()
	count := flag.Int("c", 10, "注册数量")
	mailProvider := flag.String("m", mail.AddressProviderSimpleLogin, "邮箱地址实现: sl, sun 或 mm")
	sunMailDomains := flag.String("d", "", "SunMail 域名后缀，多个用逗号分隔")
	renew := flag.Bool("r", false, "更新失效的 SunMail 账号")
	flag.Parse()
	isSunMail := strings.EqualFold(strings.TrimSpace(*mailProvider), mail.AddressProviderSunMail)
	if !*renew && *count <= 0 {
		panic("count must be greater than 0")
	}
	if *renew && !isSunMail {
		panic("renew only supports sun mail provider")
	}
	domains := strings.Split(*sunMailDomains, ",")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	address, err := mail.NewAddressProvider(ctx, *mailProvider, mail.AddressProviderConfig{
		SLAPIKeys:            cfg.SLAPIKeys,
		SunMailAPIKey:        cfg.SunMailAPIKey,
		SunMailDomains:       domains,
		ManyMeUsername:       cfg.ManyMeUsername,
		ManyMeForwardAddress: cfg.ManyMeForwardAddress,
	})
	if err != nil {
		panic(err)
	}
	bro, err := browser.NewDefault(false)
	if err != nil {
		panic(err)
	}
	defer bro.MustClose()
	worker := toapi.New(mail.From(address, mail.NewSunMail(cfg.SunMailAPIKey)), bro, cfg, isSunMail)
	if *renew {
		err = worker.Renew(ctx)
	} else {
		err = worker.Start(ctx, *count)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		panic(err)
	}
}
