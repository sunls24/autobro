package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"autobro/internal/browser"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
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

func TestAuthPageURL(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		url  string
		want string
	}{
		{emailURL + "?auth_page=test#fragment", emailURL},
		{passkeyURL + "#test", passkeyURL},
		{"https://example.com/email-verification?auth_page=test", "https://example.com/email-verification"},
		{emailURL + "/other?auth_page=test", emailURL + "/other"},
	} {
		t.Run(tt.url, func(t *testing.T) {
			if got := authPageURL(tt.url); got != tt.want {
				t.Fatalf("authPageURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoginRedirectHonorsParentDeadline(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		page := newRedirectTestPage(t, "")
		ctx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
		defer cancel()
		got, err := browser.WaitURLChange(ctx, page, loginWithURL, authPageURL)
		if !errors.Is(err, context.DeadlineExceeded) || got != "" {
			t.Fatalf("login redirect = %q, %v, want empty URL and parent deadline", got, err)
		}
	})
}

func TestSlowNavigationDoesNotResubmit(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const unchangedPolls = 44
		urls := []string{loginWithURL + "?screen_hint=before", ""}
		for range unchangedPolls - 1 {
			urls = append(urls, loginWithURL+"?screen_hint=after#test")
		}
		page := newRedirectTestPage(t, append(urls, emailURL+"?auth_page=next")...)
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		submissions, checks := 0, 0
		from, to, err := browser.SubmitAndWaitForURLChange(ctx, page, authPageURL, func() error {
			submissions++
			return nil
		}, func(*rod.Page) error {
			checks++
			return nil
		})
		if err != nil || from != loginWithURL || to != emailURL {
			t.Fatalf("navigation = %q, %q, %v, want %q, %q, nil", from, to, err, loginWithURL, emailURL)
		}
		if submissions != 1 || checks != unchangedPolls {
			t.Fatalf("got %d submissions and %d checks, want 1 submission and %d checks", submissions, checks, unchangedPolls)
		}
	})
}

func TestSubmitAndWaitForURLChangePreservesActionError(t *testing.T) {
	t.Parallel()
	page := newRedirectTestPage(t, emailURL+"?auth_page=before")
	cause := errors.New("submit failed")
	from, last, err := browser.SubmitAndWaitForURLChange(t.Context(), page, authPageURL, func() error {
		return cause
	})
	if !errors.Is(err, cause) || from != emailURL || last != emailURL {
		t.Fatalf("navigation = %q, %q, %v, want %q, %q, %v", from, last, err, emailURL, emailURL, cause)
	}
}

func newRedirectTestPage(t *testing.T, urls ...string) *rod.Page {
	t.Helper()
	client := &redirectClient{urls: urls, events: make(chan *cdp.Event)}
	t.Cleanup(func() { close(client.events) })
	bro := rod.New().Context(t.Context()).ControlURL("").NoDefaultDevice().Client(client)
	if err := bro.Connect(); err != nil {
		t.Fatal(err)
	}
	page, err := bro.PageFromTarget("page")
	if err != nil {
		t.Fatal(err)
	}

	return page
}

type redirectClient struct {
	urls   []string
	events chan *cdp.Event
}

type deadlineClient struct {
	redirectClient
	cancel     context.CancelFunc
	closeTried bool
}

func (c *deadlineClient) Call(ctx context.Context, session, method string, params any) ([]byte, error) {
	switch method {
	case "Target.createTarget":
		return []byte(`{"targetId":"page"}`), nil
	case "Page.navigate":
		c.cancel()
		return nil, ctx.Err()
	case "Page.close":
		_, bounded := ctx.Deadline()
		c.closeTried = bounded && ctx.Err() == nil
		return nil, errors.New("fixture close failure")
	default:
		return c.redirectClient.Call(ctx, session, method, params)
	}
}

func TestBrowserPageUsesAuthContextAndPreservesNavigationError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := &deadlineClient{redirectClient: redirectClient{events: make(chan *cdp.Event)}, cancel: cancel}
	t.Cleanup(func() { close(client.events) })
	bro := rod.New().Context(t.Context()).ControlURL("").NoDefaultDevice().Client(client)
	if err := bro.Connect(); err != nil {
		t.Fatal(err)
	}
	account := &Account{Email: "test@example.com", ForwardMail: "forward@example.com"}
	_, err := NewBrowserFlow(nil, bro).RegisterOrLogin(ctx, account)
	if !errors.Is(err, context.Canceled) || !client.closeTried {
		t.Fatalf("authentication error = %v, cleanup used independent deadline = %v", err, client.closeTried)
	}
}

func (c *redirectClient) Event() <-chan *cdp.Event { return c.events }

func (c *redirectClient) Call(_ context.Context, _, method string, _ any) ([]byte, error) {
	switch method {
	case "Target.attachToTarget":
		return []byte(`{"sessionId":"session"}`), nil
	case "Target.getTargetInfo":
		if len(c.urls) == 0 {
			return nil, errors.New("unexpected extra URL read")
		}
		info := proto.TargetGetTargetInfoResult{TargetInfo: &proto.TargetTargetInfo{URL: c.urls[0]}}
		c.urls = c.urls[1:]
		return json.Marshal(info)
	default:
		return []byte(`{}`), nil
	}
}
