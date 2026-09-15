package mail

import (
	"bytes"
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
	accounts          []account
	currentIndex      int
	aliases           map[string]aliasRecord
	aliasCounts       []int
	reusableAliases   [][]aliasCandidate
	aliasCountsLoaded bool
}

type aliasRecord struct {
	id           int64
	accountIndex int
	created      bool
}

type aliasCandidate struct {
	email  string
	record aliasRecord
}

type simpleLoginAliasesResponse struct {
	Aliases []simpleLoginAlias `json:"aliases"`
}

type simpleLoginAlias struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Note  string `json:"note"`
}

const (
	simpleLoginAliasesPageSize  = 20
	simpleLoginMaxAliases       = 4
	simpleLoginRegistrationNote = "autobro:chatgpt:v1"
	simpleLoginReusableNote     = "autobro:chatgpt:reusable"
)

func (sl *simpleLogin) nextAccount() {
	sl.currentIndex = (sl.currentIndex + 1) % len(sl.accounts)
	logMailStep(
		"SimpleLogin",
		"切换账号",
		slog.String("address", sl.accounts[sl.currentIndex].forward),
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
	if len(sl.accounts) == 0 {
		return "", errors.New("SL：无可用账号")
	}
	if err := sl.ensureAliasCounts(ctx); err != nil {
		return "", err
	}
	for attempts := 0; attempts < len(sl.accounts); attempts++ {
		address, reused, err := sl.reuseAlias(ctx, sl.currentIndex)
		if err != nil {
			return "", err
		}
		if reused {
			return address, nil
		}
		if sl.aliasCount(sl.currentIndex) < simpleLoginMaxAliases {
			return sl.createAddress(ctx, name)
		}
		if attempts < len(sl.accounts)-1 {
			sl.nextAccount()
		}
	}
	return "", fmt.Errorf("SL：所有账号均达到 %d 个别名上限", simpleLoginMaxAliases)
}

func (sl *simpleLogin) ensureAliasCounts(ctx context.Context) error {
	if sl.aliasCountsLoaded {
		return nil
	}
	// Renew only needs the mailbox identities; defer the alias scan until a new
	// registration actually needs capacity accounting.
	counts := make([]int, len(sl.accounts))
	reusableAliases := make([][]aliasCandidate, len(sl.accounts))
	for accountIndex, account := range sl.accounts {
		count := 0
		for pageID := 0; ; pageID++ {
			aliases, err := sl.listAliases(ctx, accountIndex, pageID)
			if err != nil {
				return fmt.Errorf("SL：拉取 %s 的别名列表：%w", account.forward, err)
			}
			count += len(aliases)
			for _, alias := range aliases {
				if alias.ID <= 0 || strings.TrimSpace(alias.Email) == "" {
					return fmt.Errorf("SL：%s 返回的别名数据异常", account.forward)
				}
				if strings.TrimSpace(alias.Note) != simpleLoginRegistrationNote {
					reusableAliases[accountIndex] = append(reusableAliases[accountIndex], aliasCandidate{
						email: alias.Email,
						record: aliasRecord{
							id:           alias.ID,
							accountIndex: accountIndex,
						},
					})
				}
			}
			if len(aliases) < simpleLoginAliasesPageSize {
				break
			}
		}
		counts[accountIndex] = count
		logMailDebug("SimpleLogin", "统计已有别名", slog.String("address", account.forward), slog.Int("count", count), slog.Int("可复用", len(reusableAliases[accountIndex])))
	}
	sl.aliasCounts = counts
	sl.reusableAliases = reusableAliases
	sl.aliasCountsLoaded = true
	return nil
}

func (sl *simpleLogin) reuseAlias(ctx context.Context, accountIndex int) (string, bool, error) {
	if accountIndex < 0 || accountIndex >= len(sl.reusableAliases) || len(sl.reusableAliases[accountIndex]) == 0 {
		return "", false, nil
	}
	candidate := sl.reusableAliases[accountIndex][0]
	if err := sl.markAliasUsed(ctx, candidate.record); err != nil {
		return "", false, fmt.Errorf("SL：标记别名 %s：%w", candidate.email, err)
	}
	sl.reusableAliases[accountIndex] = sl.reusableAliases[accountIndex][1:]
	sl.trackAlias(candidate.email, candidate.record)
	return candidate.email, true, nil
}

func (sl *simpleLogin) createAddress(ctx context.Context, name string) (string, error) {
	address := nameToAddress(name)
	options, err := sl.aliasOptions(ctx)
	if err != nil {
		return "", err
	}
	if len(options) == 0 {
		return "", errors.New("SL：无可用别名后缀")
	}
	return sl.aliasCustomNew(ctx, address, options[rand.IntN(len(options))].signedSuffix)
}

func (sl *simpleLogin) aliasCount(accountIndex int) int {
	if accountIndex < 0 || accountIndex >= len(sl.aliasCounts) {
		return 0
	}
	return sl.aliasCounts[accountIndex]
}

func (sl *simpleLogin) incrementAliasCount(accountIndex int) {
	if accountIndex < 0 {
		return
	}
	if accountIndex >= len(sl.aliasCounts) {
		counts := make([]int, len(sl.accounts))
		copy(counts, sl.aliasCounts)
		sl.aliasCounts = counts
	}
	sl.aliasCounts[accountIndex]++
}

func (sl *simpleLogin) decrementAliasCount(accountIndex int) {
	if accountIndex < 0 || accountIndex >= len(sl.aliasCounts) || sl.aliasCounts[accountIndex] == 0 {
		return
	}
	sl.aliasCounts[accountIndex]--
}

func (sl *simpleLogin) DelAddressByMetadata(ctx context.Context, metadata AddressMetadata) error {
	if metadata.Provider != "" && !strings.EqualFold(strings.TrimSpace(metadata.Provider), AddressProviderSimpleLogin) {
		return fmt.Errorf("SL：不支持的元数据提供方 %q", metadata.Provider)
	}
	record, tracked := sl.aliases[normalizeAddress(metadata.Email)]
	if tracked {
		if !record.created {
			if err := sl.releaseAlias(ctx, record); err != nil {
				return err
			}
			sl.removeAlias(record, metadata.Email)
			sl.requeueAlias(record, metadata.Email)
			return nil
		}
	} else {
		if metadata.AddressID <= 0 || metadata.OwnerID <= 0 {
			return fmt.Errorf("SL：%s 的别名元数据不完整", metadata.Email)
		}
		accountIndex := -1
		for i, account := range sl.accounts {
			if account.mailboxId == metadata.OwnerID {
				accountIndex = i
				break
			}
		}
		if accountIndex < 0 {
			return fmt.Errorf("SL：未找到邮箱归属者 %d", metadata.OwnerID)
		}
		record = aliasRecord{id: metadata.AddressID, accountIndex: accountIndex, created: true}
	}
	if err := sl.deleteAlias(ctx, record); err != nil {
		return err
	}
	sl.removeAlias(record, metadata.Email)
	sl.decrementAliasCount(record.accountIndex)
	return nil
}

func (sl *simpleLogin) deleteAlias(ctx context.Context, record aliasRecord) error {
	if record.id <= 0 || record.accountIndex < 0 || record.accountIndex >= len(sl.accounts) {
		return errors.New("SL：别名记录无效")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/aliases/%d", simpleAPIBase, record.id), nil)
	if err != nil {
		return err
	}
	_, err = client.Do(req, types.NewPair("Authentication", sl.accounts[record.accountIndex].apiKey))
	return err
}

func (sl *simpleLogin) markAliasUsed(ctx context.Context, record aliasRecord) error {
	return sl.setAliasNote(ctx, record, simpleLoginRegistrationNote)
}

func (sl *simpleLogin) releaseAlias(ctx context.Context, record aliasRecord) error {
	err := sl.setAliasNote(ctx, record, simpleLoginReusableNote)
	if err == nil || ctx.Err() != nil {
		return err
	}
	if retryErr := sl.setAliasNote(ctx, record, simpleLoginReusableNote); retryErr != nil {
		return errors.Join(err, retryErr)
	}
	return nil
}

func (sl *simpleLogin) setAliasNote(ctx context.Context, record aliasRecord, note string) error {
	if record.id <= 0 || record.accountIndex < 0 || record.accountIndex >= len(sl.accounts) {
		return errors.New("SL：别名记录无效")
	}
	body, err := json.Marshal(map[string]string{"note": note})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, fmt.Sprintf("%s/aliases/%d", simpleAPIBase, record.id), bytes.NewReader(body))
	if err != nil {
		return err
	}
	_, err = client.Do(req, header.New().ContentTypeJSON().Add(types.NewPair("Authentication", sl.accounts[record.accountIndex].apiKey)).Get()...)
	return err
}

func (sl *simpleLogin) requeueAlias(record aliasRecord, address string) {
	sl.reusableAliases[record.accountIndex] = append(
		[]aliasCandidate{{email: address, record: record}},
		sl.reusableAliases[record.accountIndex]...,
	)
}

func (sl *simpleLogin) removeAlias(record aliasRecord, address string) {
	key := normalizeAddress(address)
	if key != "" {
		delete(sl.aliases, key)
	}
	for key, tracked := range sl.aliases {
		if tracked == record {
			delete(sl.aliases, key)
		}
	}
}

func (sl *simpleLogin) ForgetAddress(address string) {
	delete(sl.aliases, normalizeAddress(address))
}

func (sl *simpleLogin) ForwardAddress(ctx context.Context, address string) (string, error) {
	record, ok := sl.aliases[normalizeAddress(address)]
	if !ok {
		return "", fmt.Errorf("SL：未找到别名 %s", address)
	}
	if record.accountIndex < 0 || record.accountIndex >= len(sl.accounts) {
		return "", fmt.Errorf("SL：%s 的别名归属者无效", address)
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

func (sl *simpleLogin) listAliases(ctx context.Context, accountIndex, pageID int) ([]simpleLoginAlias, error) {
	if accountIndex < 0 || accountIndex >= len(sl.accounts) {
		return nil, fmt.Errorf("SL：无效的账号序号 %d", accountIndex)
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
		return nil, fmt.Errorf("SL：解码别名列表：%w", err)
	}
	return response.Aliases, nil
}

var simpleAPIBase = "https://app.simplelogin.io/api"

type suffix struct {
	signedSuffix string
}

func (sl *simpleLogin) aliasOptions(ctx context.Context) ([]suffix, error) {
	const PATH = "/v5/alias/options"
	body, err := client.Get(ctx, simpleAPIBase+PATH, sl.auth())
	if err != nil {
		return nil, err
	}
	suffixes := gjson.GetBytes(body, "suffixes").Array()
	if len(suffixes) == 0 {
		// 解析结果为空时输出原始响应便于定位接口变更；正常路径不刷响应体。
		logMailDebug("SimpleLogin", "别名选项响应异常", slog.String("body", string(body)))
	}
	return gox.Map(suffixes, func(r gjson.Result) suffix {
		return suffix{signedSuffix: r.Get("signed_suffix").String()}
	}), nil
}

func (sl *simpleLogin) aliasCustomNew(ctx context.Context, custom string, signedSuffix string) (string, error) {
	const PATH = "/v3/alias/custom/new"
	body, err := client.Post(ctx, simpleAPIBase+PATH, map[string]any{
		"alias_prefix":  custom,
		"signed_suffix": signedSuffix,
		"mailbox_ids":   []int64{sl.mailboxId()},
		"note":          simpleLoginRegistrationNote,
	}, header.New().ContentTypeJSON().Add(sl.auth()).Get()...)
	if err != nil {
		return "", err
	}
	email := gjson.GetBytes(body, "email").String()
	aliasID := gjson.GetBytes(body, "id").Int()
	if email == "" || aliasID == 0 {
		logMailDebug("SimpleLogin", "创建别名响应异常", slog.String("body", string(body)))
		return "", errors.New("SL：创建别名响应异常")
	}
	logMailDebug("SimpleLogin", "别名已创建", slog.String("address", email), slog.Int64("aliasId", aliasID))
	record := aliasRecord{id: aliasID, accountIndex: sl.currentIndex, created: true}
	sl.trackAlias(email, record)
	sl.incrementAliasCount(sl.currentIndex)
	return email, nil
}

func NewSimpleLogin(ctx context.Context, apiKeys []string) (IMailAddress, error) {
	const PATH = "/v2/mailboxes"

	accounts := make([]account, 0, len(apiKeys))
	for _, apiKey := range apiKeys {
		body, err := client.Get(ctx, simpleAPIBase+PATH, types.NewPair("Authentication", apiKey))
		if err != nil {
			logMailFailure("SimpleLogin", "获取邮箱列表", err)
			continue
		}
		added := false
		for _, box := range gjson.GetBytes(body, "mailboxes").Array() {
			verified := box.Get("verified").Bool()
			if !verified {
				continue
			}
			id := box.Get("id").Int()
			email := box.Get("email").String()
			logMailDebug("SimpleLogin", "添加邮箱账号", slog.String("address", email), slog.Int64("mailboxId", id))
			accounts = append(accounts, account{
				mailboxId: id,
				apiKey:    apiKey,
				forward:   email,
			})
			added = true
			break
		}
		if !added {
			// 响应里没有已验证邮箱时输出原始响应，便于定位接口变更或 Key 失效。
			logMailDebug("SimpleLogin", "邮箱列表无已验证账号", slog.String("body", string(body)))
		}
	}
	if len(accounts) == 0 {
		return nil, errors.New("SL：未找到有效的邮箱账号")
	}
	return &simpleLogin{
		accounts: accounts,
		aliases:  make(map[string]aliasRecord),
	}, nil
}
