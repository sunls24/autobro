package mail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"

	"github.com/sunls24/gox"
	"github.com/sunls24/gox/network/client"
	"github.com/sunls24/gox/types"
	"github.com/tidwall/gjson"
)

var _ IMailWait = (*sunMail)(nil)
var _ IMailAddress = (*sunMail)(nil)

type sunMail struct {
	domains []string
	current string
	apiKey  string
}

func (sm *sunMail) ForwardAddress(ctx context.Context, address string) (string, error) {
	return address, nil
}

func (sm *sunMail) fetchDomain(ctx context.Context) error {
	if len(sm.domains) > 0 {
		return nil
	}
	const PATH = "/domain"
	body, err := client.Get(ctx, sunMailBaseURL+PATH, sm.apiKeyHeader())
	if err != nil {
		return err
	}
	sm.domains = parseSunMailDomains(body)
	if len(sm.domains) == 0 {
		return errors.New("no domain found")
	}
	return nil
}

func parseSunMailDomains(body []byte) []string {
	return gox.Map(gjson.GetBytes(body, "data").Array(), func(r gjson.Result) string {
		return r.String()
	})
}

func (sm *sunMail) DelAddress(ctx context.Context, address string) error {
	return nil
}

func (sm *sunMail) NewAddress(ctx context.Context, name string) (string, error) {
	if err := sm.fetchDomain(ctx); err != nil {
		return "", err
	}
	sm.current = nameToAddress(name) + "@" + sm.domains[rand.IntN(len(sm.domains))]
	return sm.current, nil
}

const sunMailBaseURL = "https://mail.sunls.de/api"

func (sm *sunMail) waitMailCode(ctx context.Context, address string) (string, error) {
	slog.Info("stat wait mail code", slog.String("address", address))
	start := time.Now().Unix()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
			time.Sleep(time.Second)
			body, err := client.Get(ctx, fmt.Sprintf("%s/fetch?to=%s&since=%d", sunMailBaseURL, address, start), sm.apiKeyHeader())
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					slog.Warn("fetch mail failed, retrying", slog.Any("err", err))
					continue
				}
				return "", err
			}
			list := gjson.ParseBytes(body).Get("data").Array()
			for _, item := range list {
				subject := item.Get("subject").String()
				if len(subject) < 6 {
					continue
				}
				code := subject[len(subject)-6:]
				if isDigits(code) {
					return code, nil
				}
				detail, err := client.Get(ctx, fmt.Sprintf("%s/fetch/%s?to=%s", sunMailBaseURL, item.Get("id").String(), address), sm.apiKeyHeader())
				if err != nil {
					return "", err
				}
				htmlContent := gjson.ParseBytes(detail).Get("data.content").String()
				code = extractMailCode(htmlContent)
				if code == "" {
					continue
				}
				return code, nil
			}
		}
	}
}

func (sm *sunMail) WaitMailCode(ctx context.Context, address string) <-chan gox.Result[string] {
	return gox.Async(func() (string, error) {
		return sm.waitMailCode(ctx, address)
	})
}

var codeRE = regexp.MustCompile(`(?:^|[^[:alnum:]#])([0-9]{6})(?:[^[:alnum:]]|$)`)

func extractMailCode(content string) string {
	for _, match := range codeRE.FindAllStringSubmatch(content, -1) {
		if len(match) > 1 {
			return strings.TrimSpace(match[1])
		}
	}
	return ""
}

func isDigits(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func (sm *sunMail) apiKeyHeader() types.Pair[string] {
	return types.NewPair("X-API-Key", sm.apiKey)
}

func NewSunMail(apiKey string, domains ...string) IMail {
	return &sunMail{
		apiKey:  apiKey,
		domains: normalizeSunMailDomains(domains),
	}
}

func normalizeSunMailDomains(domains []string) []string {
	result := make([]string, 0, len(domains))
	for _, domain := range domains {
		domain = strings.TrimPrefix(strings.TrimSpace(domain), "@")
		if domain != "" {
			result = append(result, domain)
		}
	}
	return result
}
