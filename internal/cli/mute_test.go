package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sermoapp "sermo/internal/app"
	"sermo/internal/state"
)

func TestMuteUnmuteCommand(t *testing.T) {
	root, global := writeCatalogServiceConfig(t)
	catalogDir := filepath.Join(root, "catalog")
	stateDir := filepath.Join(root, "state")
	run := func(args ...string) (int, string) {
		var out, errOut bytes.Buffer
		app := hermeticApp(t, App{Env: func(string) string { return "" }, Stdout: &out, Stderr: &errOut, LoadConfig: testLoadConfigWithCatalog(catalogDir)})
		code := app.Run(context.Background(), append([]string{"--config", global}, args...))
		return code, out.String() + errOut.String()
	}
	mute := func(key string) (state.NotificationMuteRecord, bool) {
		store, err := state.OpenContextWith(context.Background(), filepath.Join(stateDir, state.Filename), state.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		rec, found, err := store.NotificationMute(key)
		if err != nil {
			t.Fatal(err)
		}
		return rec, found
	}

	before := time.Now()
	if code, out := run("mute", "web"); code != exitSuccess || !strings.Contains(out, "web: notifications muted until ") {
		t.Fatalf("mute exit = %d out=%q", code, out)
	}
	rec, found := mute("web")
	if !found || rec.Source != state.SourceCLI || rec.Until.Before(before.Add(sermoapp.DefaultNotificationMute-time.Second)) || rec.Until.After(time.Now().Add(sermoapp.DefaultNotificationMute)) {
		t.Fatalf("default mute = %+v found=%v, want about %s from now", rec, found, sermoapp.DefaultNotificationMute)
	}

	code, out := run("--json", "mute", "web", "--for", "4h")
	var payload map[string]any
	if code != exitSuccess || json.Unmarshal([]byte(out), &payload) != nil || payload["notifications"] != muteStatusMuted || payload["service"] != "web" || payload["muted_until"] == nil {
		t.Fatalf("json mute exit = %d out=%q", code, out)
	}
	if rec, _ := mute("web"); rec.Until.Before(before.Add(4*time.Hour - time.Second)) {
		t.Fatalf("a second mute must replace the first: %+v", rec)
	}

	for _, args := range [][]string{
		{"mute", "web", "--for", "30s"},
		{"mute", "web", "--for", "25h"},
		{"unmute", "web", "--for", "1h"},
		{"mute", "ghost"},
	} {
		if code, out := run(args...); code == exitSuccess {
			t.Errorf("%v must fail, got exit 0: %q", args, out)
		}
	}

	if code, out := run("unmute", "web"); code != exitSuccess || !strings.Contains(out, "web: notifications unmuted") {
		t.Fatalf("unmute exit = %d out=%q", code, out)
	}
	if _, found := mute("web"); found {
		t.Fatal("unmute must remove the mute")
	}
	if code, out := run("unmute", "web"); code != exitSuccess || !strings.Contains(out, "notifications were not muted") {
		t.Fatalf("repeat unmute exit = %d out=%q", code, out)
	}
}

func TestWatchMuteUnmuteCommand(t *testing.T) {
	root, global := writeCatalogServiceConfig(t)
	servicesDir := filepath.Join(root, "services")
	stateDir := filepath.Join(root, "state")
	if err := os.WriteFile(filepath.Join(servicesDir, "mail-queue.yml"), []byte(
		"name: mail-queue\nservice: mail-queue\npolicy: { cooldown: 5m }\n"+
			"watches:\n  deferred-backlog:\n    check: { type: count, path: /tmp, of: file, count: { op: \">=\", value: 0 } }\n"+
			"    then: { hook: { command: [\"/bin/true\"] } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) int {
		app := hermeticApp(t, App{Env: func(string) string { return "" }, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, LoadConfig: testLoadConfigWithCatalog(filepath.Join(root, "catalog"))})
		return app.Run(context.Background(), append([]string{"--config", global}, args...))
	}
	const name = "mail-queue:deferred-backlog"
	if code := run("watch", "mute", name, "--for", "15m"); code != exitSuccess {
		t.Fatalf("watch mute exit = %d", code)
	}
	store, err := state.OpenContextWith(context.Background(), filepath.Join(stateDir, state.Filename), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, watchMuted, _ := store.NotificationMute(sermoapp.WatchMonitorKey(name))
	_, serviceMuted, _ := store.NotificationMute("mail-queue")
	store.Close()
	if !watchMuted || serviceMuted {
		t.Fatalf("watch mute wrote watch=%v service=%v, want only the watch", watchMuted, serviceMuted)
	}
	if code := run("watch", "unmute", name); code != exitSuccess {
		t.Fatalf("watch unmute exit = %d", code)
	}
	if code := run("watch", "mute", "mail-queue:ghost"); code == exitSuccess {
		t.Error("muting an unknown watch must fail")
	}
}

func TestStatusShowsNotificationMute(t *testing.T) {
	root, global := writeCatalogServiceConfig(t)
	app := monitorTestApp(t, root, nil)
	if code := app.Run(context.Background(), []string{"--config", global, "mute", "web", "--for", "2h"}); code != exitSuccess {
		t.Fatalf("mute exit = %d", code)
	}

	var out bytes.Buffer
	app.Stdout = &out
	if code := app.Run(context.Background(), []string{"--config", global, "status", "web"}); code != exitSuccess {
		t.Fatalf("status exit = %d", code)
	}
	if text := out.String(); !strings.Contains(text, "  notifications muted until ") || !strings.Contains(text, "source="+state.SourceCLI) {
		t.Fatalf("status output = %q", text)
	}

	out.Reset()
	if code := app.Run(context.Background(), []string{"--config", global, "--json", "status", "web"}); code != exitSuccess {
		t.Fatalf("status json exit = %d", code)
	}
	var st statusJSON
	if err := json.Unmarshal(out.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	until, err := time.Parse(time.RFC3339, st.NotifyMutedUntil)
	if err != nil || st.NotifyMuteSource != state.SourceCLI || until.Before(time.Now().Add(time.Hour)) || st.Paused {
		t.Fatalf("status json = %+v", st)
	}
}
