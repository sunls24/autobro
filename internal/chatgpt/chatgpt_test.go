package chatgpt

import (
	"codex-free/internal/browser"
	"codex-free/internal/mail"
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-rod/rod"
)

func TestFlow_RegisterOrLogin(t *testing.T) {
	bro, err := browser.NewDefault(false)
	if err != nil {
		t.Fatal(err)
	}
	flow := New(WithBro(bro), WithIMail(mail.NewSunMail(os.Getenv("SUNMAIL_API_KEY"))), WithBackground())
	err = rod.Try(func() {
		for i := 0; i < 100; i++ {
			t.Log(i)
			a := flow.MustRegisterOrLogin(context.Background(), nil)
			a = flow.MustRegisterOrLogin(context.Background(), a)
		}
	})
	if err != nil {
		t.Error(err)
		time.Sleep(time.Hour)
	}
}
