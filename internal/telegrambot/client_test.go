package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sermo/internal/telegramapi"
	"strings"
	"testing"
	"time"
)

// doerFunc adapts a function to httpDoer.
type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

// testClient points a client at a test server base and token.
func testClient(base, token string) *client {
	c := newClient(token, time.Second)
	c.base = base + "/bot"
	return c
}

func TestGetUpdatesParsesMessages(t *testing.T) {
	const token = "123:abc"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/bot"+token+"/"+telegramapi.MethodGetUpdates) {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		var body struct {
			Offset         int64    `json:"offset"`
			Timeout        int      `json:"timeout"`
			AllowedUpdates []string `json:"allowed_updates"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Offset != 7 || body.Timeout != 30 || len(body.AllowedUpdates) != 1 || body.AllowedUpdates[0] != "message" {
			t.Errorf("unexpected getUpdates request: %+v", body)
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":[{"update_id":9,"message":{"message_id":1,"chat":{"id":42,"type":"private"},"text":"/status"}}]}`)
	}))
	defer srv.Close()

	updates, err := testClient(srv.URL, token).getUpdates(context.Background(), 7, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0].UpdateID != 9 || updates[0].Message == nil ||
		updates[0].Message.Chat.ID != 42 || updates[0].Message.Text != "/status" {
		t.Fatalf("unexpected updates: %+v", updates)
	}
}

func TestSendMessagePostsChatAndText(t *testing.T) {
	var got struct {
		ChatID          int64  `json:"chat_id"`
		Text            string `json:"text"`
		MessageThreadID int    `json:"message_thread_id"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/"+telegramapi.MethodSendMessage) {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	if err := testClient(srv.URL, "t").sendMessage(context.Background(), 42, 5, "hello"); err != nil {
		t.Fatal(err)
	}
	if got.ChatID != 42 || got.Text != "hello" || got.MessageThreadID != 5 {
		t.Fatalf("unexpected sendMessage body: %+v", got)
	}
}

func TestSendMessageOmitsThreadWhenZero(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	if err := testClient(srv.URL, "t").sendMessage(context.Background(), 42, 0, "hi"); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["message_thread_id"]; ok {
		t.Fatalf("thread id 0 should be omitted, got %v", raw)
	}
}

func TestCallReportsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"ok":false,"description":"chat not found"}`)
	}))
	defer srv.Close()

	err := testClient(srv.URL, "t").sendMessage(context.Background(), 1, 0, "x")
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("want API error surfaced, got %v", err)
	}
}

func TestCallScrubsTokenFromTransportError(t *testing.T) {
	const token = "SUPERSECRETTOKEN"
	c := newClient(token, time.Second)
	c.http = doerFunc(func(r *http.Request) (*http.Response, error) {
		// A real transport error is a *url.Error whose text embeds the full
		// request URL — for Telegram that URL carries the bot token.
		return nil, &url.Error{Op: "Post", URL: r.URL.String(), Err: errors.New("dial tcp: refused")}
	})
	err := c.call(context.Background(), telegramapi.MethodGetUpdates, map[string]any{}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("token leaked into error: %q", err.Error())
	}
}
