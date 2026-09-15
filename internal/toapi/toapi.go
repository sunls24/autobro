package toapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
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

const maxRegistrationAttempts = 3

var registrationRetryWait = waitRegistrationRetry

type registrationState struct {
	account *chatgpt.Account
	stored  bool
	started time.Time
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
		state := &registrationState{
			account: &chatgpt.Account{},
			started: time.Now(),
		}
		var err error
		for attempt := 1; attempt <= maxRegistrationAttempts; attempt++ {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return errors.Join(ctxErr, err)
			}

			if state.account.AuthStage == chatgpt.AuthStageTokenReady && state.account.AccessToken != "" {
				logging.Step("注册", "处理账号", slog.Int("index", index), slog.Int("total", total), slog.Int("attempt", attempt))
				err = w.finishRegistration(ctx, state, index, total)
			} else {
				logging.Step("注册", "开始认证", slog.Int("index", index), slog.Int("total", total), slog.Int("attempt", attempt))
				err = w.runWithAuthenticator(func(authenticator chatgpt.Authenticator) error {
					return w.authenticateOne(ctx, authenticator, state.account)
				})
				if err == nil {
					err = w.finishRegistration(ctx, state, index, total)
				} else if state.account.AuthStage >= chatgpt.AuthStageAccountCreated && !errors.Is(err, chatgpt.ErrAccountDeactivated) {
					// 认证尚未完成也保存已创建的账号，最终失败或取消后可通过续期恢复。
					err = errors.Join(err, w.storeRegistration(state))
				}
			}
			if err == nil {
				break
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return errors.Join(ctxErr, err)
			}
			if attempt == maxRegistrationAttempts || !retryableRegistrationError(state.account, err) {
				// 账号级失败：具体环节由原因文本给出（认证步骤、保存账号或上传 SceneMint），
				// 避免把非认证失败也标成“认证失败”。
				logging.Failure("注册", "账号", err, slog.Int("index", index), slog.Int("total", total))
				failures = append(failures, fmt.Errorf("注册 %d/%d：%w", index, total, err))
				break
			}
			if state.account.AuthStage < chatgpt.AuthStageAccountCreated {
				// 账号尚未创建，当前邮箱清理已由认证流程负责；下一次按新账号流程开始。
				*state.account = chatgpt.Account{}
			}
			logging.Warning("注册", "账号重试",
				slog.Int("index", index),
				slog.Int("total", total),
				slog.Int("attempt", attempt+1),
				slog.Any("err", err),
			)
			if waitErr := registrationRetryWait(ctx, attempt); waitErr != nil {
				return errors.Join(waitErr, err)
			}
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

func (w *Worker) authenticateOne(ctx context.Context, authenticator chatgpt.Authenticator, account *chatgpt.Account) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	a, err := authenticator.RegisterOrLogin(ctx, account)
	if err != nil {
		return err
	}
	if a == nil {
		return errors.New("认证未返回账号")
	}
	if a != account {
		*account = *a
	}
	if strings.TrimSpace(account.AccessToken) == "" {
		return errors.New("认证未返回访问令牌")
	}
	account.AuthStage = chatgpt.AuthStageTokenReady
	return nil
}

func (w *Worker) storeRegistration(state *registrationState) error {
	if w.storeAccounts && !state.stored {
		if err := appendAccount(accountsFile, state.account); err != nil {
			return fmt.Errorf("保存本地账号失败：%w", err)
		}
		state.stored = true
	}
	return nil
}

func (w *Worker) finishRegistration(ctx context.Context, state *registrationState, index, total int) error {
	finishCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	if err := w.storeRegistration(state); err != nil {
		return err
	}
	if err := w.sceneMint.Upload(finishCtx, state.account.AccessToken); err != nil {
		return fmt.Errorf("上传 SceneMint 失败：%w", err)
	}
	logging.Done("注册", "账号",
		slog.String("email", state.account.Email),
		slog.Int("index", index),
		slog.Int("total", total),
		slog.Duration("duration", time.Since(state.started)),
	)
	return nil
}

func retryableRegistrationError(account *chatgpt.Account, err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, chatgpt.ErrAccountDeactivated) || errors.Is(err, chatgpt.ErrMailCodeTimeout) {
		return false
	}
	if account != nil && account.AuthStage >= chatgpt.AuthStageAccountCreated {
		return true
	}
	return isTimeoutError(err)
}

func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func waitRegistrationRetry(ctx context.Context, attempt int) error {
	delay := 10 * time.Second * time.Duration(1<<(attempt-1))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
