package chatgpt

import (
	"autobro/internal/browser"
	"autobro/internal/logging"
	"autobro/internal/mail"
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

	lastStep string

	mailCodeInterval   time.Duration
	mailCodeTimeout    time.Duration
	mailCodeTimeoutSet bool
}

// markStep 记录当前步骤用于失败归因，并在详细级别下输出诊断行。args 只随诊断行
// 输出、不参与 lastStep，因此这些步骤被省略后仍保留失败归因所需的上下文。
func (f *Flow) markStep(step string, args ...any) {
	f.lastStep = step
	logAuthTrace("浏览器", step, args...)
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

// WithMailCodeTimeout sets the optional overall timeout for waiting for a mail code.
func WithMailCodeTimeout(timeout time.Duration) Options {
	return func(w *Flow) {
		w.mailCodeTimeout = timeout
		w.mailCodeTimeoutSet = true
	}
}

func WithMailCodeInterval(interval time.Duration) Options {
	return func(w *Flow) {
		w.mailCodeInterval = interval
	}
}

func New(options ...Options) *Flow {
	opts := &Flow{mailCodeInterval: defaultMailCodeInterval}
	for _, option := range options {
		option(opts)
	}
	return opts
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
	if a == nil {
		a = &Account{}
	}
	// name 用于注册新账号：既作为邮箱地址的本地部分，也填入资料页。
	name := mail.GenerateName()
	// addressAcquired 为 true 时，本次流程负责的别名在账号创建前失败时需要释放。
	// 账号创建成功后别名已经提交，不再自动删除。
	addressAcquired := false
	var addressMetadata mail.AddressMetadata
	defer func() {
		if !addressAcquired || a.AuthStage >= AuthStageAccountCreated {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if cleanupErr := f.m.DelAddressByMetadata(cleanupCtx, addressMetadata); cleanupErr != nil {
			logAuthFailure("浏览器", "清理邮箱地址", cleanupErr, slog.String("email", addressMetadata.Email))
		}
	}()
	if strings.TrimSpace(a.Email) == "" {
		f.markStep("创建邮箱地址")
		address, err := f.m.NewAddress(ctx, name)
		if err != nil {
			panic(err)
		}
		a.Email = address
		addressMetadata = f.m.Metadata(address)
		addressMetadata.Email = address
		addressAcquired = true
		a.MailProvider = addressMetadata.Provider
		a.ProviderAddressID = addressMetadata.AddressID
		a.ProviderOwnerID = addressMetadata.OwnerID
	}

	var err error
	if strings.TrimSpace(a.ForwardMail) == "" {
		f.markStep("获取转发地址")
		var forwardMail string
		forwardMail, err = f.m.ForwardAddress(ctx, a.Email)
		if err != nil {
			panic(err)
		}
		a.ForwardMail = forwardMail
	}
	forwardMail := a.ForwardMail

	logging.Sub("账号信息", slog.String("email", a.Email), slog.String("address", forwardMail))
	f.markStep("打开浏览器页面")
	page := browser.MustBackgroundPage(f.bro)
	if page == nil {
		panic("page is nil")
	}
	defer page.MustClose()

	f.markStep("清理登录状态")
	if err = clearSession(page); err != nil {
		panic(err)
	}

	f.markStep("进入首页")
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
			f.markStep("点击登录")
			clickLogin(page)
		}
	}
	f.markStep("输入邮箱并继续")
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
	f.markStep("页面跳转", slog.String("url", nowURL))
	switch nowURL {
	case passwordURL:
		f.markStep("输入密码")
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
		f.markStep("等待邮箱验证码")
		if err = f.waitMailCode(ctx, forwardMail, func(code string) {
			page.MustElement(`input[inputmode="numeric"]`).MustInput(code)
		}, func() {
			page.MustElement(`button[name="intent"][value="resend"]`).MustClick()
		}); err != nil {
			panic(err)
		}
		f.markStep("校验邮箱验证码")
		nowURL = browser.MustWaitURLChange(ctx, page, func() {
			page.Timeout(timeout).MustElement(`button[name="intent"][value="validate"]`).MustClick()
		}, func(page *rod.Page) error {
			err := checkAccountDeactivated(page)
			if errors.Is(err, ErrAccountDeactivated) {
				// 保留已使用标记，避免停用账号的别名被释放后再次分配。
				f.m.ForgetAddress(a.Email)
				addressAcquired = false
			}
			return err
		})
		logging.SubDone("邮箱验证码已校验")
	default:
		if strings.Contains(nowURL, "/auth/login_with") {
			time.Sleep(time.Second * 1)
			nowURL = page.MustInfo().URL
			goto inputEmail
		}

		unexpectedURL(nowURL)
	}

	f.markStep("页面跳转", slog.String("url", nowURL))
	switch nowURL {
	case aboutURL:
		page.MustWaitLoad()
		f.markStep("填写账号资料")
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
			f.markStep("填写生日", slog.String("birthday", want))

			fillDateSegment(page, "year", fmt.Sprintf("%04d", y))
			fillDateSegment(page, "month", fmt.Sprintf("%02d", m))
			fillDateSegment(page, "day", fmt.Sprintf("%02d", d))

			page.Timeout(timeout).MustWait(`(want) => document.querySelector('input[name="birthday"]')?.value === want`, want)
		}
		f.markStep("创建账号")
		nowURL = browser.MustWaitURLChange(ctx, page, func() {
			page.Timeout(timeout).MustElement(`button[type="submit"][data-dd-action-name="Continue"]`).MustClick()
		})
	case baseURL:
	default:
		unexpectedURL(nowURL)
	}
	switch nowURL {
	case passkeyURL:
		// 到达 Passkey 页面说明账号创建请求已经完成；后续跳过 Passkey
		// 失败时也必须保留当前账号和邮箱别名，下一次改走登录流程。
		a.AuthStage = AuthStageAccountCreated
		f.markStep("跳过 Passkey")
		nowURL = browser.MustWaitURLChange(ctx, page, func() {
			page.Timeout(timeout).MustElement(`[data-dd-action-name="skip create account enroll passkey"]`).MustClick()
		})
	}
	if nowURL != baseURL {
		unexpectedURL(nowURL)
	}
	logging.SubDone("账号已创建")
	if a.AuthStage < AuthStageAccountCreated {
		a.AuthStage = AuthStageAccountCreated
	}

	f.markStep("获取访问令牌")
	a.AccessToken = gjson.Get(page.MustEval(`async () => {
		const res = await fetch("https://chatgpt.com/api/auth/session/")
		return await res.text()
	}`).String(), "accessToken").String()
	if strings.TrimSpace(a.AccessToken) == "" {
		panic("访问令牌为空")
	}
	a.AuthStage = AuthStageTokenReady
	logging.SubDone("访问令牌已获取")
	f.m.ForgetAddress(a.Email)
	return a
}

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

// waitMailCode 等待转发邮箱收到验证码并写入页面。等待与重发策略由 waitForMailCode 提供。
func (f *Flow) waitMailCode(ctx context.Context, forwardMail string, input func(code string), resend func()) error {
	code, err := waitForMailCode(ctx, f.m, forwardMail, mailCodeWait{
		interval:   f.mailCodeInterval,
		timeout:    f.mailCodeTimeout,
		timeoutSet: f.mailCodeTimeoutSet,
	}, func(context.Context) error {
		if resend != nil {
			resend()
		}
		return nil
	})
	if err != nil {
		return err
	}
	f.markStep("收到邮箱验证码")
	input(code)
	return nil
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
