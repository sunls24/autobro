package toapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"autobro/internal/chatgpt"
	"autobro/internal/logging"
	"autobro/internal/scenemint"
)

// AuthenticatorFactory creates an isolated authentication session for one
// account attempt and returns its cleanup function.
type AuthenticatorFactory func() (chatgpt.Authenticator, func() error, error)

type Worker struct {
	authenticatorFactory AuthenticatorFactory
	sceneMint            *scenemint.Client
	storeAccounts        bool
}

func New(authenticatorFactory AuthenticatorFactory, sceneMint *scenemint.Client, storeAccounts bool) *Worker {
	return &Worker{
		authenticatorFactory: authenticatorFactory,
		sceneMint:            sceneMint,
		storeAccounts:        storeAccounts,
	}
}

func logBatchResult(action string, total, failed int) {
	args := []any{slog.Int("count", total), slog.Int("failed", failed)}
	if failed == 0 {
		logging.Done(action, "批次", args...)
		return
	}
	logging.Warning(action, "批次未完全成功", args...)
}

func (w *Worker) Start(ctx context.Context, count int) error {
	logging.Step("注册", "开始", slog.Int("count", count))
	var failures []error
	for i := 0; i < count; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		index, total := i+1, count
		logging.Step("注册", "开始认证", slog.Int("index", index), slog.Int("total", total))
		if err := w.runWithAuthenticator(func(authenticator chatgpt.Authenticator) error {
			return w.registerOne(ctx, authenticator, index, total)
		}); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			// 账号级失败：具体环节由原因文本给出（认证步骤、保存账号或上传 SceneMint），
			// 避免把非认证失败也标成“认证失败”。
			logging.Failure("注册", "账号", err, slog.Int("index", index), slog.Int("total", total))
			failures = append(failures, fmt.Errorf("注册 %d/%d：%w", index, total, err))
		}
	}
	logBatchResult("注册", count, len(failures))
	return errors.Join(failures...)
}

func (w *Worker) runWithAuthenticator(fn func(chatgpt.Authenticator) error) (err error) {
	authenticator, closeSession, err := w.authenticatorFactory()
	if err != nil {
		return fmt.Errorf("create authenticator: %w", err)
	}
	defer func() {
		if closeErr := closeSession(); closeErr != nil {
			logging.Warning("会话", "关闭认证会话失败", slog.Any("err", closeErr))
		}
	}()
	return fn(authenticator)
}

// registerOne 完成单个账号的认证与上传。成功与失败都只由调用方汇总成一行，
// 中间步骤不再单独打印，避免同一邮箱在连续多行里反复出现。
func (w *Worker) registerOne(ctx context.Context, authenticator chatgpt.Authenticator, index, total int) error {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	a, err := authenticator.RegisterOrLogin(ctx, nil)
	if err != nil {
		// 认证错误本身已带失败步骤，直接返回可避免日志出现「认证失败：认证失败」。
		return err
	}
	if w.storeAccounts {
		if err := appendAccount(accountsFile, a); err != nil {
			return fmt.Errorf("保存本地账号失败：%w", err)
		}
	}
	if err := w.sceneMint.Upload(ctx, a.AccessToken); err != nil {
		return fmt.Errorf("上传 SceneMint 失败：%w", err)
	}
	logging.Done("注册", "账号",
		slog.String("email", a.Email),
		slog.Int("index", index),
		slog.Int("total", total),
		slog.Duration("duration", time.Since(start)),
	)
	return nil
}

func (w *Worker) Renew(ctx context.Context) error {
	accounts, err := loadAccounts(accountsFile)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		return fmt.Errorf("no local accounts found in %s", accountsFile)
	}
	logging.Step("续期", "开始", slog.Int("count", len(accounts)))
	var failures []error
	for i := range accounts {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		account := &accounts[i]
		index, total := i+1, len(accounts)
		progress := func(extra ...any) []any {
			args := []any{slog.String("email", account.Email), slog.Int("index", index), slog.Int("total", total)}
			return append(args, extra...)
		}

		remote, queryErr := w.sceneMint.GetByEmail(ctx, account.Email)
		if queryErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			logging.Failure("续期", "查询账号", queryErr, progress()...)
			failures = append(failures, fmt.Errorf("query SceneMint account %s: %w", account.Email, queryErr))
			continue
		}
		if remote != nil {
			switch remote.Status {
			case "active":
				if remote.TokenExpiresAt.After(time.Now()) {
					logging.Skip("续期", "未过期", progress()...)
					continue
				}
			case "invalid", "disabled":
			default:
				logging.Failure("续期", "未知账号状态", nil, progress(slog.String("status", remote.Status))...)
				failures = append(failures, fmt.Errorf("unknown SceneMint account status for %s: %s", account.Email, remote.Status))
				continue
			}
		}

		start := time.Now()
		logging.Step("续期", "重新登录", progress()...)
		err = w.renewAuthentication(ctx, account)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if errors.Is(err, chatgpt.ErrAccountDeactivated) {
				if removeErr := removeAccount(accountsFile, account.Email); removeErr != nil {
					// 写盘失败只影响该账号，与同函数其他失败分支保持一致。
					logging.Failure("续期", "移除停用账号", removeErr, progress()...)
					failures = append(failures, fmt.Errorf("remove deactivated account %s: %w", account.Email, removeErr))
					continue
				}
				logging.Warning("续期", "移除停用账号", progress()...)
				continue
			}
			logging.Failure("续期", "重新登录", err, progress()...)
			failures = append(failures, fmt.Errorf("renew account %s: %w", account.Email, err))
			continue
		}
		if strings.TrimSpace(account.AccessToken) == "" {
			logging.Failure("续期", "获取访问令牌", errors.New("访问令牌为空"), progress()...)
			failures = append(failures, fmt.Errorf("续期账号 %s：访问令牌为空", account.Email))
			continue
		}
		if err = w.sceneMint.Upload(ctx, account.AccessToken); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			logging.Failure("续期", "上传 SceneMint", err, progress()...)
			failures = append(failures, fmt.Errorf("upload renewed account %s: %w", account.Email, err))
			continue
		}
		logging.Done("续期", "账号", progress(slog.Duration("duration", time.Since(start)))...)
	}
	logBatchResult("续期", len(accounts), len(failures))
	return errors.Join(failures...)
}

func (w *Worker) renewAuthentication(ctx context.Context, account *chatgpt.Account) error {
	return w.runWithAuthenticator(func(authenticator chatgpt.Authenticator) error {
		renewCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		_, err := authenticator.RegisterOrLogin(renewCtx, account)
		return err
	})
}
