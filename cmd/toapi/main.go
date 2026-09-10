package main

import (
	"codex-free/internal/browser"
	"codex-free/internal/chatgpt"
	"codex-free/internal/mail"
	"codex-free/internal/scenemint"
	"codex-free/internal/toapi"
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
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
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = transport.Clone()
		transport.TLSHandshakeTimeout = time.Minute
		http.DefaultTransport = transport
	}
	//slog.SetLogLoggerLevel(slog.LevelDebug)
	cfg := toapi.MustNew()
	count := flag.Int("c", 10, "注册数量")
	mailProvider := flag.String("m", mail.AddressProviderSimpleLogin, "邮箱地址实现: sl, sun 或 mm")
	sunMailDomains := flag.String("d", "", "SunMail 域名后缀，多个用逗号分隔")
	renew := flag.Bool("r", false, "更新需要重新登录的账号")
	saveAccounts := &optionalBool{}
	flag.Var(saveAccounts, "s", "保存账号；默认仅 sl 保存，可用 -s=false 关闭")
	flag.Parse()
	provider := mail.NormalizeAddressProvider(*mailProvider)
	storeAccounts := provider == mail.AddressProviderSimpleLogin
	if saveAccounts.set {
		storeAccounts = saveAccounts.value
	}
	if !*renew && *count <= 0 {
		panic("count must be greater than 0")
	}
	domains := strings.Split(*sunMailDomains, ",")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	var savedAccounts []chatgpt.Account
	if (provider == mail.AddressProviderSimpleLogin && storeAccounts && !*renew) || *renew {
		savedAccounts, err = toapi.LoadAccounts(toapi.AccountsFile)
		if err != nil {
			panic(err)
		}
	}
	usedAddresses := make([]string, 0, len(savedAccounts))
	if provider == mail.AddressProviderSimpleLogin && storeAccounts && !*renew {
		for _, account := range savedAccounts {
			if account.MailProvider == "" || strings.EqualFold(strings.TrimSpace(account.MailProvider), mail.AddressProviderSimpleLogin) {
				usedAddresses = append(usedAddresses, account.Email)
			}
		}
	}

	address, err := mail.NewAddressProvider(ctx, provider, mail.AddressProviderConfig{
		SLAPIKeys:            cfg.SLAPIKeys,
		SLReuseExisting:      provider == mail.AddressProviderSimpleLogin && storeAccounts && !*renew,
		SLUsedAddresses:      usedAddresses,
		SunMailAPIKey:        cfg.SunMailAPIKey,
		SunMailDomains:       domains,
		ManyMeUsername:       cfg.ManyMeUsername,
		ManyMeForwardAddress: cfg.ManyMeForwardAddress,
	})
	if err != nil {
		panic(err)
	}
	var simpleLoginCleaner mail.IMailAddress
	if provider == mail.AddressProviderSimpleLogin {
		simpleLoginCleaner = address
	} else if *renew {
		needSimpleLoginCleaner := false
		for _, account := range savedAccounts {
			if strings.EqualFold(strings.TrimSpace(account.MailProvider), mail.AddressProviderSimpleLogin) {
				needSimpleLoginCleaner = true
				break
			}
		}
		if needSimpleLoginCleaner {
			slCleaner, cleanerErr := mail.NewAddressProvider(ctx, mail.AddressProviderSimpleLogin, mail.AddressProviderConfig{
				SLAPIKeys: cfg.SLAPIKeys,
			})
			if cleanerErr != nil {
				slog.Error("初始化 SimpleLogin 清理器失败", slog.Any("err", cleanerErr))
			} else {
				simpleLoginCleaner = slCleaner
			}
		}
	}
	bro, err := browser.NewDefault(false)
	if err != nil {
		panic(err)
	}
	defer bro.MustClose()
	sceneMint, err := scenemint.NewClient(cfg.SceneMintURL, cfg.SceneMintAPIKey)
	if err != nil {
		panic(err)
	}
	worker := toapi.New(mail.From(address, mail.NewSunMail(cfg.SunMailAPIKey)), bro, sceneMint, storeAccounts, simpleLoginCleaner)
	if *renew {
		err = worker.Renew(ctx)
	} else {
		err = worker.Start(ctx, *count)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		panic(err)
	}
}
