package chatgpt

import (
	"codex-free/internal/browser"
	"codex-free/internal/cpa"
	"codex-free/internal/mail"
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/sunls24/gox"
)

type Worker struct {
	m       mail.IMail
	browser *rod.Browser
	cpa     *cpa.Client
}

func New(m mail.IMail, browser *rod.Browser, cpa *cpa.Client) *Worker {
	return &Worker{m: m, browser: browser, cpa: cpa}
}

func (w *Worker) Start(count int) error {
	slog.Info("启动 chatgpt 自动化注册", slog.Int("count", count))
	for i := 0; i < count; i++ {
		slog.Info("==> 开始注册", slog.String("progress", fmt.Sprintf("%d/%d", i+1, count)))
		if err := rod.Try(w.registerOne); err != nil {
			slog.Error("==> 注册失败\n" + err.Error())
		}
	}
	return nil
}

const (
	baseURL     = "https://chatgpt.com/"
	passwordURL = "https://auth.openai.com/create-account/password"
	emailURL    = "https://auth.openai.com/email-verification"
	aboutURL    = "https://auth.openai.com/about-you"

	password2URL = "https://auth.openai.com/log-in/password"
	consentURL   = "https://auth.openai.com/sign-in-with-chatgpt/codex/consent"
	addPhoneURL  = "https://auth.openai.com/add-phone"

	maxOauthCount = 2
)

func (w *Worker) registerOne() {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute*15)
	defer cancel()

	name := mail.GenerateName()
	address, err := w.m.NewAddress(ctx, name)
	if err != nil {
		panic(err)
	}
	defer func() {
		err = w.m.DelAddress(ctx, address)
		if err != nil {
			slog.Error("DelAddress failed:", slog.String("address", address), slog.Any("err", err))
		}
	}()
	forwardMail, err := w.m.ForwardAddress(ctx)
	if err != nil {
		panic(err)
	}
	slog.Info("-> 获取邮箱地址：" + address)
	incognito := w.browser.MustIncognito()
	defer incognito.MustClose()
	slog.Info("-> 进入首页，点击注册")
	page := incognito.MustPage(baseURL)
	defer page.MustClose()
	page.MustWaitLoad()
	page.MustElement(`button[data-testid="signup-button"]`).MustClick()
	slog.Info("-> 输入邮箱，点击继续")
	page.MustElement(`#email`).MustInput(address)
	nowURL := browser.MustWaitURLChange(ctx, page, func() {
		page.MustElement(`button[type="submit"]`).MustClick()
	})

	slog.Info("-> " + nowURL)
	switch nowURL {
	case passwordURL:
		slog.Info("-> 输入密码")
		page.MustElement(`input[type="password"]`).MustInput(gox.RandStr(11) + "@")
		nowURL = browser.MustWaitURLChange(ctx, page, func() {
			page.MustElement(`button[data-dd-action-name="Continue"]`).MustWaitVisible().MustClick()

		})
		fallthrough
	case emailURL:
		slog.Info("-> 等待验证码")
		w.waitMailCode(ctx, forwardMail, func(code string) {
			page.MustElement(`input[inputmode="numeric"]`).MustInput(code)
		}, func() {
			page.MustElement(`button[name="intent"][value="resend"]`).MustClick()
		})
		nowURL = browser.MustWaitURLChange(ctx, page, func() {
			page.MustElement(`button[name="intent"][value="validate"]`).MustClick()
		})
	default:
		unexpectedURL(nowURL)
	}
	slog.Info("-> " + nowURL)
	switch nowURL {
	case aboutURL:
		page.MustReload()
		page.MustWaitLoad()
		page.MustElement(`input[name="name"]`).MustInput(name)
		page.MustElement(`#_r_3_-age`).MustFocus().MustInput(strconv.Itoa(rand.IntN(10) + 18))
		nowURL = browser.MustWaitURLChange(ctx, page, func() {
			page.MustElement(`button[type="submit"][data-dd-action-name="Continue"]`).MustClick()
		})
	case baseURL:
	default:
		unexpectedURL(nowURL)
	}
	if nowURL != baseURL {
		unexpectedURL(nowURL)
	}

	slog.Info("-> stage 1 done!!!")
	oauthCount := 0
oauth:
	oauthCount++
	oauthURL, err := w.cpa.CodexAuthURL(ctx)
	if err != nil {
		panic(err)
	}
	slog.Info(fmt.Sprintf("-> 打开 OAuthURL(%d): %s", oauthCount, oauthURL))
	page.MustNavigate(oauthURL)
	page.MustWaitLoad()
	slog.Info("-> 输入邮箱，点击继续")
	page.MustElement(`input[name="email"]`).MustInput(address)
	nowURL = browser.MustWaitURLChange(ctx, page, func() {
		page.MustElement(`button[name="intent"][value="email"]`).MustClick()
	})
	slog.Info("-> " + nowURL)
	switch nowURL {
	case password2URL:
		nowURL = browser.MustWaitURLChange(ctx, page, func() {
			slog.Info("-> 使用一次性验证码登录")
			page.MustElement(`button[name="intent"][value="passwordless_login_send_otp"]`).MustClick()
		})
	default:
		unexpectedURL(nowURL)
	}
	slog.Info("-> " + nowURL)
	switch nowURL {
	case emailURL:
		slog.Info("-> 等待验证码")
		w.waitMailCode(ctx, forwardMail, func(code string) {
			page.MustElement(`input[inputmode="numeric"]`).MustInput(code)
		}, func() {
			page.MustElement(`button[name="intent"][value="resend"]`).MustClick()
		})
		nowURL = browser.MustWaitURLChange(ctx, page, func() {
			page.MustElement(`button[name="intent"][value="validate"]`).MustWaitVisible().MustClick()
		})
	default:
		unexpectedURL(nowURL)
	}
	slog.Info("-> " + nowURL)
	switch nowURL {
	case consentURL:
		nowURL = browser.MustWaitURLChange(ctx, page, func() {
			slog.Info("-> 点击继续")
			page.MustElement(`button[type="submit"][data-dd-action-name="Continue"]`).MustClick()
		})
	case addPhoneURL:
		if oauthCount < maxOauthCount {
			goto oauth
		}

		panic("oauth add phone: " + address)
	default:
		unexpectedURL(nowURL)
	}
	slog.Info("-> " + nowURL)
	if strings.Contains(nowURL, "localhost") {
		err = w.cpa.CodexCallback(ctx, nowURL)
		if err != nil {
			panic(err)
		}
	} else {
		unexpectedURL(nowURL)
	}

	slog.Info("==> 注册成功", slog.String("用时", time.Since(start).String()), slog.String("address", address))
}

func unexpectedURL(url string) {
	panic("意外的URL: " + url)
}

func (w *Worker) waitMailCode(ctx context.Context, forwardMail string, input func(code string), resend func()) {
	ch := w.m.WaitMailCode(ctx, forwardMail)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case code := <-ch:
			if code.Err != nil {
				panic("WaitMailCode: " + code.Err.Error())
			}
			slog.Info("-> " + code.Value)
			input(code.Value)
			return
		case <-ticker.C:
			if resend != nil {
				slog.Info("-> 重新发送电子邮件")
				resend()
			}
		}
	}
}
