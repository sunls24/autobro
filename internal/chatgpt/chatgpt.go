package chatgpt

import (
	"codex-free/internal/browser"
	"codex-free/internal/cpa"
	"codex-free/internal/mail"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sunls24/gox"
	"github.com/tidwall/gjson"
)

type Flow struct {
	m   mail.IMail
	bro *rod.Browser
	cpa *cpa.Client

	background bool
}

type Options func(*Flow)

func WithIMail(m mail.IMail) Options {
	return func(w *Flow) {
		w.m = m
	}
}

func WithBro(bro *rod.Browser) Options {
	return func(w *Flow) {
		w.bro = bro
	}
}

func WithCPA(cpa *cpa.Client) Options {
	return func(w *Flow) {
		w.cpa = cpa
	}
}

func WithBackground() Options {
	return func(w *Flow) {
		w.background = true
	}
}

func New(options ...Options) *Flow {
	opts := &Flow{}
	for _, option := range options {
		option(opts)
	}
	return opts
}

type Account struct {
	Email       string `json:"email"`
	Password    string `json:"password,omitempty"`
	ForwardMail string `json:"forward_mail"`
	AccessToken string `json:"-"`
}

func clickLogin(page *rod.Page) {
	selector := `
		button[data-testid="login-button"],
		button[data-mobile-auth-entry-action="login"]
	`
	for _, button := range page.MustElements(selector) {
		if button.MustVisible() && !button.MustDisabled() {
			button.
				MustScrollIntoView().
				MustClick()
			return
		}
	}
	panic("没有找到可见的登录按钮")
}

func (f *Flow) MustRegisterOrLogin(ctx context.Context, a *Account) *Account {
	var name = "NOT SPECIFIED"
	if a == nil || a.Email == "" {
		a = &Account{}
		name = mail.GenerateName()
		address, err := f.m.NewAddress(ctx, name)
		if err != nil {
			panic(err)
		}
		a.Email = address
	}

	var err error
	if strings.TrimSpace(a.ForwardMail) == "" {
		var forwardMail string
		forwardMail, err = f.m.ForwardAddress(ctx, a.Email)
		if err != nil {
			panic(err)
		}
		a.ForwardMail = forwardMail
	}
	forwardMail := a.ForwardMail

	slog.Info("-> 邮箱地址：" + a.Email)
	var page *rod.Page
	if f.background {
		page = browser.MustBackgroundPage(f.bro)
	} else {
		page = f.bro.MustPage()
	}
	if page == nil {
		panic("page is nil")
	}
	defer page.MustClose()

	slog.Info("-> 清理登录状态")
	if err = clearSession(page); err != nil {
		panic(err)
	}

	slog.Info("-> 进入首页")
	page.MustNavigate(baseURL)
	page.MustWaitLoad()

	switch page.MustInfo().URL {
	case baseURL:
		emailFound := false
		_, _ = page.Timeout(timeout).Race().
			ElementR("div.text-xl.font-medium", "全新 ChatGPT Images").
			MustHandle(func(_ *rod.Element) {
				_ = page.Keyboard.Press(input.Escape)
			}).
			Element("#email").
			MustHandle(func(_ *rod.Element) {
				emailFound = true
			}).
			Do()

		if !emailFound {
			slog.Info("-> 点击登录")
			clickLogin(page)
		}
	}
	slog.Info("-> 输入邮箱，点击继续")
	page.Timeout(timeout * 2).
		MustElement("#email, #mobile-auth-email").
		MustWaitVisible().
		MustInput(a.Email)
	email := page.Timeout(timeout * 2).
		MustElement("#email, #mobile-auth-email").
		MustWaitVisible().
		MustSelectAllText().
		MustInput(strings.TrimSpace(a.Email))
	nowURL := browser.MustWaitURLChange(ctx, page, func() {
		email.MustType(input.Enter)
	})

inputEmail:
	slog.Info("-> " + nowURL)
	switch nowURL {
	case passwordURL:
		slog.Info("-> 输入密码")
		if a.Password == "" {
			a.Password = gox.RandStr(11) + "@"
		}
		page.MustElement(`input[type="password"]`).MustInput(a.Password)
		nowURL = browser.MustWaitURLChange(ctx, page, func() {
			page.Timeout(timeout).MustElement(`button[data-dd-action-name="Continue"]`).MustClick()
		})
		if nowURL != emailURL {
			break
		}
		fallthrough
	case emailURL:
		slog.Info("-> 等待验证码")
		if err = f.waitMailCode(ctx, forwardMail, func(code string) {
			page.MustElement(`input[inputmode="numeric"]`).MustInput(code)
		}, func() {
			page.MustElement(`button[name="intent"][value="resend"]`).MustClick()
		}); err != nil {
			panic(err)
		}
		nowURL = browser.MustWaitURLChange(ctx, page, func() {
			page.Timeout(timeout).MustElement(`button[name="intent"][value="validate"]`).MustClick()
		}, checkAccountDeactivated)
	default:
		if strings.Contains(nowURL, "/auth/login_with") {
			time.Sleep(time.Second * 1)
			nowURL = page.MustInfo().URL
			goto inputEmail
		}

		unexpectedURL(nowURL)
	}

	slog.Info("-> " + nowURL)
	switch nowURL {
	case aboutURL:
		page.MustWaitLoad()
		page.MustElement(`input[name="name"]`).MustInput(name)
		page.Keyboard.MustType(input.Tab)
		age := rand.IntN(10) + 18
		ageInput, err := page.Timeout(timeout).Element(`input[name="age"][type="number"]`)
		if err == nil {
			ageInput.MustInput(strconv.Itoa(age))
		} else {
			y := time.Now().Year() - age
			m := rand.IntN(12) + 1
			d := rand.IntN(28) + 1
			want := fmt.Sprintf("%04d-%02d-%02d", y, m, d)
			slog.Info("-> birthday " + want)

			fillDateSegment(page, "year", fmt.Sprintf("%04d", y))
			fillDateSegment(page, "month", fmt.Sprintf("%02d", m))
			fillDateSegment(page, "day", fmt.Sprintf("%02d", d))

			page.Timeout(timeout).MustWait(`(want) => document.querySelector('input[name="birthday"]')?.value === want`, want)
		}
		nowURL = browser.MustWaitURLChange(ctx, page, func() {
			page.Timeout(timeout).MustElement(`button[type="submit"][data-dd-action-name="Continue"]`).MustClick()
		})
	case baseURL:
	default:
		unexpectedURL(nowURL)
	}
	switch nowURL {
	case passkeyURL:
		nowURL = browser.MustWaitURLChange(ctx, page, func() {
			page.Timeout(timeout).MustElement(`[data-dd-action-name="skip create account enroll passkey"]`).MustClick()
		})
	}
	if nowURL != baseURL {
		unexpectedURL(nowURL)
	}

	slog.Info("-> 获取 access token")
	a.AccessToken = gjson.Get(page.MustEval(`async () => {
		const res = await fetch("https://chatgpt.com/api/auth/session/")
		return await res.text()
	}`).String(), "accessToken").String()
	if strings.TrimSpace(a.AccessToken) == "" {
		panic("access token is empty")
	}
	return a
}

var ErrAccountDeactivated = errors.New("account deactivated")

func checkAccountDeactivated(page *rod.Page) error {
	body, err := page.Element("body")
	if err != nil {
		return nil
	}
	text, err := body.Text()
	if err == nil && strings.Contains(text, "account_deactivated") {
		return ErrAccountDeactivated
	}
	return nil
}

const (
	timeout = time.Second * 2

	baseURL     = "https://chatgpt.com/"
	passwordURL = "https://auth.openai.com/create-account/password"
	emailURL    = "https://auth.openai.com/email-verification"
	aboutURL    = "https://auth.openai.com/about-you"
	passkeyURL  = "https://auth.openai.com/create-account-enroll-passkey"
)

func (f *Flow) waitMailCode(ctx context.Context, forwardMail string, input func(code string), resend func()) error {
	ch := f.m.WaitMailCode(ctx, forwardMail)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case code := <-ch:
			if code.Err != nil {
				return fmt.Errorf("waitMailCode: %w", code.Err)
			}
			slog.Info("-> " + code.Value)
			input(code.Value)
			return nil
		case <-ticker.C:
			if resend != nil {
				slog.Info("-> 重新发送电子邮件")
				resend()
			}
		}
	}
}

func fillDateSegment(page *rod.Page, typ, val string) {
	seg := page.MustElement(fmt.Sprintf(`.react-aria-DateField [data-type="%s"]`, typ))
	seg.MustClick()
	seg.MustEval(`function() {
		this.focus();
		const range = document.createRange();
		range.selectNodeContents(this);
		const selection = window.getSelection();
		selection.removeAllRanges();
		selection.addRange(range);
	}`)

	for _, r := range val {
		page.Keyboard.MustType(input.Key(r))
	}
}

var sessionOrigins = []string{
	"https://chatgpt.com",
	"https://auth.openai.com",
}

func clearSession(page *rod.Page) (err error) {
	if err = page.Browser().SetCookies(nil); err != nil {
		return fmt.Errorf("clear cookies: %w", err)
	}

	for _, origin := range sessionOrigins {
		err = proto.StorageClearDataForOrigin{
			Origin:       origin,
			StorageTypes: string(proto.StorageStorageTypeAll),
		}.Call(page)
		if err != nil {
			return fmt.Errorf("clear storage for %s: %w", origin, err)
		}
	}

	return nil
}

func unexpectedURL(url string) {
	panic("意外的URL: " + url)
}
