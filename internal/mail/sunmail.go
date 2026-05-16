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
	"github.com/tidwall/gjson"
)

var _ IMailWait = (*sunMail)(nil)
var _ IMailAddress = (*sunMail)(nil)

type sunMail struct {
	domains []string
	current string
}

func (sm *sunMail) ForwardAddress(ctx context.Context, address string) (string, error) {
	return address, nil
}

func (sm *sunMail) fetchDomain(ctx context.Context) error {
	if len(sm.domains) > 0 {
		return nil
	}
	const PATH = "/domain"
	body, err := client.Get(ctx, sunMailBaseURL+PATH)
	if err != nil {
		return err
	}
	sm.domains = gox.Map(gjson.ParseBytes(body).Array(), func(r gjson.Result) string {
		return r.String()
	})
	if len(sm.domains) == 0 {
		return errors.New("no domain found")
	}
	return nil
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

const (
	sunMailBaseURL = "https://mail.sunls.de/api"
)

func (sm *sunMail) waitMailCode(ctx context.Context, address string) (string, error) {
	slog.Info("stat wait mail code", slog.String("address", address))
	start := time.Now().Unix()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
			time.Sleep(time.Second)
			body, err := client.Get(ctx, fmt.Sprintf("%s/fetch?to=%s&since=%d", sunMailBaseURL, address, start))
			if err != nil {
				return "", err
			}
			list := gjson.ParseBytes(body).Array()
			for _, item := range list {
				subject := item.Get("subject").String()
				if len(subject) < 6 {
					continue
				}
				code := subject[len(subject)-6:]
				if isDigits(code) {
					return code, nil
				}
				detail, err := client.Get(ctx, fmt.Sprintf("%s/fetch/%s", sunMailBaseURL, item.Get("id").String()))
				if err != nil {
					return "", err
				}
				htmlContent := gjson.ParseBytes(detail).Get("content").String()
				codes := codeRE.FindAllString(htmlContent, 5)
				code = ""
				for _, v := range codes {
					if v[:1] == "#" {
						continue
					}
					code = strings.TrimSpace(v)
					break
				}
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

var codeRE = regexp.MustCompile(`.\b\d{6}\b`)

func isDigits(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func NewSunMail() IMail {
	return &sunMail{}
}
