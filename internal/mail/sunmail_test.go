package mail

import (
	"context"
	"strings"
	"testing"
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

func TestNormalizeSunMailDomainsSkipsEmptyValues(t *testing.T) {
	t.Parallel()

	domains := normalizeSunMailDomains([]string{"", " @first.example ", "  ", "second.example"})
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
