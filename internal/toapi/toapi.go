package toapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"
	"time"

	"autobro/internal/chatgpt"
	"autobro/internal/logging"
	"autobro/internal/mail"
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

func logBatchResult(action string, total, failed int, extra ...any) {
	args := append([]any{slog.Int("count", total)}, extra...)
	if failed == 0 {
		logging.Done(action, "批次", args...)
		return
	}
	args = append(args, slog.Int("failed", failed))
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

			var pendingSaveErr error
			if state.account.AuthStage == chatgpt.AuthStageTokenReady && state.account.AccessToken != "" {
				logging.Step("注册", "处理账号", slog.Int("index", index), slog.Int("total", total), slog.Int("attempt", attempt))
				err = w.finishRegistration(ctx, state, index, total)
			} else {
				logging.Step("注册", "开始认证", slog.Int("index", index), slog.Int("total", total), slog.Int("attempt", attempt))
				err = w.authenticateOne(ctx, state.account)
				if err == nil {
					err = w.finishRegistration(ctx, state, index, total)
				} else if state.account.AuthStage == chatgpt.AuthStageCreateUnconfirmed {
					err = fmt.Errorf("创建结果未确认，已保留邮箱别名：%w", err)
					if w.storeAccounts {
						if pendingSaveErr = appendAccount(pendingAccountsFile, state.account); pendingSaveErr != nil {
							err = errors.Join(err, fmt.Errorf("保存待核对账号失败：%w", pendingSaveErr))
						}
					}
				} else if state.account.AuthStage >= chatgpt.AuthStageAccountCreated && !errors.Is(err, chatgpt.ErrAccountDeactivated) {
					// 认证尚未完成也保存已创建的账号，最终失败或取消后可通过续期恢复。
					err = errors.Join(err, w.storeRegistration(state))
				}
			}
			if err == nil {
				break
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				if pendingSaveErr != nil {
					logging.Failure("注册", "保存待核对账号", pendingSaveErr, slog.String("file", pendingAccountsFile))
				}
				return errors.Join(ctxErr, err)
			}
			if errors.Is(err, mail.ErrUumailUnavailable) {
				logging.Skip("注册", "UU", slog.Any("err", err))
				return errors.Join(failures...)
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
		return fmt.Errorf("创建认证会话：%w", err)
	}
	defer func() {
		if closeErr := closeSession(); closeErr != nil {
			logging.Warning("会话", "关闭认证会话失败", slog.Any("err", closeErr))
		}
	}()
	return fn(authenticator)
}

func (w *Worker) authenticateOne(ctx context.Context, account *chatgpt.Account) error {
	return w.runWithAuthenticator(func(authenticator chatgpt.Authenticator) error {
		authCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()

		result, err := authenticator.RegisterOrLogin(authCtx, account)
		if err != nil {
			return err
		}
		if result == nil {
			return errors.New("认证未返回账号")
		}
		if result != account {
			*account = *result
		}
		if strings.TrimSpace(account.AccessToken) == "" {
			return errors.New("认证未返回访问令牌")
		}
		account.AuthStage = chatgpt.AuthStageTokenReady
		return nil
	})
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
	if account != nil && account.AuthStage == chatgpt.AuthStageCreateUnconfirmed {
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

// Renew 尝试重新登录需要续期的账号。limit 为 0 时不限次数。
func (w *Worker) Renew(ctx context.Context, limit int) error {
	accounts, err := loadAccounts(accountsFile)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		return fmt.Errorf("本地账号文件 %s 中没有账号", accountsFile)
	}
	logging.Step("续期", "开始", slog.Int("count", len(accounts)))
	var scanned, attempted int
	if limit > 0 {
		if startIndex := relogStartIndex(accounts); startIndex > 0 {
			accounts = slices.Concat(accounts[startIndex:], accounts[:startIndex])
		}
		defer func() {
			if scanned < len(accounts) {
				saveRelogCursor(accounts[scanned].Email)
			}
		}()
	}
	var failures []error
scan:
	for batch := range slices.Chunk(accounts, scenemint.MaxRelogCheckEmails) {
		emails := make([]string, len(batch))
		for index, account := range batch {
			emails[index] = strings.ToLower(strings.TrimSpace(account.Email))
		}
		batchLimit := len(batch)
		if limit > 0 {
			batchLimit = min(batchLimit, limit-attempted)
		}
		check, err := w.sceneMint.CheckRelogAccounts(ctx, emails, batchLimit)
		if err != nil {
			logging.Failure("续期", "批量查询账号", err, slog.Int("index", scanned+1), slog.Int("count", len(batch)))
			failures = append(failures, fmt.Errorf("批量查询 SceneMint 账号：%w", err))
			break
		}
		invalidEmailIndexes := check.InvalidEmailIndexes
		candidates := check.Items
		for index, account := range batch {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(failures, err)...)
			}
			if len(invalidEmailIndexes) > 0 && invalidEmailIndexes[0] == index {
				invalidEmailIndexes = invalidEmailIndexes[1:]
				err := fmt.Errorf("第 %d 个账号的邮箱格式无效", scanned+1)
				logging.Failure("续期", "查询账号", err, slog.String("email", account.Email))
				failures = append(failures, err)
			} else if len(candidates) > 0 && candidates[0].Email == emails[index] {
				candidate := candidates[0]
				candidates = candidates[1:]
				attempted++
				if err := w.renewAccount(ctx, &account, scanned+1, len(accounts), candidate.Reason); err != nil {
					if ctx.Err() != nil {
						return errors.Join(append(failures, err)...)
					}
					failures = append(failures, err)
				}
			}
			scanned++
			if limit > 0 && attempted == limit {
				break scan
			}
		}
	}
	logBatchResult("续期", scanned, len(failures), slog.Int("登录尝试", attempted))
	return errors.Join(failures...)
}

func (w *Worker) renewAccount(ctx context.Context, account *chatgpt.Account, index, total int, reason string) error {
	progress := []any{slog.String("email", account.Email), slog.Int("index", index), slog.Int("total", total)}
	start := time.Now()
	logging.Step("续期", "重新登录", append(progress, slog.String("reason", reason))...)
	if err := w.authenticateOne(ctx, account); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		removeReason := ""
		switch {
		case errors.Is(err, chatgpt.ErrAccountDeactivated):
			removeReason = "停用账号"
		case errors.Is(err, chatgpt.ErrMailCodeTimeout):
			removeReason = "验证码超时账号"
		}
		if removeReason != "" {
			if removeErr := removeAccount(accountsFile, account.Email); removeErr != nil {
				logging.Failure("续期", "移除"+removeReason, removeErr, progress...)
				return fmt.Errorf("移除%s %s：%w", removeReason, account.Email, removeErr)
			}
			logging.Skip("续期", removeReason+"已移除", progress...)
			return nil
		}
		logging.Failure("续期", "重新登录", err, progress...)
		return fmt.Errorf("续期账号 %s：%w", account.Email, err)
	}
	if err := w.sceneMint.Upload(ctx, account.AccessToken); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		logging.Failure("续期", "上传 SceneMint", err, progress...)
		return fmt.Errorf("上传续期账号 %s：%w", account.Email, err)
	}
	logging.Done("续期", "账号", append(progress, slog.Duration("duration", time.Since(start)))...)
	return nil
}
