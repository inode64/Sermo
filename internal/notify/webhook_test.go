package notify

import (
	"context"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestPushMessageFields(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want map[string]any
	}{
		{
			name: "subject only",
			msg:  Message{Subject: "recovered"},
			want: map[string]any{pushPayloadMessageKey: "recovered"},
		},
		{
			name: "subject and body",
			msg:  Message{Subject: "disk full", Body: "SERMO_PATH=/"},
			want: map[string]any{pushPayloadTitleKey: "disk full", pushPayloadMessageKey: "SERMO_PATH=/"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := pushMessageFields(test.msg); !maps.Equal(got, test.want) {
				t.Fatalf("pushMessageFields() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPostWebhookDeliversJSON(t *testing.T) {
	var gotBody []byte
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := sendWebhook(context.Background(), nil, TypeSlack, srv.URL, nil, []byte(`{"text":"hi"}`)); err != nil {
		t.Fatalf("slack webhook: %v", err)
	}
	if gotContentType != "application/json" || string(gotBody) != `{"text":"hi"}` {
		t.Fatalf("got Content-Type %q body %q", gotContentType, gotBody)
	}
	if err := sendWebhook(context.Background(), nil, TypeTeams, srv.URL, nil, []byte(`{}`)); err != nil {
		t.Fatalf("teams webhook: %v", err)
	}
}

func TestPostWebhookNon2xxCarriesLabelAndSnippet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("kaboom"))
	}))
	defer srv.Close()

	err := postWebhook(context.Background(), "teams", srv.URL, nil, []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "teams webhook returned") || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("err = %v, want teams label and body snippet", err)
	}
}

// A redirect is not a delivery: Go would replay the POST as a bodyless GET,
// carry the URL (a Telegram token) as Referer and a Gotify key header to the
// new host, and a 2xx there would count as sent.
func TestPostWebhookDoesNotFollowRedirects(t *testing.T) {
	followed := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed = true
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	for _, code := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, code)
		}))
		err := postWebhook(context.Background(), TypeGotify, srv.URL, map[string]string{"X-Gotify-Key": "secret"}, []byte(`{}`))
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), strconv.Itoa(code)) {
			t.Fatalf("redirect %d: err = %v, want a failed delivery naming the status", code, err)
		}
	}
	if followed {
		t.Fatal("a redirect was followed")
	}
}

func TestPostWebhookConnectionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := srv.URL
	srv.Close() // refuse the connection

	if err := postWebhook(context.Background(), "slack", url, nil, []byte(`{}`)); err == nil {
		t.Fatal("expected a connection error")
	}
}

func TestEmailDSNAddr(t *testing.T) {
	if got := (emailDSN{host: "mail.example.com", port: smtpDefaultPort}).addr(); got != "mail.example.com:587" {
		t.Fatalf("addr = %q", got)
	}
}

// A transport failure must never surface the request URL, since a Telegram
// bot URL carries the token. Only the underlying cause may appear.
func TestPostWebhookHidesURLOnTransportError(t *testing.T) {
	const secretURL = "https://127.0.0.1:1/bot123456:SECRETTOKEN/sendMessage"
	err := postWebhook(context.Background(), "telegram", secretURL, nil, []byte(`{}`))
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "SECRETTOKEN") || strings.Contains(err.Error(), secretURL) {
		t.Fatalf("error leaked the request URL/token: %v", err)
	}
}
