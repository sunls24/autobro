package main

import (
	"codex-free/internal/browser"
	"codex-free/internal/chatgpt"
	"codex-free/internal/cpa"
	"codex-free/internal/mail"
	"context"
)

func main() {
	//slog.SetLogLoggerLevel(slog.LevelDebug)

	cfg := chatgpt.MustNew()
	bro, err := browser.NewDefault(false)
	if err != nil {
		panic(err)
	}
	defer bro.MustClose()
	sl, err := mail.NewSimpleLogin(context.TODO(), cfg.SLAPIKeys)
	if err != nil {
		panic(err)
	}
	worker := chatgpt.New(mail.From(sl, mail.NewSunMail()), bro, cpa.New(cfg.CpaURL, cfg.CpaToken))
	err = worker.Start(10)
	if err != nil {
		panic(err)
	}
}
