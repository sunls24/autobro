package toapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountsClient(t *testing.T) {
	t.Parallel()

	const auth = "test-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+auth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"items":[{"access_token":"old-token","status":"异常"}]}`))
		case http.MethodPost:
			_, _ = w.Write([]byte(`{"items":[{"access_token":"new-token","status":"正常"}]}`))
		case http.MethodDelete:
			var body struct {
				Tokens []string `json:"tokens"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Tokens) != 1 || body.Tokens[0] != "old-token" {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"removed":1,"items":[]}`))
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	w := &Worker{accountsURL: server.URL, accountsToken: auth}
	accounts, err := w.listRemoteAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if accounts["old-token"].Status != "异常" {
		t.Fatalf("remote status = %q, want 异常", accounts["old-token"].Status)
	}
	if err = w.uploadAccessToken(context.Background(), "new-token"); err != nil {
		t.Fatal(err)
	}
	if err = w.deleteAccessToken(context.Background(), "old-token"); err != nil {
		t.Fatal(err)
	}
}

func TestUploadAccessTokenRejectsAbnormalAccount(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"access_token":"new-token","status":"异常"}]}`))
	}))
	defer server.Close()

	w := &Worker{accountsURL: server.URL}
	err := w.uploadAccessToken(context.Background(), "new-token")
	if err == nil || !strings.Contains(err.Error(), "abnormal") {
		t.Fatalf("uploadAccessToken() error = %v, want abnormal account error", err)
	}
}
