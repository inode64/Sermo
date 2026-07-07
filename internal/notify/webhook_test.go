package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostWebhookDeliversJSON(t *testing.T) {
	var gotBody []byte
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := sendWebhook(context.Background(), nil, TypeSlack, srv.URL, []byte(`{"text":"hi"}`)); err != nil {
		t.Fatalf("slack webhook: %v", err)
	}
	if gotContentType != "application/json" || string(gotBody) != `{"text":"hi"}` {
		t.Fatalf("got Content-Type %q body %q", gotContentType, gotBody)
	}
	if err := sendWebhook(context.Background(), nil, TypeTeams, srv.URL, []byte(`{}`)); err != nil {
		t.Fatalf("teams webhook: %v", err)
	}
}

func TestPostWebhookNon2xxCarriesLabelAndSnippet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("kaboom"))
	}))
	defer srv.Close()

	err := postWebhook(context.Background(), "teams", srv.URL, []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "teams webhook returned") || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("err = %v, want teams label and body snippet", err)
	}
}

func TestPostWebhookConnectionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := srv.URL
	srv.Close() // refuse the connection

	if err := postWebhook(context.Background(), "slack", url, []byte(`{}`)); err == nil {
		t.Fatal("expected a connection error")
	}
}

func TestEmailDSNAddr(t *testing.T) {
	if got := (emailDSN{host: "mail.example.com", port: "587"}).addr(); got != "mail.example.com:587" {
		t.Fatalf("addr = %q", got)
	}
}
