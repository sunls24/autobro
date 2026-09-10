package mail

import (
	"context"
	"errors"
	"fmt"
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
	var domains []string
	_, err := retrySunMail(ctx, "domain", func() ([]byte, error) {
		body, err := client.Get(ctx, sunMailBaseURL+PATH, sm.apiKeyHeader())
		if err != nil {
			return nil, err
		}
		domains = parseSunMailDomains(body)
		if len(domains) == 0 {
			return nil, errors.New("no domain found")
		}
		return body, nil
	})
	if err != nil {
		return err
	}
	sm.domains = domains
	return nil
}

func parseSunMailDomains(body []byte) []string {
	return gox.Map(gjson.GetBytes(body, "data").Array(), func(r gjson.Result) string {
		return r.String()
	})
}

func (sm *sunMail) DelAddressByMetadata(ctx context.Context, metadata AddressMetadata) error {
	return nil
}

func (sm *sunMail) ForgetAddress(address string) {}

func (sm *sunMail) Metadata(address string) AddressMetadata {
	return AddressMetadata{Email: address, Provider: AddressProviderSunMail}
}

func (sm *sunMail) NewAddress(ctx context.Context, name string) (string, error) {
	if err := sm.fetchDomain(ctx); err != nil {
		return "", err
	}
	sm.current = nameToAddress(name) + "@" + sm.domains[rand.IntN(len(sm.domains))]
	return sm.current, nil
}

var sunMailBaseURL = "https://prod.sunlss.com/api"

var (
	sunMailRetryInitialDelay = 2 * time.Second
	sunMailRetryMaxDelay     = 20 * time.Second
)

func retrySunMail(ctx context.Context, operation string, request func() ([]byte, error)) ([]byte, error) {
	delay := sunMailRetryInitialDelay
	for attempt := 1; ; attempt++ {
		body, err := request()
		if err == nil {
			return body, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("SunMail %s: %w (last error: %v)", operation, ctxErr, err)
		}
		slog.Warn("SunMail request failed, retrying", slog.String("operation", operation), slog.Int("attempt", attempt), slog.Any("err", err))
		if waitErr := waitContext(ctx, delay); waitErr != nil {
			return nil, fmt.Errorf("SunMail %s: %w (last request error: %v)", operation, waitErr, err)
		}
		if delay < sunMailRetryMaxDelay {
			delay *= 2
			if delay > sunMailRetryMaxDelay {
				delay = sunMailRetryMaxDelay
			}
		}
	}
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (sm *sunMail) fetchMailList(ctx context.Context, address string, start int64) ([]gjson.Result, error) {
	var list []gjson.Result
	_, err := retrySunMail(ctx, "fetch", func() ([]byte, error) {
		body, err := client.Get(ctx, fmt.Sprintf("%s/fetch?to=%s&since=%d", sunMailBaseURL, address, start), sm.apiKeyHeader())
		if err != nil {
			return nil, err
		}
		data := gjson.GetBytes(body, "data")
		if !data.Exists() || !data.IsArray() {
			return nil, errors.New("invalid fetch response")
		}
		list = data.Array()
		return body, nil
	})
	return list, err
}

func (sm *sunMail) fetchMailDetail(ctx context.Context, address, id string) ([]byte, error) {
	return retrySunMail(ctx, "fetch detail", func() ([]byte, error) {
		body, err := client.Get(ctx, fmt.Sprintf("%s/fetch/%s?to=%s", sunMailBaseURL, id, address), sm.apiKeyHeader())
		if err != nil {
			return nil, err
		}
		if !gjson.GetBytes(body, "data").Exists() {
			return nil, errors.New("invalid fetch detail response")
		}
		return body, nil
	})
}

func (sm *sunMail) waitMailCode(ctx context.Context, address string) (string, error) {
	slog.Info("stat wait mail code", slog.String("address", address))
	start := time.Now().Unix()
	for {
		if err := waitContext(ctx, time.Second); err != nil {
			return "", err
		}
		list, err := sm.fetchMailList(ctx, address, start)
		if err != nil {
			return "", err
		}
		for _, item := range list {
			subject := item.Get("subject").String()
			if len(subject) < 6 {
				continue
			}
			code := subject[len(subject)-6:]
			if isDigits(code) {
				return code, nil
			}
			detail, err := sm.fetchMailDetail(ctx, address, item.Get("id").String())
			if err != nil {
				return "", err
			}
			htmlContent := gjson.GetBytes(detail, "data.content").String()
			code = extractMailCode(htmlContent)
			if code == "" {
				continue
			}
			return code, nil
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
