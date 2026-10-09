package chatgpt

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"autobro/internal/mail"

	"github.com/go-rod/rod"
)

var _ Authenticator = (*BrowserFlow)(nil)

type BrowserFlow struct {
	flow *Flow
}

func NewBrowserFlow(m mail.IMail, bro *rod.Browser) *BrowserFlow {
	return &BrowserFlow{
		flow: New(
			WithIMail(m),
			WithBro(bro),
		),
	}
}

func (f *BrowserFlow) RegisterOrLogin(ctx context.Context, account *Account) (*Account, error) {
	if f == nil || f.flow == nil {
		return nil, errors.New("浏览器流程未初始化")
	}
	if f.flow.bro == nil {
		return nil, errors.New("浏览器未初始化")
	}
	f.flow.steps.reset()
	var result *Account
	if err := rod.Try(func() {
		result = f.flow.MustRegisterOrLogin(ctx, account)
	}); err != nil {
		tryErr := err.(*rod.TryError)
		stack := strings.NewReplacer("\r\n", "；", "\n", "；", "\r", "；").Replace(tryErr.Stack)
		logAuthTrace("浏览器", "认证错误堆栈", slog.String("stack", stack))
		return nil, f.flow.steps.wrap(tryErr.Unwrap())
	}
	return result, nil
}
