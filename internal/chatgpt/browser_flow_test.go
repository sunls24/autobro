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

func TestEmailVerificationURLWithQuery(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		url  string
		want bool
	}{
		{emailURL, true},
		{emailURL + "?auth_page=test", true},
		{"https://example.com/email-verification?auth_page=test", false},
		{emailURL + "/other?auth_page=test", false},
	} {
		t.Run(tt.url, func(t *testing.T) {
			if got := withoutURLQuery(tt.url) == emailURL; got != tt.want {
				t.Fatalf("email verification page match = %v, want %v", got, tt.want)
			}
		})
	}
}
