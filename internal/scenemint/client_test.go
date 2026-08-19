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
	_, err = client.GetByEmail(context.Background(), "account@example.com")
	if err == nil {
		t.Fatal("GetByEmail() returned nil error")
	}
	if time.Since(started) >= 100*time.Millisecond {
		t.Fatal("GetByEmail() did not respect the HTTP client timeout")
	}
}
