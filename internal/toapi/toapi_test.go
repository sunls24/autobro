package toapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
		account = &chatgpt.Account{}
	}
	account.Email = "factory@example.com"
	account.AccessToken = a.accessToken
	account.AuthStage = chatgpt.AuthStageTokenReady
	return account, nil
}

type stagedTestAuthenticator struct {
	mu        sync.Mutex
	calls     int
	emails    []string
	failFirst bool
}

func (a *stagedTestAuthenticator) RegisterOrLogin(_ context.Context, account *chatgpt.Account) (*chatgpt.Account, error) {
	if account == nil {
		account = &chatgpt.Account{}
	}
	a.mu.Lock()
	a.calls++
	a.emails = append(a.emails, account.Email)
	call := a.calls
	a.mu.Unlock()

	account.Email = "retry@example.com"
	account.ForwardMail = "forward@example.com"
	if a.failFirst && call == 1 {
		account.AuthStage = chatgpt.AuthStageAccountCreated
		return nil, context.DeadlineExceeded
	}
	account.AccessToken = "fixture-token"
	account.AuthStage = chatgpt.AuthStageTokenReady
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

func TestStartRetriesCreatedAccountWithoutCreatingAnotherAccount(t *testing.T) {
	t.Chdir(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/ops/accounts" {
			t.Errorf("unexpected SceneMint request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"message":"","data":{}}`)
	}))
	defer server.Close()

	sceneMint, err := scenemint.NewClient(server.URL, "fixture-key")
	if err != nil {
		t.Fatal(err)
	}

	oldRetryWait := registrationRetryWait
	registrationRetryWait = func(context.Context, int) error { return nil }
	t.Cleanup(func() { registrationRetryWait = oldRetryWait })

	staged := &stagedTestAuthenticator{failFirst: true}
	created := 0
	worker := New(func() (chatgpt.Authenticator, func() error, error) {
		created++
		return staged, func() error { return nil }, nil
	}, sceneMint, true)

	if err := worker.Start(context.Background(), 1); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	staged.mu.Lock()
	calls := staged.calls
	emails := append([]string(nil), staged.emails...)
	staged.mu.Unlock()
	if created != 2 || calls != 2 {
		t.Fatalf("authentication attempts = created %d, calls %d; want 2 and 2", created, calls)
	}
	if len(emails) != 2 || emails[0] != "" || emails[1] != "retry@example.com" {
		t.Fatalf("account emails = %v, want empty then retry@example.com", emails)
	}
	data, err := os.ReadFile(accountsFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "\n") != 1 {
		t.Fatalf("account saved more than once: %q", data)
	}

}

func TestStartRetriesFinalizationWithoutReauthenticating(t *testing.T) {
	uploads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		w.Header().Set("Content-Type", "application/json")
		if uploads == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"code":1,"message":"temporary","data":{}}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"message":"","data":{}}`)
	}))
	defer server.Close()

	sceneMint, err := scenemint.NewClient(server.URL, "fixture-key")
	if err != nil {
		t.Fatal(err)
	}

	oldRetryWait := registrationRetryWait
	registrationRetryWait = func(context.Context, int) error { return nil }
	t.Cleanup(func() { registrationRetryWait = oldRetryWait })

	created := 0
	worker := New(func() (chatgpt.Authenticator, func() error, error) {
		created++
		return &factoryTestAuthenticator{accessToken: "fixture-token"}, func() error { return nil }, nil
	}, sceneMint, false)

	if err := worker.Start(context.Background(), 1); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if created != 1 || uploads != 2 {
		t.Fatalf("finalization = authenticator sessions %d, uploads %d; want 1 and 2", created, uploads)
	}
}

func TestRetryableRegistrationErrorDoesNotRetryMailCodeTimeout(t *testing.T) {
	account := &chatgpt.Account{AuthStage: chatgpt.AuthStageAccountCreated}
	if retryableRegistrationError(account, chatgpt.ErrMailCodeTimeout) {
		t.Fatal("mail code timeout should not be retried")
	}
}

type registrationTestFunc func(context.Context, *chatgpt.Account) (*chatgpt.Account, error)

func (f registrationTestFunc) RegisterOrLogin(ctx context.Context, account *chatgpt.Account) (*chatgpt.Account, error) {
	return f(ctx, account)
}

func TestStartPreservesErrorWhenCanceledBetweenAttempts(t *testing.T) {
	for _, duringWait := range []bool{true, false} {
		name := "after wait"
		if duringWait {
			name = "during wait"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			oldWait := registrationRetryWait
			t.Cleanup(func() { registrationRetryWait = oldWait })
			registrationRetryWait = func(ctx context.Context, _ int) error {
				cancel()
				if duringWait {
					return ctx.Err()
				}
				return nil
			}
			calls := 0
			auth := registrationTestFunc(func(context.Context, *chatgpt.Account) (*chatgpt.Account, error) {
				calls++
				return nil, context.DeadlineExceeded
			})
			worker := New(func() (chatgpt.Authenticator, func() error, error) {
				return auth, func() error { return nil }, nil
			}, nil, false)
			err := worker.Start(ctx, 1)
			if !errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Start() error = %v, want cancellation and authentication error", err)
			}
			if calls != 1 {
				t.Fatalf("authentication calls = %d, want 1", calls)
			}
		})
	}
}

func TestStartPreservesCreatedAccountOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stage      chatgpt.AuthStage
		authErr    error
		save       bool
		cancel     bool
		failSave   bool
		wantCalls  int
		wantStored bool
	}{
		{name: "retries exhausted", stage: chatgpt.AuthStageAccountCreated, authErr: context.DeadlineExceeded, save: true, wantCalls: 3, wantStored: true},
		{name: "mail timeout", stage: chatgpt.AuthStageAccountCreated, authErr: chatgpt.ErrMailCodeTimeout, save: true, wantCalls: 1, wantStored: true},
		{name: "canceled", stage: chatgpt.AuthStageAccountCreated, authErr: context.Canceled, save: true, cancel: true, wantCalls: 1, wantStored: true},
		{name: "not created", authErr: context.DeadlineExceeded, save: true, wantCalls: 3},
		{name: "saving disabled", stage: chatgpt.AuthStageAccountCreated, authErr: context.DeadlineExceeded, wantCalls: 3},
		{name: "deactivated", stage: chatgpt.AuthStageAccountCreated, authErr: chatgpt.ErrAccountDeactivated, save: true, wantCalls: 1},
		{name: "save failure", stage: chatgpt.AuthStageAccountCreated, authErr: context.DeadlineExceeded, save: true, failSave: true, wantCalls: 3},
		{name: "save failure on cancellation", stage: chatgpt.AuthStageAccountCreated, authErr: context.Canceled, save: true, cancel: true, failSave: true, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			oldWait := registrationRetryWait
			registrationRetryWait = func(context.Context, int) error { return nil }
			t.Cleanup(func() { registrationRetryWait = oldWait })
			if tc.failSave {
				if err := os.Mkdir(accountsFile, 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			auth := registrationTestFunc(func(_ context.Context, account *chatgpt.Account) (*chatgpt.Account, error) {
				calls++
				account.Email = "recover@example.com"
				account.Password = "fixture-password"
				account.ForwardMail = "forward@example.com"
				account.MailProvider = "sl"
				account.ProviderAddressID = 7
				account.ProviderOwnerID = 11
				account.AuthStage = tc.stage
				if tc.cancel {
					cancel()
				}
				return nil, tc.authErr
			})
			worker := New(func() (chatgpt.Authenticator, func() error, error) {
				return auth, func() error { return nil }, nil
			}, nil, tc.save)
			err := worker.Start(ctx, 1)
			if !errors.Is(err, tc.authErr) {
				t.Fatalf("Start() error = %v, want %v", err, tc.authErr)
			}
			if calls != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", calls, tc.wantCalls)
			}
			if tc.failSave {
				if !strings.Contains(err.Error(), "保存本地账号失败") {
					t.Fatalf("missing save error: %v", err)
				}
				return
			}
			data, readErr := os.ReadFile(accountsFile)
			if !tc.wantStored {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf("unexpected account file: %q, error: %v", data, readErr)
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Count(string(data), "\n") != 1 {
				t.Fatalf("expected one stored record, got %q", data)
			}
			accounts, err := LoadStoredAccounts()
			if err != nil {
				t.Fatal(err)
			}
			if len(accounts) != 1 {
				t.Fatalf("stored accounts = %d, want 1", len(accounts))
			}
			a := accounts[0]
			if a.Email != "recover@example.com" || a.Password != "fixture-password" || a.ForwardMail != "forward@example.com" || a.MailProvider != "sl" || a.ProviderAddressID != 7 || a.ProviderOwnerID != 11 {
				t.Fatalf("incomplete recovery account: %+v", a)
			}
		})
	}
}
