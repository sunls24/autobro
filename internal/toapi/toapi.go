package toapi

import (
	"codex-free/internal/chatgpt"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
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
	storeAccounts bool
}

func New(m mail.IMail, bro *rod.Browser, cfg *Config, storeAccounts bool) *Worker {
	return &Worker{
		flow:          chatgpt.New(chatgpt.WithIMail(m), chatgpt.WithBackground(), chatgpt.WithBro(bro), chatgpt.WithDelAddress()),
		accountsURL:   cfg.AccountsURL,
		accountsToken: cfg.AccountsToken,
		storeAccounts: storeAccounts,
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
	if w.storeAccounts {
		if err := appendAccount(accountsFile, a); err != nil {
			panic(err)
		}
	}
	slog.Info("-> 上传 access token")
	if err := w.uploadAccessToken(ctx, a.AccessToken); err != nil {
		panic(err)
	}

	slog.Info("==> 注册成功", slog.String("用时", time.Since(start).String()), slog.String("address", a.Email))
}

type remoteAccount struct {
	AccessToken string `json:"access_token"`
	Status      string `json:"status"`
}

func (w *Worker) Renew(ctx context.Context) error {
	accounts, err := loadAccounts(accountsFile)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		return fmt.Errorf("no local accounts found in %s", accountsFile)
	}
	remote, err := w.listRemoteAccounts(ctx)
	if err != nil {
		return err
	}

	for i := range accounts {
		account := &accounts[i]
		oldToken := account.AccessToken
		remoteAccount, exists := remote[oldToken]
		if exists && remoteAccount.Status != "异常" {
			slog.Info("跳过可用账号", slog.String("email", account.Email), slog.String("status", remoteAccount.Status))
			continue
		}

		slog.Info("重新登录账号", slog.String("email", account.Email))
		renewCtx, cancel := context.WithTimeout(ctx, time.Minute*5)
		err = rod.Try(func() { w.flow.MustRegisterOrLogin(renewCtx, account) })
		cancel()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			slog.Error("重新登录失败", slog.String("email", account.Email), slog.Any("err", err))
			continue
		}
		if strings.TrimSpace(account.AccessToken) == "" {
			slog.Error("重新登录未获取到 access token", slog.String("email", account.Email))
			continue
		}
		if err = w.uploadAccessToken(ctx, account.AccessToken); err != nil {
			slog.Error("上传新 access token 失败", slog.String("email", account.Email), slog.Any("err", err))
			continue
		}
		if err = appendAccount(accountsFile, account); err != nil {
			return err
		}
		if account.AccessToken != oldToken && exists && remoteAccount.Status == "异常" {
			if err = w.deleteAccessToken(ctx, oldToken); err != nil {
				slog.Error("删除旧 access token 失败", slog.String("email", account.Email), slog.Any("err", err))
			}
		}
		slog.Info("账号更新完成", slog.String("email", account.Email))
	}
	return nil
}

func (w *Worker) listRemoteAccounts(ctx context.Context) (map[string]remoteAccount, error) {
	body, err := client.Get(ctx, w.accountsURL, header.New().Authorization(w.accountsToken).Get()...)
	if err != nil {
		return nil, fmt.Errorf("list remote accounts: %w", err)
	}
	var response struct {
		Items []remoteAccount `json:"items"`
	}
	if err = json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode remote accounts: %w", err)
	}
	accounts := make(map[string]remoteAccount, len(response.Items))
	for _, account := range response.Items {
		if account.AccessToken != "" {
			accounts[account.AccessToken] = account
		}
	}
	return accounts, nil
}

func (w *Worker) deleteAccessToken(ctx context.Context, accessToken string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, w.accountsURL, client.NewBody(map[string][]string{
		"tokens": {accessToken},
	}))
	if err != nil {
		return err
	}
	_, err = client.Do(req, header.New().ContentTypeJSON().Authorization(w.accountsToken).Get()...)
	return err
}

func (w *Worker) uploadAccessToken(ctx context.Context, accessToken string) error {
	body, err := client.Post(ctx, w.accountsURL, map[string][]string{
		"tokens": {accessToken},
	}, header.New().ContentTypeJSON().Authorization(w.accountsToken).Get()...)
	if err != nil {
		return err
	}
	var response struct {
		Items []remoteAccount `json:"items"`
	}
	if err = json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decode uploaded account: %w", err)
	}
	for _, account := range response.Items {
		if account.AccessToken == accessToken {
			if account.Status == "异常" {
				return errors.New("uploaded access token is abnormal")
			}
			return nil
		}
	}
	return errors.New("uploaded access token not found in remote accounts")
}
