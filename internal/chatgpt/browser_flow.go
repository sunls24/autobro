package chatgpt

import (
	"context"
	"errors"
	"fmt"

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
		return nil, errors.New("browser flow is not initialized")
	}
	if f.flow.bro == nil {
		return nil, errors.New("browser is not initialized")
	}
	f.flow.lastStep = ""
	var result *Account
	if err := rod.Try(func() {
		result = f.flow.MustRegisterOrLogin(ctx, account)
	}); err != nil {
		if f.flow.lastStep != "" {
			return nil, fmt.Errorf("浏览器认证步骤“%s”失败：%w", f.flow.lastStep, err)
		}
		return nil, err
	}
	return result, nil
}
