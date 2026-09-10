package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
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
	accounts      []account
	currentIndex  int
	aliases       map[string]aliasRecord
	usedAddresses map[string]struct{}
	reuseExisting bool
}

type aliasRecord struct {
	id           int64
	accountIndex int
}

type simpleLoginAlias struct {
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	Enabled   bool   `json:"enabled"`
	Mailboxes []struct {
		ID int64 `json:"id"`
	} `json:"mailboxes"`
}

type simpleLoginAliasesResponse struct {
	Aliases []simpleLoginAlias `json:"aliases"`
}

type SimpleLoginOptions struct {
	ReuseExisting bool
	UsedAddresses []string
}

const simpleLoginAliasesPageSize = 20

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
	if sl.reuseExisting {
		address, err := sl.reuseAddress(ctx)
		if err != nil {
			return "", err
		}
		if address != "" {
			return address, nil
		}
	}
	address := nameToAddress(name)
	return sl.newAddress(func() (string, error) {
		options, err := sl.aliasOptions(ctx)
		if err != nil {
			return "", err
		}
		if len(options) == 0 {
			return "", errors.New("SL: no alias suffix available")
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

func (sl *simpleLogin) DelAddressByMetadata(ctx context.Context, metadata AddressMetadata) error {
	if metadata.Provider != "" && !strings.EqualFold(strings.TrimSpace(metadata.Provider), AddressProviderSimpleLogin) {
		return fmt.Errorf("SL: unsupported metadata provider %q", metadata.Provider)
	}
	if metadata.AddressID <= 0 || metadata.OwnerID <= 0 {
		return fmt.Errorf("SL: incomplete alias metadata for %s", metadata.Email)
	}
	accountIndex := -1
	for i, account := range sl.accounts {
		if account.mailboxId == metadata.OwnerID {
			accountIndex = i
			break
		}
	}
	if accountIndex < 0 {
		return fmt.Errorf("SL: mailbox owner not found: %d", metadata.OwnerID)
	}
	record := aliasRecord{id: metadata.AddressID, accountIndex: accountIndex}
	if err := sl.deleteAlias(ctx, record); err != nil {
		sl.releaseUsed(record, metadata.Email)
		return err
	}
	sl.removeAlias(record, metadata.Email)
	return nil
}

func (sl *simpleLogin) deleteAlias(ctx context.Context, record aliasRecord) error {
	if record.id <= 0 || record.accountIndex < 0 || record.accountIndex >= len(sl.accounts) {
		return errors.New("SL: invalid alias record")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/aliases/%d", simpleAPIBase, record.id), nil)
	if err != nil {
		return err
	}
	_, err = client.Do(req, types.NewPair("Authentication", sl.accounts[record.accountIndex].apiKey))
	return err
}

func (sl *simpleLogin) removeAlias(record aliasRecord, address string) {
	key := normalizeAddress(address)
	if key != "" {
		delete(sl.aliases, key)
		delete(sl.usedAddresses, key)
	}
	for key, tracked := range sl.aliases {
		if tracked == record {
			delete(sl.aliases, key)
			delete(sl.usedAddresses, key)
		}
	}
}

func (sl *simpleLogin) releaseUsed(record aliasRecord, address string) {
	if key := normalizeAddress(address); key != "" {
		delete(sl.usedAddresses, key)
	}
	for key, tracked := range sl.aliases {
		if tracked == record {
			delete(sl.usedAddresses, key)
		}
	}
}

func (sl *simpleLogin) ForgetAddress(address string) {
	delete(sl.aliases, normalizeAddress(address))
}

func (sl *simpleLogin) ForwardAddress(ctx context.Context, address string) (string, error) {
	record, ok := sl.aliases[normalizeAddress(address)]
	if !ok {
		return "", fmt.Errorf("SL: alias not found: %s", address)
	}
	if record.accountIndex < 0 || record.accountIndex >= len(sl.accounts) {
		return "", fmt.Errorf("SL: invalid alias owner for %s", address)
	}
	return sl.accounts[record.accountIndex].forward, nil
}

func (sl *simpleLogin) Metadata(address string) AddressMetadata {
	key := normalizeAddress(address)
	record, ok := sl.aliases[key]
	if !ok || record.accountIndex < 0 || record.accountIndex >= len(sl.accounts) {
		return AddressMetadata{Email: address, Provider: AddressProviderSimpleLogin}
	}
	return AddressMetadata{
		Email:     address,
		Provider:  AddressProviderSimpleLogin,
		AddressID: record.id,
		OwnerID:   sl.accounts[record.accountIndex].mailboxId,
	}
}

func (sl *simpleLogin) trackAlias(address string, record aliasRecord) {
	if sl.aliases == nil {
		sl.aliases = make(map[string]aliasRecord)
	}
	sl.aliases[normalizeAddress(address)] = record
}

func (sl *simpleLogin) markAddressUsed(address string) {
	if sl.usedAddresses == nil {
		sl.usedAddresses = make(map[string]struct{})
	}
	sl.usedAddresses[normalizeAddress(address)] = struct{}{}
}

func (sl *simpleLogin) reuseAddress(ctx context.Context) (string, error) {
	if len(sl.accounts) == 0 {
		return "", errors.New("SL: no account available")
	}
	alias, accountIndex, err := sl.findAlias(ctx, func(candidate simpleLoginAlias) bool {
		return candidate.ID > 0 && normalizeAddress(candidate.Email) != "" && candidate.Enabled && !sl.isAddressUsed(candidate.Email)
	})
	if err != nil {
		return "", err
	}
	if accountIndex < 0 {
		return "", nil
	}
	sl.currentIndex = accountIndex
	record := aliasRecord{id: alias.ID, accountIndex: accountIndex}
	sl.trackAlias(alias.Email, record)
	sl.markAddressUsed(alias.Email)
	slog := slog.With(slog.Int("index", accountIndex), slog.String("email", alias.Email))
	slog.Info("SL: reuse existing alias")
	return alias.Email, nil
}

func (sl *simpleLogin) isAddressUsed(address string) bool {
	_, ok := sl.usedAddresses[normalizeAddress(address)]
	return ok
}

func (sl *simpleLogin) findAlias(ctx context.Context, match func(simpleLoginAlias) bool) (simpleLoginAlias, int, error) {
	for accountIndex := range sl.accounts {
		for pageID := 0; ; pageID++ {
			aliases, err := sl.listAliases(ctx, accountIndex, pageID)
			if err != nil {
				return simpleLoginAlias{}, -1, err
			}
			for _, alias := range aliases {
				if match(alias) {
					return alias, sl.aliasOwnerIndex(alias, accountIndex), nil
				}
			}
			if len(aliases) < simpleLoginAliasesPageSize {
				break
			}
		}
	}
	return simpleLoginAlias{}, -1, nil
}

func (sl *simpleLogin) aliasOwnerIndex(alias simpleLoginAlias, fallback int) int {
	for _, mailbox := range alias.Mailboxes {
		for accountIndex, account := range sl.accounts {
			if mailbox.ID != 0 && mailbox.ID == account.mailboxId {
				return accountIndex
			}
		}
	}
	return fallback
}

func (sl *simpleLogin) listAliases(ctx context.Context, accountIndex, pageID int) ([]simpleLoginAlias, error) {
	if accountIndex < 0 || accountIndex >= len(sl.accounts) {
		return nil, fmt.Errorf("SL: invalid account index: %d", accountIndex)
	}
	endpoint, err := url.Parse(simpleAPIBase + "/v2/aliases")
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("page_id", strconv.Itoa(pageID))
	endpoint.RawQuery = query.Encode()
	body, err := client.Get(ctx, endpoint.String(), types.NewPair("Authentication", sl.accounts[accountIndex].apiKey))
	if err != nil {
		return nil, err
	}
	var response simpleLoginAliasesResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("SL: decode aliases: %w", err)
	}
	return response.Aliases, nil
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
	record := aliasRecord{id: aliasID, accountIndex: sl.currentIndex}
	sl.trackAlias(email, record)
	sl.markAddressUsed(email)
	return email, nil
}

func NewSimpleLogin(ctx context.Context, apiKeys []string, option SimpleLoginOptions) (IMailAddress, error) {
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
	usedAddresses := make(map[string]struct{}, len(option.UsedAddresses))
	for _, address := range option.UsedAddresses {
		usedAddresses[normalizeAddress(address)] = struct{}{}
	}
	return &simpleLogin{
		accounts:      accounts,
		aliases:       make(map[string]aliasRecord),
		usedAddresses: usedAddresses,
		reuseExisting: option.ReuseExisting,
	}, nil
}
