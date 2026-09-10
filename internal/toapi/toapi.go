package toapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"codex-free/internal/chatgpt"
	"codex-free/internal/mail"
	"codex-free/internal/scenemint"

	"github.com/go-rod/rod"
)

type Worker struct {
	flow               *chatgpt.Flow
	sceneMint          *scenemint.Client
	storeAccounts      bool
	simpleLoginCleaner mail.IMailAddress
}

func New(m mail.IMail, bro *rod.Browser, sceneMint *scenemint.Client, storeAccounts bool, simpleLoginCleaner mail.IMailAddress) *Worker {
	return &Worker{
		flow:               chatgpt.New(chatgpt.WithIMail(m), chatgpt.WithBackground(), chatgpt.WithBro(bro)),
		sceneMint:          sceneMint,
		storeAccounts:      storeAccounts,
		simpleLoginCleaner: simpleLoginCleaner,
	}
}

func (w *Worker) Start(ctx context.Context, count int) error {
	slog.Info("启动 chatgpt 自动化注册", slog.Int("count", count))
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
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	a := w.flow.MustRegisterOrLogin(ctx, nil)
	if w.storeAccounts {
		if err := appendAccount(accountsFile, a); err != nil {
			panic(err)
		}
	}
	if err := w.sceneMint.Upload(ctx, a.AccessToken); err != nil {
		panic(err)
	}
	slog.Info("==> 注册成功", slog.String("用时", time.Since(start).String()), slog.String("address", a.Email))
}

func (w *Worker) Renew(ctx context.Context) error {
	accounts, err := loadAccounts(accountsFile)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		return fmt.Errorf("no local accounts found in %s", accountsFile)
	}
	for i := range accounts {
		account := &accounts[i]
		remote, queryErr := w.sceneMint.GetByEmail(ctx, account.Email)
		if queryErr != nil {
			slog.Error("查询 SceneMint 账号失败", slog.String("email", account.Email), slog.Any("err", queryErr))
			continue
		}
		if remote != nil {
			switch remote.Status {
			case "active":
				if remote.TokenExpiresAt.After(time.Now()) {
					slog.Info("跳过有效账号", slog.String("email", account.Email))
					continue
				}
			case "invalid", "disabled":
			default:
				slog.Error("未知 SceneMint 账号状态", slog.String("email", account.Email), slog.String("status", remote.Status))
				continue
			}
		}

		slog.Info("重新登录账号", slog.String("email", account.Email))
		renewCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		err = rod.Try(func() { w.flow.MustRegisterOrLogin(renewCtx, account) })
		cancel()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if errors.Is(err, chatgpt.ErrAccountDeactivated) {
				w.cleanupDeactivatedAccount(account)
				if removeErr := removeAccount(accountsFile, account.Email); removeErr != nil {
					return removeErr
				}
				slog.Warn("账号已被删除或停用，已移除本地记录", slog.String("email", account.Email))
				continue
			}
			slog.Error("重新登录失败", slog.String("email", account.Email), slog.Any("err", err))
			continue
		}
		if strings.TrimSpace(account.AccessToken) == "" {
			slog.Error("重新登录未获取到 access token", slog.String("email", account.Email))
			continue
		}
		if err = w.sceneMint.Upload(ctx, account.AccessToken); err != nil {
			slog.Error("上传 SceneMint 账号失败", slog.String("email", account.Email), slog.Any("err", err))
			continue
		}
		slog.Info("账号更新完成", slog.String("email", account.Email))
	}
	return nil
}

func (w *Worker) cleanupDeactivatedAccount(account *chatgpt.Account) {
	provider := strings.ToLower(strings.TrimSpace(account.MailProvider))
	if provider != mail.AddressProviderSimpleLogin {
		return
	}
	if w.simpleLoginCleaner == nil {
		slog.Error("停用账号缺少 SimpleLogin 清理器", slog.String("email", account.Email))
		return
	}
	if account.ProviderAddressID <= 0 || account.ProviderOwnerID <= 0 {
		slog.Warn("停用账号缺少 SimpleLogin 别名元数据，跳过远程删除", slog.String("email", account.Email))
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	metadata := mail.AddressMetadata{
		Email:     account.Email,
		Provider:  mail.AddressProviderSimpleLogin,
		AddressID: account.ProviderAddressID,
		OwnerID:   account.ProviderOwnerID,
	}
	if err := w.simpleLoginCleaner.DelAddressByMetadata(cleanupCtx, metadata); err != nil {
		slog.Error("删除停用账号邮箱地址失败", slog.String("email", account.Email), slog.String("provider", provider), slog.Any("err", err))
	}
}
