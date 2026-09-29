package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sermo/internal/telegramapi"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeReporter returns canned data for command tests.
type fakeReporter struct {
	status   StatusReport
	services []ServiceLine
	watches  []WatchLine
	sla      []SLAWindow
	slaOK    bool
	events   []EventLine
	lastN    int
	err      error
}

func (f *fakeReporter) Status(context.Context) (StatusReport, error) { return f.status, f.err }
func (f *fakeReporter) Services(context.Context) ([]ServiceLine, error) {
	return f.services, f.err
}
func (f *fakeReporter) Watches(context.Context) ([]WatchLine, error) { return f.watches, f.err }
func (f *fakeReporter) SLA(_ context.Context, _ string) ([]SLAWindow, bool, error) {
	return f.sla, f.slaOK, f.err
}
func (f *fakeReporter) Events(_ context.Context, limit int) ([]EventLine, error) {
	f.lastN = limit
	return f.events, f.err
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestParseCommand(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantArgs []string
	}{
		{"/status", "/status", nil},
		{"  /Status  ", "/status", nil},
		{"/status@SermoBot", "/status", nil},
		{"/sla web", "/sla", []string{"web"}},
		{"status", "/status", nil},
		{"", "", nil},
		{"   ", "", nil},
	}
	for _, c := range cases {
		name, args := parseCommand(c.in)
		if name != c.wantName {
			t.Errorf("parseCommand(%q) name = %q, want %q", c.in, name, c.wantName)
		}
		if strings.Join(args, ",") != strings.Join(c.wantArgs, ",") {
			t.Errorf("parseCommand(%q) args = %v, want %v", c.in, args, c.wantArgs)
		}
	}
}

func TestDispatch(t *testing.T) {
	rep := &fakeReporter{
		status:   StatusReport{Host: "srv1", Services: 3, OK: 2, Failing: 1},
		services: []ServiceLine{{Name: "web", State: "running", Health: "ok", Monitored: true}},
		watches:  []WatchLine{{Name: "disk", Scope: "host", State: "ok", Monitored: true}},
		sla:      []SLAWindow{{Window: "day", Ratio: "99.9%"}},
		slaOK:    true,
		events:   []EventLine{{Time: "t", Kind: "firing", Message: "down"}},
	}
	b := &Bot{reporter: rep, log: discardLogger()}
	ctx := context.Background()

	cases := []struct {
		in      string
		wantSub string
	}{
		{"/status", "Sermo status — srv1"},
		{"/services", "web: running / ok"},
		{"/services web", "State: running"},
		{"/services ghost", `No service named "ghost"`},
		{"/watches", "disk (host): ok"},
		{"/sla web", "SLA — web"},
		{"/sla", "Usage: /sla <service>"},
		{"/events", "Recent events (1)"},
		{"/help", "read-only commands"},
		{"", "read-only commands"},
		{"/bogus", "Unknown command /bogus"},
		{"/status@SomeBot", "Sermo status"},
	}
	for _, c := range cases {
		got, err := b.dispatch(ctx, c.in)
		if err != nil {
			t.Fatalf("dispatch(%q): %v", c.in, err)
		}
		if !strings.Contains(got, c.wantSub) {
			t.Errorf("dispatch(%q) = %q, want substring %q", c.in, got, c.wantSub)
		}
	}
}

func TestDispatchEventsLimitCapped(t *testing.T) {
	rep := &fakeReporter{}
	b := &Bot{reporter: rep, log: discardLogger()}
	if _, err := b.dispatch(context.Background(), "/events 9999"); err != nil {
		t.Fatal(err)
	}
	if rep.lastN != EventsMaxLimit {
		t.Fatalf("events limit = %d, want cap %d", rep.lastN, EventsMaxLimit)
	}
}

func TestDispatchPropagatesReporterError(t *testing.T) {
	b := &Bot{reporter: &fakeReporter{err: errors.New("backend down")}, log: discardLogger()}
	if _, err := b.dispatch(context.Background(), "/status"); err == nil {
		t.Fatal("expected reporter error to propagate")
	}
}

func TestHandleUpdateAuthorization(t *testing.T) {
	var sends atomic.Int32
	var lastText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/"+telegramapi.MethodSendMessage) {
			sends.Add(1)
			var body struct {
				Text string `json:"text"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			lastText = body.Text
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	cfg := Config{Enabled: true, Token: "t", AllowedChats: []int64{42}}
	b := &Bot{reporter: &fakeReporter{status: StatusReport{Host: "h"}}, log: discardLogger()}
	cl := testClient(srv.URL, "t")
	ctx := context.Background()

	// Unauthorized chat: no reply is ever sent.
	b.handleUpdate(ctx, cfg, cl, update{UpdateID: 1, Message: &message{Chat: chat{ID: 99}, Text: "/status"}})
	if sends.Load() != 0 {
		t.Fatalf("unauthorized chat must not get a reply, got %d sends", sends.Load())
	}

	// Authorized chat: a reply is sent.
	b.handleUpdate(ctx, cfg, cl, update{UpdateID: 2, Message: &message{Chat: chat{ID: 42}, Text: "/status"}})
	if sends.Load() != 1 {
		t.Fatalf("authorized chat should get one reply, got %d sends", sends.Load())
	}
	if !strings.Contains(lastText, "Sermo status") {
		t.Fatalf("unexpected reply text: %q", lastText)
	}
}

func TestHandleUpdateSplitsLongReply(t *testing.T) {
	var texts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if telegramapi.TextLength(body.Text) > telegramapi.MaxTextLength {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"ok":false,"description":"Bad Request: message is too long"}`)
			return
		}
		texts = append(texts, body.Text)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	services := make([]ServiceLine, 400)
	for i := range services {
		services[i] = ServiceLine{Name: fmt.Sprintf("service-with-a-long-name-%03d", i), State: "running"}
	}
	cfg := Config{Enabled: true, Token: "t", AllowedChats: []int64{42}}
	b := &Bot{reporter: &fakeReporter{services: services}, log: discardLogger()}
	b.handleUpdate(context.Background(), cfg, testClient(srv.URL, "t"), update{UpdateID: 1, Message: &message{Chat: chat{ID: 42}, Text: "/services"}})
	if len(texts) < 2 {
		t.Fatalf("a reply over the limit must arrive in several messages, got %d", len(texts))
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "service-with-a-long-name-000") || !strings.Contains(joined, "service-with-a-long-name-399") {
		t.Fatal("split reply lost services")
	}
}

// fakeBotAPI is an in-memory Bot API: per token, a queue of updates that
// getUpdates serves from the requested offset, plus the replies sent. arrivals
// are queued only after the token's first getUpdates, so they model commands
// sent once the bot is polling.
type fakeBotAPI struct {
	mu       sync.Mutex
	queued   map[string][]update
	arrivals map[string][]update
	polled   map[string]bool
	replies  map[string][]string
}

func newFakeBotAPI() *fakeBotAPI {
	return &fakeBotAPI{queued: map[string][]update{}, arrivals: map[string][]update{}, polled: map[string]bool{}, replies: map[string][]string{}}
}

func (f *fakeBotAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/bot"), "/")
	token, method := parts[0], parts[1]
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case telegramapi.MethodGetUpdates:
		var body struct {
			Offset int64 `json:"offset"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var out []update
		for _, u := range f.queued[token] {
			if u.UpdateID >= body.Offset {
				out = append(out, u)
			}
		}
		if !f.polled[token] {
			f.polled[token] = true
			f.queued[token] = append(f.queued[token], f.arrivals[token]...)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": out})
	case telegramapi.MethodSendMessage:
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.replies[token] = append(f.replies[token], body.Text)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}
}

func (f *fakeBotAPI) repliesFor(token string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.replies[token]...)
}

func commandUpdate(id int64, text string) update {
	return update{UpdateID: id, Message: &message{Chat: chat{ID: 42}, Text: text}}
}

// waitForReply polls until token received a reply or the deadline passes.
func waitForReply(t *testing.T, api *fakeBotAPI, token string) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if replies := api.repliesFor(token); len(replies) > 0 {
			return replies
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no reply for token %s", token)
	return nil
}

// setBotConfig swaps config and client together, pointing the client at the
// fake API (UpdateConfig would build one against the real endpoint).
func setBotConfig(b *Bot, cfg Config, base string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cfg, b.client = cfg, testClient(base, cfg.Token)
}

func TestRunSkipsBacklogAfterTokenChange(t *testing.T) {
	api := newFakeBotAPI()
	api.queued["a"] = []update{commandUpdate(100, "/help")}
	// The new bot's update ids are lower than the old bot's offset. Its backlog
	// (5) predates the switch; 6 is sent afterwards and must be answered.
	api.queued["b"] = []update{commandUpdate(5, "/help")}
	api.arrivals["b"] = []update{commandUpdate(6, "/status")}
	srv := httptest.NewServer(api)
	defer srv.Close()

	b := &Bot{reporter: &fakeReporter{status: StatusReport{Host: "h"}}, log: discardLogger(), idle: time.Millisecond}
	cfg := Config{Enabled: true, Token: "a", AllowedChats: []int64{42}, PollInterval: time.Second}
	setBotConfig(b, cfg, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	time.Sleep(50 * time.Millisecond)
	cfg.Token = "b"
	setBotConfig(b, cfg, srv.URL)
	replies := waitForReply(t, api, "b")
	if len(replies) != 1 || !strings.Contains(replies[0], "Sermo status") {
		t.Fatalf("replies = %q, want only the /status sent after the switch", replies)
	}
	if got := api.repliesFor("a"); len(got) != 0 {
		t.Fatalf("old backlog replayed: %q", got)
	}
}

func TestRunSkipsBacklogWhenEnabledAfterStart(t *testing.T) {
	api := newFakeBotAPI()
	api.queued["a"] = []update{commandUpdate(7, "/help")}
	api.arrivals["a"] = []update{commandUpdate(8, "/status")}
	srv := httptest.NewServer(api)
	defer srv.Close()

	b := &Bot{reporter: &fakeReporter{status: StatusReport{Host: "h"}}, log: discardLogger(), idle: time.Millisecond}
	cfg := Config{Enabled: false, Token: "a", AllowedChats: []int64{42}, PollInterval: time.Second}
	setBotConfig(b, cfg, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	time.Sleep(20 * time.Millisecond)
	cfg.Enabled = true
	setBotConfig(b, cfg, srv.URL)
	replies := waitForReply(t, api, "a")
	if len(replies) != 1 || !strings.Contains(replies[0], "Sermo status") {
		t.Fatalf("replies = %q, want only the /status sent after enabling", replies)
	}
}
