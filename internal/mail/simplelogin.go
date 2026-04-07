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

	lastestAlias   string
	lastestAliasId int64
}

type simpleLogin struct {
	accounts     []account
	currentIndex int
}

func (sl *simpleLogin) nextAccount() bool {
	defer func() {
		slog.Info("SL: use next account", slog.Int("index", sl.currentIndex), slog.String("forward", sl.accounts[sl.currentIndex].forward))
	}()
	sl.currentIndex++
	if sl.currentIndex >= len(sl.accounts) {
		sl.currentIndex = 0
		return false
	}
	return true
}

func (sl *simpleLogin) apiKey() string {
	return sl.accounts[sl.currentIndex].apiKey
}

func (sl *simpleLogin) mailboxId() int64 {
	return sl.accounts[sl.currentIndex].mailboxId
}

func (sl *simpleLogin) setLastest(alias string, aliasId int64) {
	sl.accounts[sl.currentIndex].lastestAlias = alias
	sl.accounts[sl.currentIndex].lastestAliasId = aliasId
}

func (sl *simpleLogin) auth() types.Pair[string] {
	return types.NewPair("Authentication", sl.apiKey())
}

func (sl *simpleLogin) NewAddress(ctx context.Context, name string) (string, error) {
	address := nameToAddress(name)
	options, err := sl.aliasOptions(ctx)
	if err != nil {
		return "", err
	}
	result, err := sl.aliasCustomNew(ctx, address, options[rand.IntN(len(options))].signedSuffix)
	if err != nil {
		if strings.Contains(err.Error(), "429") && sl.nextAccount() {
			return sl.NewAddress(ctx, name)
		}
		return "", err
	}
	return result, nil
}

func (sl *simpleLogin) DelAddress(ctx context.Context, address string) error {
	var aliasId int64
	if ca := sl.accounts[sl.currentIndex]; address == ca.lastestAlias {
		aliasId = ca.lastestAliasId
	} else {
		for _, a := range sl.accounts {
			if a.lastestAlias == address {
				aliasId = a.lastestAliasId
			}
		}
	}
	if aliasId == 0 {
		return errors.New("lastest alias not found")
	}
	req, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/aliases/%d", simpleAPIBase, aliasId), nil)
	if err != nil {
		return err
	}
	_, err = client.Do(req, sl.auth())
	return err
}

func (sl *simpleLogin) ForwardAddress(ctx context.Context) (string, error) {
	return sl.accounts[sl.currentIndex].forward, nil
}

const (
	simpleAPIBase = "https://app.simplelogin.io/api"
)

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
	alias := gjson.GetBytes(body, "alias").String()
	sl.setLastest(alias, gjson.GetBytes(body, "id").Int())
	return gjson.GetBytes(body, "email").String(), nil
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
	return &simpleLogin{accounts: accounts}, nil
}
