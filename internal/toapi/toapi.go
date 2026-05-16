package toapi

import (
	"codex-free/internal/chatgpt"
	"context"
	"fmt"
	"log/slog"
	"time"

	"codex-free/internal/mail"

	"github.com/go-rod/rod"
	"github.com/sunls24/gox/network/client"
	"github.com/sunls24/gox/network/header"
)

type Worker struct {
	flow          *chatgpt.Flow
	accountsURL   string
	accountsToken string
}

func New(m mail.IMail, bro *rod.Browser, cfg *Config) *Worker {
	return &Worker{
		flow:          chatgpt.New(chatgpt.WithIMail(m), chatgpt.WithBackground(), chatgpt.WithBro(bro), chatgpt.WithDelAddress()),
		accountsURL:   cfg.AccountsURL,
		accountsToken: cfg.AccountsToken,
	}
}

func (w *Worker) Start(ctx context.Context, count int) error {
	slog.Info("启动 chatgpt2api 自动化注册", slog.Int("count", count))
	for i := 0; i < count; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		slog.Info("==> 开始注册", slog.String("progress", fmt.Sprintf("%d/%d", i+1, count)))
		if err := rod.Try(func() { w.registerOne(ctx) }); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			slog.Error("==> 注册失败\n" + err.Error())
		}
	}
	return nil
}

func (w *Worker) registerOne(ctx context.Context) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, time.Minute*5)
	defer cancel()

	a := w.flow.MustRegisterOrLogin(ctx, nil)
	slog.Info("-> 上传 access token")
	if err := w.uploadAccessToken(ctx, a.AccessToken); err != nil {
		panic(err)
	}

	slog.Info("==> 注册成功", slog.String("用时", time.Since(start).String()), slog.String("address", a.Email))
}

func (w *Worker) uploadAccessToken(ctx context.Context, accessToken string) error {
	_, err := client.Post(ctx, w.accountsURL, map[string][]string{
		"tokens": {accessToken},
	}, header.New().ContentTypeJSON().Authorization(w.accountsToken).Get()...)
	return err
}
