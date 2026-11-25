package scenemint

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	client.http.Timeout = 10 * time.Millisecond

	started := time.Now()
	_, err = client.CheckRelogAccounts(context.Background(), []string{"account@example.com"}, 1)
	if err == nil {
		t.Fatal("CheckRelogAccounts() returned nil error")
	}
	if time.Since(started) >= 100*time.Millisecond {
		t.Fatal("CheckRelogAccounts() did not respect the HTTP client timeout")
	}
}
