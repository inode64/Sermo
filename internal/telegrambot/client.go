package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"sermo/internal/httpx"
	"sermo/internal/netutil"
)

const (
	telegramAPIBase           = "https://api.telegram.org/bot"
	telegramGetUpdatesMethod  = "getUpdates"
	telegramSendMessageMethod = "sendMessage"
	// pollClientMargin extends the HTTP timeout past the long-poll timeout so a
	// legitimately held-open getUpdates is not cut off by the client.
	pollClientMargin = 10 * time.Second
	// errorBodySnippetLimit bounds the API error body captured into an error.
	errorBodySnippetLimit  = 256
	httpStatusClassDivisor = 100
	httpStatusClassSuccess = 2
)

// httpDoer performs an HTTP request; *http.Client satisfies it. Injected so
// tests exercise the client without real network I/O.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// client talks to the Telegram Bot API for one bot token. The token lives only
// inside the request URL and is scrubbed from any surfaced error.
type client struct {
	base  string // API base, up to but excluding the token; overridable in tests
	token string
	http  httpDoer
}

func newClient(token string, timeout time.Duration) *client {
	return &client{
		base:  telegramAPIBase,
		token: token,
		http:  &http.Client{Timeout: timeout, Transport: httpx.CloneDefaultTransport()},
	}
}

func (c *client) methodURL(method string) string {
	return c.base + c.token + "/" + method
}

// update is one getUpdates result item; only message updates are requested.
// Only the fields the bot acts on are decoded.
type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message"`
}

type message struct {
	Chat            chat   `json:"chat"`
	Text            string `json:"text"`
	MessageThreadID int    `json:"message_thread_id"`
}

type chat struct {
	ID int64 `json:"id"`
}

// getUpdates long-polls for updates newer than offset, holding the connection
// up to timeout. Only message updates are requested.
func (c *client) getUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]update, error) {
	body := map[string]any{
		"offset":          offset,
		"timeout":         int(timeout / time.Second),
		"allowed_updates": []string{"message"},
	}
	var resp struct {
		OK          bool     `json:"ok"`
		Result      []update `json:"result"`
		Description string   `json:"description"`
	}
	if err := c.call(ctx, telegramGetUpdatesMethod, body, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("telegram getUpdates: %s", resp.Description)
	}
	return resp.Result, nil
}

// sendMessage posts a plain-text reply to chatID, optionally within a forum
// topic thread (threadID 0 means the chat's main timeline).
func (c *client) sendMessage(ctx context.Context, chatID int64, threadID int, text string) error {
	body := map[string]any{"chat_id": chatID, "text": text}
	if threadID != 0 {
		body["message_thread_id"] = threadID
	}
	var resp struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := c.call(ctx, telegramSendMessageMethod, body, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("telegram sendMessage: %s", resp.Description)
	}
	return nil
}

// call POSTs a JSON body to a Bot API method and decodes the JSON response.
// The bot token is embedded in the URL, so every error is scrubbed of the URL
// via netutil.URLErrorCause before it is returned — no credential ever reaches
// a log line.
func (c *client) call(ctx context.Context, method string, body map[string]any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(method), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build %s request: %w", method, netutil.URLErrorCause(err))
	}
	req.Header.Set(httpx.HeaderContentType, httpx.ContentTypeJSON)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s request: %w", method, netutil.URLErrorCause(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode/httpStatusClassDivisor != httpStatusClassSuccess {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodySnippetLimit))
		return fmt.Errorf("telegram %s returned %s: %s", method, resp.Status, strings.TrimSpace(string(snippet)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s response: %w", method, err)
	}
	return nil
}
