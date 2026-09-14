package mail

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseSunMailDomains(t *testing.T) {
	t.Parallel()

	body := []byte(`{"code":0,"message":"ok","data":["isco.eu.org","sunix.eu.org","chato.eu.org"]}`)
	domains := parseSunMailDomains(body)

	want := []string{"isco.eu.org", "sunix.eu.org", "chato.eu.org"}
	if len(domains) != len(want) {
		t.Fatalf("parseSunMailDomains() = %q, want %q", domains, want)
	}
	for i := range want {
		if domains[i] != want[i] {
			t.Fatalf("parseSunMailDomains()[%d] = %q, want %q", i, domains[i], want[i])
		}
	}
}

func TestNewSunMailUsesSpecifiedDomain(t *testing.T) {
	t.Parallel()

	provider := NewSunMail("", " @example.com ")
	address, err := provider.NewAddress(context.Background(), "Alice Smith")
	if err != nil {
		t.Fatalf("NewAddress() error = %v", err)
	}
	if !strings.HasSuffix(address, "@example.com") {
		t.Fatalf("NewAddress() = %q, want example.com domain", address)
	}
}

func TestSunMailSkipsDomainDiscoveryForSpecifiedDomains(t *testing.T) {
	called := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called <- struct{}{}
		http.Error(w, "domain discovery should be skipped", http.StatusInternalServerError)
	}))
	defer server.Close()

	provider := NewSunMailWithConfig(SunMailConfig{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Domains:    []string{" @Example.com "},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	address, err := provider.NewAddress(ctx, "Alice Smith")
	if err != nil {
		t.Fatalf("NewAddress() error = %v", err)
	}
	if !strings.HasSuffix(address, "@example.com") {
		t.Fatalf("NewAddress() = %q, want example.com domain", address)
	}
	select {
	case <-called:
		t.Fatal("domain discovery request was made")
	default:
	}
}

func TestNormalizeSunMailDomainsSkipsEmptyValues(t *testing.T) {
	t.Parallel()

	domains := normalizeSunMailDomains([]string{"", " @First.Example ", "first.example", "  ", "SECOND.example"})
	want := []string{"first.example", "second.example"}
	if len(domains) != len(want) {
		t.Fatalf("normalizeSunMailDomains() = %q, want %q", domains, want)
	}
	for i := range want {
		if domains[i] != want[i] {
			t.Fatalf("normalizeSunMailDomains()[%d] = %q, want %q", i, domains[i], want[i])
		}
	}
}

func TestSunMailUsesConfiguredBaseURLForMailbox(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-API-Key"); got != "sunmail-test-key" {
			t.Fatalf("X-API-Key = %q, want sunmail-test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fetch":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case "/fetch/message-1":
			_, _ = w.Write([]byte(`{"data":{"content":"944160"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	sm := newSunMail(SunMailConfig{
		APIKey:     "sunmail-test-key",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Domains:    []string{"example.com"},
	})
	if _, err := sm.fetchMailList(t.Context(), "alice@example.com", 1); err != nil {
		t.Fatalf("fetchMailList() error = %v", err)
	}
	if _, err := sm.fetchMailDetail(t.Context(), "alice@example.com", "message-1"); err != nil {
		t.Fatalf("fetchMailDetail() error = %v", err)
	}
}

func TestRetrySunMailRetriesEveryError(t *testing.T) {
	oldInitialDelay, oldMaxDelay := sunMailRetryInitialDelay, sunMailRetryMaxDelay
	sunMailRetryInitialDelay = time.Millisecond
	sunMailRetryMaxDelay = time.Millisecond
	defer func() {
		sunMailRetryInitialDelay = oldInitialDelay
		sunMailRetryMaxDelay = oldMaxDelay
	}()

	attempts := 0
	body, err := retrySunMail(context.Background(), "test", func() ([]byte, error) {
		attempts++
		if attempts == 1 {
			return nil, context.DeadlineExceeded
		}
		if attempts < 3 {
			return nil, errors.New("temporary failure")
		}
		return []byte("ok"), nil
	})
	if err != nil {
		t.Fatalf("retrySunMail() error = %v", err)
	}
	if string(body) != "ok" {
		t.Fatalf("retrySunMail() body = %q, want ok", body)
	}
	if attempts != 3 {
		t.Fatalf("retrySunMail() attempts = %d, want 3", attempts)
	}
}

func TestExtractMailCodeSkipsManyMeColorValues(t *testing.T) {
	t.Parallel()

	content := `
		<td style="color:#404040;background-color:#202123">
			<p>输入此临时验证码以继续：</p>
			<p>944160</p>
		</td>
	`

	code := extractMailCode(content)
	if code != "944160" {
		t.Fatalf("extractMailCode() = %q, want %q", code, "944160")
	}
}

func TestExtractMailCodeNoCode(t *testing.T) {
	t.Parallel()

	code := extractMailCode(`<td style="color:#404040;background-color:#202123"></td>`)
	if code != "" {
		t.Fatalf("extractMailCode() = %q, want empty", code)
	}
}
