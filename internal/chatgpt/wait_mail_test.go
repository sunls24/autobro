package chatgpt

import (
	"autobro/internal/mail"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sunls24/gox"
)

type waitingMail struct{}

func (waitingMail) NewAddress(context.Context, string) (string, error) { return "", nil }
func (waitingMail) DelAddressByMetadata(context.Context, mail.AddressMetadata) error {
	return nil
}
func (waitingMail) ForgetAddress(string) {}
func (waitingMail) ForwardAddress(context.Context, string) (string, error) {
	return "", nil
}
func (waitingMail) Metadata(address string) mail.AddressMetadata {
	return mail.AddressMetadata{Email: address}
}
func (waitingMail) WaitMailCode(context.Context, string) <-chan gox.Result[string] {
	return make(chan gox.Result[string])
}

func TestWaitMailCodeTimeout(t *testing.T) {
	flow := New(WithIMail(waitingMail{}), WithMailCodeTimeout(10*time.Millisecond))
	err := flow.waitMailCode(context.Background(), "forward@example.com", func(string) {}, nil)
	if !errors.Is(err, ErrMailCodeTimeout) {
		t.Fatalf("waitMailCode() error = %v, want ErrMailCodeTimeout", err)
	}
}

func TestWaitMailCodeSunMailFailureIsNotTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "mail unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	sunMail := mail.NewSunMailWithConfig(mail.SunMailConfig{
		BaseURL:      server.URL,
		HTTPClient:   server.Client(),
		FailFastWait: true,
	})
	flow := New(WithIMail(mail.From(sunMail, sunMail)), WithMailCodeTimeout(2*time.Second))
	err := flow.waitMailCode(context.Background(), "forward@example.com", func(string) {}, nil)
	if err == nil || errors.Is(err, ErrMailCodeTimeout) {
		t.Fatalf("waitMailCode() error = %v, want SunMail failure", err)
	}
}

func TestWaitMailCodeResendsAtMostFiveTimes(t *testing.T) {
	flow := New(WithIMail(waitingMail{}), WithMailCodeInterval(time.Millisecond))
	resends := 0
	err := flow.waitMailCode(context.Background(), "forward@example.com", func(string) {}, func() {
		resends++
	})
	if !errors.Is(err, ErrMailCodeTimeout) {
		t.Fatalf("waitMailCode() error = %v, want ErrMailCodeTimeout", err)
	}
	if resends != 5 {
		t.Fatalf("resend count = %d, want 5", resends)
	}
}

func TestWaitMailCodeHonorsContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	flow := New(WithIMail(waitingMail{}), WithMailCodeInterval(time.Minute))
	err := flow.waitMailCode(ctx, "forward@example.com", func(string) {}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitMailCode() error = %v, want context.DeadlineExceeded", err)
	}
}
