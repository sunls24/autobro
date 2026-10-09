package chatgpt

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/go-rod/rod"
)

type browserFailureMail struct {
	waitingMail
	err error
}

func (m browserFailureMail) NewAddress(context.Context, string) (string, error) {
	return "", m.err
}

func TestBrowserFlowReportsOriginalError(t *testing.T) {
	t.Parallel()
	cause := &url.Error{Op: "Get", URL: "https://api.uu.me/v1/user/info", Err: io.ErrUnexpectedEOF}
	flow := NewBrowserFlow(browserFailureMail{err: cause}, rod.New())
	_, err := flow.RegisterOrLogin(context.Background(), nil)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "创建邮箱地址") || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("RegisterOrLogin() lost original error or step: %v", err)
	}
	if strings.Contains(err.Error(), "goroutine") || strings.Contains(err.Error(), "error value:") {
		t.Fatalf("RegisterOrLogin() includes panic diagnostics: %v", err)
	}
}
