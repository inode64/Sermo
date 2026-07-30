package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sermo/internal/telegramapi"
	"strings"
	"sync/atomic"
	"testing"
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
