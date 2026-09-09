package mail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"

	"github.com/sunls24/gox"
	"github.com/sunls24/gox/network/client"
	"github.com/sunls24/gox/network/header"
	"github.com/sunls24/gox/types"
	"github.com/tidwall/gjson"
)

type account struct {
	mailboxId int64
	apiKey    string
	forward   string
}

type simpleLogin struct {
	accounts     []account
	currentIndex int
	aliases      map[string]aliasRecord
}

type aliasRecord struct {
	id           int64
	accountIndex int
}

func (sl *simpleLogin) nextAccount() {
	sl.currentIndex = (sl.currentIndex + 1) % len(sl.accounts)
	slog.Info(
		"SL: use next account",
		slog.Int("index", sl.currentIndex),
		slog.String("forward", sl.accounts[sl.currentIndex].forward),
	)
}

func (sl *simpleLogin) apiKey() string {
	return sl.accounts[sl.currentIndex].apiKey
}

func (sl *simpleLogin) mailboxId() int64 {
	return sl.accounts[sl.currentIndex].mailboxId
}

func (sl *simpleLogin) auth() types.Pair[string] {
	return types.NewPair("Authentication", sl.apiKey())
}

func (sl *simpleLogin) NewAddress(ctx context.Context, name string) (string, error) {
	address := nameToAddress(name)
	return sl.newAddress(func() (string, error) {
		options, err := sl.aliasOptions(ctx)
		if err != nil {
			return "", err
		}
		return sl.aliasCustomNew(ctx, address, options[rand.IntN(len(options))].signedSuffix)
	})
}

func (sl *simpleLogin) newAddress(create func() (string, error)) (string, error) {
	var lastErr error
	for attempts := 0; attempts < len(sl.accounts); attempts++ {
		result, err := create()
		if err == nil {
			return result, nil
		}
		if !shouldRotateSimpleLoginAccount(err) {
			return "", err
		}
		lastErr = err
		if attempts < len(sl.accounts)-1 {
			// 账号受限时最多绕账号列表一圈，避免递归漏试或无限重试。
			sl.nextAccount()
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", errors.New("SL: no account attempted")
}

func shouldRotateSimpleLoginAccount(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "429") ||
		strings.Contains(message, "maximum of 10 aliases")
}

func (sl *simpleLogin) DelAddress(ctx context.Context, address string) error {
	record, ok := sl.aliases[address]
	if !ok {
		return fmt.Errorf("SL: alias not found: %s", address)
	}
	defer func() {
		delete(sl.aliases, address)
		sl.currentIndex = (record.accountIndex + 1) % len(sl.accounts)
		slog.Info(
			"SL: use next account",
			slog.Int("index", sl.currentIndex),
			slog.String("forward", sl.accounts[sl.currentIndex].forward),
		)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/aliases/%d", simpleAPIBase, record.id), nil)
	if err != nil {
		return err
	}
	_, err = client.Do(req, types.NewPair("Authentication", sl.accounts[record.accountIndex].apiKey))
	return err
}

func (sl *simpleLogin) ForgetAddress(address string) {
	delete(sl.aliases, address)
}

func (sl *simpleLogin) ForwardAddress(ctx context.Context, address string) (string, error) {
	record, ok := sl.aliases[address]
	if !ok {
		return "", fmt.Errorf("SL: alias not found: %s", address)
	}
	return sl.accounts[record.accountIndex].forward, nil
}

var simpleAPIBase = "https://app.simplelogin.io/api"

type suffix struct {
	isPremium    bool
	signedSuffix string
	suffix       string
}

func (sl *simpleLogin) aliasOptions(ctx context.Context) ([]suffix, error) {
	const PATH = "/v5/alias/options"
	body, err := client.Get(ctx, simpleAPIBase+PATH, sl.auth())
	if err != nil {
		return nil, err
	}
	slog.Debug("SL: aliasOptions\n" + string(body))
	suffixes := gjson.GetBytes(body, "suffixes").Array()
	return gox.Map(suffixes, func(r gjson.Result) suffix {
		return suffix{
			isPremium:    r.Get("is_premium").Bool(),
			signedSuffix: r.Get("signed_suffix").String(),
			suffix:       r.Get("suffix").String(),
		}
	}), nil
}

func (sl *simpleLogin) aliasCustomNew(ctx context.Context, custom string, signedSuffix string) (string, error) {
	const PATH = "/v3/alias/custom/new"
	body, err := client.Post(ctx, simpleAPIBase+PATH, map[string]any{
		"alias_prefix":  custom,
		"signed_suffix": signedSuffix,
		"mailbox_ids":   []int64{sl.mailboxId()},
	}, header.New().ContentTypeJSON().Add(sl.auth()).Get()...)
	if err != nil {
		return "", err
	}
	slog.Debug("SL: aliasCustomNew\n" + string(body))
	email := gjson.GetBytes(body, "email").String()
	aliasID := gjson.GetBytes(body, "id").Int()
	if email == "" || aliasID == 0 {
		return "", errors.New("SL: invalid alias response")
	}
	sl.aliases[email] = aliasRecord{
		id:           aliasID,
		accountIndex: sl.currentIndex,
	}
	return email, nil
}

func NewSimpleLogin(ctx context.Context, apiKeys []string) (IMailAddress, error) {
	const PATH = "/v2/mailboxes"

	accounts := make([]account, 0, len(apiKeys))
	for i, apiKey := range apiKeys {
		body, err := client.Get(ctx, simpleAPIBase+PATH, types.NewPair("Authentication", apiKey))
		if err != nil {
			slog.Error("SL: get mailboxes", slog.Any("err", err), slog.Int("index", i))
			continue
		}
		slog.Debug("SL: mailboxes\n" + string(body))
		for _, box := range gjson.GetBytes(body, "mailboxes").Array() {
			verified := box.Get("verified").Bool()
			if !verified {
				continue
			}
			id := box.Get("id").Int()
			email := box.Get("email").String()
			slog.Debug("SL: add account", slog.String("forward", email), slog.Int64("mailboxId", id))
			accounts = append(accounts, account{
				mailboxId: id,
				apiKey:    apiKey,
				forward:   email,
			})
			break
		}
	}
	if len(accounts) == 0 {
		return nil, errors.New("SL: no valid account found")
	}
	return &simpleLogin{
		accounts: accounts,
		aliases:  make(map[string]aliasRecord),
	}, nil
}
