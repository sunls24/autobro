package main

import (
	"autobro/internal/browser"
	"autobro/internal/chatgpt"
	"autobro/internal/mail"
	"autobro/internal/scenemint"
	"autobro/internal/toapi"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"autobro/internal/logging"

	"github.com/go-rod/rod"
)

type optionalBool struct {
	value bool
	set   bool
}

func (b *optionalBool) String() string {
	if !b.set {
		return ""
	}
	return strconv.FormatBool(b.value)
}

func (b *optionalBool) Set(value string) error {
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return err
	}
	b.value = parsed
	b.set = true
	return nil
}

func (b *optionalBool) IsBoolFlag() bool {
	return true
}

func main() {
	logging.Configure(slog.LevelInfo)
	logging.Step("程序", "启动", slog.String("date", time.Now().Format("2006-01-02")))
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		logging.Failure("程序", "运行", err)
		os.Exit(1)
	}
}

func run() (err error) {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = transport.Clone()
		transport.TLSHandshakeTimeout = time.Minute
		http.DefaultTransport = transport
	}
	cfg := toapi.MustNew()
	count := flag.Int("c", 10, "注册数量")
	protocolMode := flag.Bool("p", false, "使用协议混合认证；默认使用浏览器")
	mailProvider := flag.String("m", mail.AddressProviderSunMail, "邮箱地址实现: sun, sl 或 mm")
	sunMailDomains := flag.String("d", "", "SunMail 域名后缀，多个用逗号分隔")
	renew := flag.Bool("r", false, "更新需要重新登录的账号")
	saveAccounts := &optionalBool{}
	flag.Var(saveAccounts, "s", "保存账号；默认仅 sl 保存，sun/mm 需显式开启；可用 -s=false 关闭")
	verbose := flag.Bool("v", false, "输出默认省略的详细步骤日志")
	flag.Parse()
	if *verbose {
		logging.Configure(slog.LevelDebug)
	}
	provider := mail.NormalizeAddressProvider(*mailProvider)
	storeAccounts := provider == mail.AddressProviderSimpleLogin
	if saveAccounts.set {
		storeAccounts = saveAccounts.value
	}
	if !*renew && *count <= 0 {
		return errors.New("注册数量必须大于 0")
	}
	domains := strings.Split(*sunMailDomains, ",")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	address, err := mail.NewAddressProvider(ctx, provider, mail.AddressProviderConfig{
		SLAPIKeys:            cfg.SLAPIKeys,
		SunMailAPIKey:        cfg.SunMailAPIKey,
		SunMailDomains:       domains,
		ManyMeUsername:       cfg.ManyMeUsername,
		ManyMeForwardAddress: cfg.ManyMeForwardAddress,
	})
	if err != nil {
		return fmt.Errorf("初始化邮箱服务失败: %w", err)
	}
	mailService := mail.From(address, mail.NewSunMail(cfg.SunMailAPIKey))
	authenticatorFactory := func() (chatgpt.Authenticator, func() error, error) {
		if *protocolMode {
			var session *browser.Session
			newBrowser := func() (*rod.Browser, error) {
				if session != nil {
					return session.Browser(), nil
				}
				created, sessionErr := browser.NewSession(false)
				if sessionErr != nil {
					return nil, sessionErr
				}
				session = created
				return session.Browser(), nil
			}
			closeSession := func() error {
				if session == nil {
					return nil
				}
				return session.Close()
			}
			return chatgpt.NewProtocol(
				chatgpt.WithProtocolIMail(mailService),
				chatgpt.WithProtocolSentinelProvider(chatgpt.NewLazyHybridSentinelProvider(newBrowser)),
			), closeSession, nil
		}
		session, sessionErr := browser.NewSession(false)
		if sessionErr != nil {
			return nil, nil, sessionErr
		}
		return chatgpt.NewBrowserFlow(mailService, session.Browser()), session.Close, nil
	}
	sceneMint, err := scenemint.NewClient(cfg.SceneMintURL, cfg.SceneMintAPIKey)
	if err != nil {
		return fmt.Errorf("初始化 SceneMint 失败: %w", err)
	}
	worker := toapi.New(authenticatorFactory, sceneMint, storeAccounts)
	if *renew {
		err = worker.Renew(ctx)
	} else {
		err = worker.Start(ctx, *count)
	}
	return err
}
