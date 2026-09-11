package mail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestSimpleLoginForwardAddressUsesAliasOwner(t *testing.T) {
	sl := &simpleLogin{
		accounts: []account{
			{forward: "owner@example.com"},
			{forward: "current@example.com"},
		},
		currentIndex: 1,
		aliases: map[string]aliasRecord{
			"alias@example.com": {id: 42, accountIndex: 0},
		},
	}

	got, err := sl.ForwardAddress(context.Background(), "alias@example.com")
	if err != nil {
		t.Fatalf("ForwardAddress() error = %v", err)
	}
	if got != "owner@example.com" {
		t.Fatalf("ForwardAddress() = %q, want owner@example.com", got)
	}
}

func TestSimpleLoginForgetAddressReleasesRecord(t *testing.T) {
	sl := &simpleLogin{
		aliases: map[string]aliasRecord{
			"alias@example.com": {id: 42, accountIndex: 0},
		},
	}

	sl.ForgetAddress("alias@example.com")
	if _, ok := sl.aliases["alias@example.com"]; ok {
		t.Fatal("forgotten alias remains tracked")
	}
}

func TestSimpleLoginCountsAliasesAndRotatesAfterFour(t *testing.T) {
	var gotCreateAuth string
	var gotCreateNote string
	aliasListRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		auth := r.Header.Get("Authentication")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/mailboxes":
			switch auth {
			case "first-key":
				_, _ = w.Write([]byte(`{"mailboxes":[{"id":11,"email":"first@example.com","verified":true}]}`))
			case "second-key":
				_, _ = w.Write([]byte(`{"mailboxes":[{"id":22,"email":"second@example.com","verified":true}]}`))
			default:
				http.Error(w, "unexpected key", http.StatusUnauthorized)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/v2/aliases":
			aliasListRequests++
			if r.URL.Query().Get("page_id") != "0" {
				http.Error(w, "unexpected page", http.StatusBadRequest)
				return
			}
			if auth == "first-key" {
				_, _ = w.Write([]byte(`{"aliases":[{"id":1,"email":"first-1@example.com","note":"autobro:chatgpt:v1"},{"id":2,"email":"first-2@example.com","note":"autobro:chatgpt:v1"},{"id":3,"email":"first-3@example.com","note":"autobro:chatgpt:v1"},{"id":4,"email":"first-4@example.com","note":"autobro:chatgpt:v1"},{"id":5,"email":"first-5@example.com","note":"autobro:chatgpt:v1"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"aliases":[{"id":10,"email":"second-used@example.com","note":"autobro:chatgpt:v1"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v5/alias/options":
			gotCreateAuth = auth
			_, _ = w.Write([]byte(`{"suffixes":[{"signed_suffix":"suffix"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v3/alias/custom/new":
			gotCreateAuth = auth
			var payload struct {
				Note string `json:"note"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			gotCreateNote = payload.Note
			_, _ = w.Write([]byte(`{"id":6,"email":"created@example.com"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldBase := simpleAPIBase
	simpleAPIBase = server.URL
	defer func() { simpleAPIBase = oldBase }()

	addressProvider, err := NewSimpleLogin(context.Background(), []string{"first-key", "second-key"})
	if err != nil {
		t.Fatalf("NewSimpleLogin() error = %v", err)
	}
	sl := addressProvider.(*simpleLogin)

	address, err := sl.NewAddress(context.Background(), "ignored")
	if err != nil {
		t.Fatalf("NewAddress() error = %v", err)
	}
	if address != "created@example.com" {
		t.Fatalf("NewAddress() = %q, want created@example.com", address)
	}
	if sl.currentIndex != 1 {
		t.Fatalf("currentIndex = %d, want 1", sl.currentIndex)
	}
	if got, want := sl.aliasCounts, []int{5, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("aliasCounts = %v, want %v", got, want)
	}
	if aliasListRequests != 2 {
		t.Fatalf("alias list requests during first allocation = %d, want 2", aliasListRequests)
	}
	if gotCreateAuth != "second-key" {
		t.Fatalf("create Authentication = %q, want second-key", gotCreateAuth)
	}
	if gotCreateNote != simpleLoginRegistrationNote {
		t.Fatalf("create note = %q, want %q", gotCreateNote, simpleLoginRegistrationNote)
	}
}

func TestSimpleLoginReusesUnmarkedAliasAndDoesNotDeleteIt(t *testing.T) {
	var gotPatchAuth string
	var gotPatchNote string
	deleteCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/mailboxes":
			_, _ = w.Write([]byte(`{"mailboxes":[{"id":11,"email":"first@example.com","verified":true}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v2/aliases":
			_, _ = w.Write([]byte(`{"aliases":[{"id":7,"email":"existing@example.com","note":"This is your first alias."},{"id":8,"email":"used-1@example.com","note":"autobro:chatgpt:v1"},{"id":9,"email":"used-2@example.com","note":"autobro:chatgpt:v1"},{"id":10,"email":"used-3@example.com","note":"autobro:chatgpt:v1"}]}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/aliases/7":
			gotPatchAuth = r.Header.Get("Authentication")
			var payload struct {
				Note string `json:"note"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			gotPatchNote = payload.Note
			_, _ = w.Write([]byte(`{"note":"autobro:chatgpt:v1"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/aliases/7":
			deleteCalled = true
			http.Error(w, "reused alias must not be deleted", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldBase := simpleAPIBase
	simpleAPIBase = server.URL
	defer func() { simpleAPIBase = oldBase }()

	addressProvider, err := NewSimpleLogin(context.Background(), []string{"first-key"})
	if err != nil {
		t.Fatalf("NewSimpleLogin() error = %v", err)
	}
	sl := addressProvider.(*simpleLogin)

	address, err := sl.NewAddress(context.Background(), "ignored")
	if err != nil {
		t.Fatalf("NewAddress() error = %v", err)
	}
	if address != "existing@example.com" {
		t.Fatalf("NewAddress() = %q, want existing@example.com", address)
	}
	if gotPatchAuth != "first-key" {
		t.Fatalf("patch Authentication = %q, want first-key", gotPatchAuth)
	}
	if gotPatchNote != simpleLoginRegistrationNote {
		t.Fatalf("patch note = %q, want %q", gotPatchNote, simpleLoginRegistrationNote)
	}
	metadata := sl.Metadata(address)
	if metadata.AddressID != 7 || metadata.OwnerID != 11 {
		t.Fatalf("Metadata() = %#v, want id 7 and owner 11", metadata)
	}
	if err := sl.DelAddressByMetadata(context.Background(), metadata); err != nil {
		t.Fatalf("DelAddressByMetadata() error = %v", err)
	}
	if deleteCalled {
		t.Fatal("reused alias was deleted")
	}
	if got, want := sl.aliasCounts, []int{4}; !reflect.DeepEqual(got, want) {
		t.Fatalf("aliasCounts = %v, want %v", got, want)
	}
}

func TestSimpleLoginDeletesAliasAndReleasesCount(t *testing.T) {
	var gotDeleteAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/aliases/7" {
			http.NotFound(w, r)
			return
		}
		gotDeleteAuth = r.Header.Get("Authentication")
		_, _ = w.Write([]byte(`{"deleted":true}`))
	}))
	defer server.Close()

	oldBase := simpleAPIBase
	simpleAPIBase = server.URL
	defer func() { simpleAPIBase = oldBase }()

	sl := &simpleLogin{
		accounts: []account{
			{mailboxId: 11, apiKey: "first-key", forward: "first@example.com"},
			{mailboxId: 22, apiKey: "owner-key", forward: "owner@example.com"},
		},
		aliases:     map[string]aliasRecord{"created@example.com": {id: 7, accountIndex: 1, created: true}},
		aliasCounts: []int{0, 4},
	}
	metadata := AddressMetadata{
		Email:     "created@example.com",
		Provider:  AddressProviderSimpleLogin,
		AddressID: 7,
		OwnerID:   22,
	}

	if err := sl.DelAddressByMetadata(context.Background(), metadata); err != nil {
		t.Fatalf("DelAddressByMetadata() error = %v", err)
	}
	if gotDeleteAuth != "owner-key" {
		t.Fatalf("delete Authentication = %q, want owner-key", gotDeleteAuth)
	}
	if got, want := sl.aliasCounts, []int{0, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("aliasCounts = %v, want %v", got, want)
	}
}
