package toapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"autobro/internal/chatgpt"
	"autobro/internal/scenemint"
)

type factoryTestAuthenticator struct {
	accessToken string
}

func (a *factoryTestAuthenticator) RegisterOrLogin(_ context.Context, account *chatgpt.Account) (*chatgpt.Account, error) {
	if account == nil {
		return &chatgpt.Account{
			Email:       "factory@example.com",
			AccessToken: a.accessToken,
		}, nil
	}
	account.AccessToken = a.accessToken
	return account, nil
}

func TestRunWithAuthenticatorClosesSessionOnError(t *testing.T) {
	var closed int
	worker := &Worker{
		authenticatorFactory: func() (chatgpt.Authenticator, func() error, error) {
			return &factoryTestAuthenticator{}, func() error {
				closed++
				return nil
			}, nil
		},
	}
	wantErr := errors.New("authentication failed")
	if err := worker.runWithAuthenticator(func(chatgpt.Authenticator) error {
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("runWithAuthenticator() error = %v, want %v", err, wantErr)
	}
	if closed != 1 {
		t.Fatalf("close calls = %d, want 1", closed)
	}
}

func TestRunWithAuthenticatorDoesNotFailOnCloseError(t *testing.T) {
	worker := &Worker{
		authenticatorFactory: func() (chatgpt.Authenticator, func() error, error) {
			return &factoryTestAuthenticator{}, func() error {
				return errors.New("browser close failed")
			}, nil
		},
	}

	if err := worker.runWithAuthenticator(func(chatgpt.Authenticator) error {
		return nil
	}); err != nil {
		t.Fatalf("runWithAuthenticator() error = %v, want nil", err)
	}
}

func TestStartCreatesOneAuthenticatorSessionPerAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/ops/accounts" {
			t.Errorf("unexpected SceneMint request: %s %s", r.Method, r.URL.Path)
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read SceneMint request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"message":"","data":{}}`)
	}))
	defer server.Close()

	sceneMint, err := scenemint.NewClient(server.URL, "fixture-key")
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	created := 0
	closed := 0
	worker := New(func() (chatgpt.Authenticator, func() error, error) {
		mu.Lock()
		created++
		mu.Unlock()
		return &factoryTestAuthenticator{accessToken: "fixture-token"}, func() error {
			mu.Lock()
			closed++
			mu.Unlock()
			return nil
		}, nil
	}, sceneMint, false)

	if err = worker.Start(context.Background(), 2); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if created != 2 || closed != 2 {
		t.Fatalf("sessions = created %d, closed %d; want 2 and 2", created, closed)
	}
}
