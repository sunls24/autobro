package chatgpt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"autobro/internal/logging"
	"autobro/internal/mail"
)

// mailCodeWait 描述一次验证码等待的超时策略；timeoutSet 为 false 时不启用总超时。
type mailCodeWait struct {
	interval   time.Duration
	timeout    time.Duration
	timeoutSet bool
}

// waitForMailCode 是浏览器与协议两条流程共用的验证码等待循环：按 interval 周期重发
// （最多 maxMailCodeResends 次）、可选总超时，并响应 ctx 取消。resend 为 nil 时只等待。
func waitForMailCode(ctx context.Context, m mail.IMail, forwardMail string, wait mailCodeWait, resend func(context.Context) error) (string, error) {
	if wait.interval <= 0 {
		wait.interval = defaultMailCodeInterval
	}
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := m.WaitMailCode(waitCtx, forwardMail)

	timer := time.NewTimer(wait.interval)
	defer timer.Stop()
	var timeoutTimer *time.Timer
	var timeoutCh <-chan time.Time
	if wait.timeoutSet {
		timeoutTimer = time.NewTimer(wait.timeout)
		timeoutCh = timeoutTimer.C
		defer timeoutTimer.Stop()
	}

	resendCount := 0
	var lastResendErr error
	timeoutError := func() error {
		if lastResendErr != nil {
			return fmt.Errorf("%w（最近一次重发错误：%v）", ErrMailCodeTimeout, lastResendErr)
		}
		return ErrMailCodeTimeout
	}
	for {
		select {
		case code, ok := <-ch:
			if !ok {
				return "", errors.New("等待邮箱验证码：通道已关闭")
			}
			if code.Err != nil {
				return "", fmt.Errorf("等待邮箱验证码：%w", code.Err)
			}
			value := strings.TrimSpace(code.Value)
			if value == "" {
				return "", errors.New("等待邮箱验证码：验证码为空")
			}
			return value, nil
		case <-timer.C:
			if resendCount >= maxMailCodeResends {
				return "", timeoutError()
			}
			if resend != nil {
				logging.Sub("重新发送邮箱验证码", slog.String("address", forwardMail))
				if err := resend(ctx); err != nil {
					lastResendErr = err
					logging.SubWarning("重新发送邮箱验证码", slog.String("address", forwardMail), slog.Any("err", err))
				} else {
					lastResendErr = nil
				}
			}
			resendCount++
			timer.Reset(wait.interval)
		case <-timeoutCh:
			return "", timeoutError()
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}
