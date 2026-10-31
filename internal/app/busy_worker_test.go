package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"sermo/internal/checks"
	"sermo/internal/conn"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/severity"
)

func busyPingCache() map[string]checks.Result {
	return map[string]checks.Result{"ping": {Check: "ping", Busy: true,
		Message: "mysql 127.0.0.1:3306: server busy: Too many connections (1040)",
		Data:    map[string]any{checks.DataKeyBusyReason: "Too many connections", conn.ExtraKeyErrorCode: "1040"}}}
}

// The on_change report of a container that just turned healthy fails its check
// on two consecutive cycles when a start is followed by the health transition.
// Neither is a fault, so a restart rule with a two-cycle window must not fire.
func TestAdvisoryChangeNeverRestarts(t *testing.T) {
	advisory := map[string]checks.Result{"http": {Check: "http", ActionBlocked: true, Message: "fingerprint changed (running/starting -> running/healthy)"}}
	h := &workerHarness{cache: advisory, opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(remediationTreeFor("restart-if-http-failed", "cycles", 2), rules.Policy{Cooldown: time.Minute}, nil)
	for range 3 {
		w.RunCycle(context.Background())
	}
	if len(h.ops) != 0 {
		t.Fatalf("an advisory change must never restart, ops = %v", h.ops)
	}
}

func withheldEvents(events []Event) int {
	n := 0
	for _, e := range events {
		if e.Kind == eventKindFiring && strings.HasSuffix(e.Message, busyWithheldSuffix) {
			n++
		}
	}
	return n
}

// A server that answers "too many connections" is never restarted. The check
// only a restart rule reads announces the busy episode itself, once, and its
// recovery; a server that then really dies is still restarted.
func TestBusyCheckNeverRestartsAndAnnouncesItself(t *testing.T) {
	h := &workerHarness{cache: busyPingCache(), opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(remediationTree("restart-if-ping-failed", "ping", "restart"), rules.Policy{Cooldown: time.Minute}, nil)
	for range 3 {
		w.RunCycle(context.Background())
	}
	if len(h.ops) != 0 {
		t.Fatalf("a busy server must never be restarted, ops = %v", h.ops)
	}
	if got := withheldEvents(h.events); got != 1 {
		t.Fatalf("want exactly one busy firing event, got %d: %+v", got, h.events)
	}
	if e, _ := h.eventOf(eventKindFiring); !strings.Contains(e.Message, "server busy: Too many connections (1040)") {
		t.Fatalf("the busy event must name the server's answer: %q", e.Message)
	}

	h.cache = map[string]checks.Result{"ping": {Check: "ping", OK: true, Message: "mysql ok"}}
	w.RunCycle(context.Background())
	if got := h.countEvents(eventKindRecovered); got != 1 {
		t.Fatalf("the busy episode must recover once, got %d: %+v", got, h.events)
	}

	h.cache = failedCache("ping")
	w.RunCycle(context.Background())
	if len(h.ops) != 1 || h.ops[0] != string(rules.ActionRestart) {
		t.Fatalf("a dead server must still be restarted, ops = %v", h.ops)
	}
	if got := withheldEvents(h.events); got != 1 {
		t.Fatalf("a real failure belongs to the rule, not to a new busy episode: %d", got)
	}
}

// Busy then dead: the restart counts only the dead cycles.
func TestBusyThenDeadCountsOnlyDeadCycles(t *testing.T) {
	tree := remediationTree("restart-if-ping-failed", "ping", "restart")
	tree["rules"].(map[string]any)["restart-if-ping-failed"].(map[string]any)["for"] = map[string]any{"cycles": 2}
	h := &workerHarness{cache: busyPingCache(), opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)
	w.RunCycle(context.Background())
	w.RunCycle(context.Background())
	h.cache = failedCache("ping")
	w.RunCycle(context.Background())
	if len(h.ops) != 0 {
		t.Fatalf("one dead cycle after busy ones must not complete for: 2, ops = %v", h.ops)
	}
	w.RunCycle(context.Background())
	if len(h.ops) != 1 {
		t.Fatalf("two dead cycles must restart, ops = %v", h.ops)
	}
}

// A check an alert rule reads is alarmed by that rule: no second, busy
// episode from the health reporter, and still no restart.
func TestBusyCheckReadByAlertRuleHasNoOwnEpisode(t *testing.T) {
	tree := remediationTree("restart-if-ping-failed", "ping", "restart")
	tree["rules"].(map[string]any)["alert-if-ping-failed"] = map[string]any{
		"type": "alert",
		"if":   map[string]any{"failed": map[string]any{"check": "ping"}},
		"then": map[string]any{"action": "alert", "message": "ping failed"},
	}
	h := &workerHarness{cache: busyPingCache(), opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)
	w.RunCycle(context.Background())
	if len(h.ops) != 0 {
		t.Fatalf("ops = %v", h.ops)
	}
	if got := withheldEvents(h.events); got != 0 {
		t.Fatalf("the alert rule owns the alarm, got %d busy episodes", got)
	}
}

// A busy episode restored from a snapshot is not announced again.
func TestRestoredBusyEpisodeIsNotReannounced(t *testing.T) {
	h := &workerHarness{cache: busyPingCache(), opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(remediationTree("restart-if-ping-failed", "ping", "restart"), rules.Policy{Cooldown: time.Minute}, nil)
	w.checkEpisodes = map[string]checkEpisode{"ping": {held: severity.Error, restored: true, busy: true}}
	w.RunCycle(context.Background())
	if got := withheldEvents(h.events); got != 0 || len(h.ops) != 0 {
		t.Fatalf("restored busy episode re-announced (%d) or restarted (%v)", got, h.ops)
	}
}

// A db_queries sample refused with "too many connections" says so, and its
// published result keeps the server's evidence.
func TestDBQueryWatchNamesBusyServer(t *testing.T) {
	refused := fmt.Errorf("processlist: %w", &mysql.MySQLError{Number: 1040, Message: "Too many connections"})
	if got := classifySampleFailure(refused).message; got != "server busy: Too many connections (1040)" {
		t.Fatalf("message = %q", got)
	}
	if got := classifySampleFailure(errors.New("dial unix: no such file")); got.message != "dial unix: no such file" || got.isBusy {
		t.Fatalf("a transport error keeps its own words: %+v", got)
	}
	var published checks.Result
	w := &dbQueryWatcher{name: "alert-if-query-long-running", cfg: checks.DBQueryConfig{Engine: "mysql"},
		publish: func(_, _ string, r checks.Result) { published = r }}
	w.publishUnavailable(classifySampleFailure(refused))
	if !published.Busy || !published.Unavailable || published.Data[conn.ExtraKeyErrorCode] != "1040" ||
		published.Message != "mysql: server busy: Too many connections (1040)" {
		t.Fatalf("published = %+v", published)
	}
}

// End to end: a real mysql check against a server that answers ERR 1040 never
// restarts it; once the port is closed (a dead server) the restart fires.
func TestBusyMySQLEndToEndNeverRestarts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	payload := append([]byte{0xff, 0x10, 0x04}, "Too many connections"...)
	reply := append([]byte{byte(len(payload)), 0, 0, 0}, payload...)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write(reply)
			_ = c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	built, warns := checks.Build(map[string]any{
		"ping": map[string]any{"type": "mysql", "host": "127.0.0.1", "port": port, "timeout": "2s"},
	}, checks.Deps{DefaultTimeout: 2 * time.Second})
	if len(warns) != 0 {
		t.Fatal(warns)
	}
	h := &workerHarness{opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(remediationTree("restart-if-ping-failed", "ping", "restart"), rules.Policy{Cooldown: time.Minute}, nil)
	w.Checks = func(ctx context.Context, _ checks.Deps) map[string]checks.Result {
		out := map[string]checks.Result{}
		for _, r := range checks.Run(ctx, built, 1) {
			out[r.Check] = r
		}
		return out
	}
	for range 3 {
		w.RunCycle(t.Context())
	}
	if len(h.ops) != 0 {
		t.Fatalf("a server answering 1040 must never be restarted, ops = %v", h.ops)
	}
	if got := withheldEvents(h.events); got != 1 {
		t.Fatalf("want one busy event, got %d: %+v", got, h.events)
	}
	_ = ln.Close()
	w.RunCycle(t.Context())
	if len(h.ops) != 1 || h.ops[0] != string(rules.ActionRestart) {
		t.Fatalf("a closed port must still restart, ops = %v", h.ops)
	}
}

// A host watch whose server answers it is busy reports the firing but runs no
// hook: a hook must never restart a saturated server.
func TestWatchWithholdsHookOnBusyServer(t *testing.T) {
	check := &scriptedCheck{results: []checks.Result{
		{Check: "mysql", Busy: true, Message: "mysql 127.0.0.1:3306: server busy: Too many connections (1040)"},
	}}
	var events []Event
	var hooks int
	w := &Watch{
		Name: "mysql-ping", CheckType: "mysql", Check: check, FireOnFail: true,
		Hook: HookSpec{Command: []string{"/usr/bin/systemctl", "restart", "mysql"}},
		Runner: HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error {
			hooks++
			return nil
		}),
		Emit: func(event Event) { events = append(events, event) },
	}
	w.RunCycle(context.Background())
	w.RunCycle(context.Background())
	if hooks != 0 {
		t.Fatalf("a busy server ran %d hooks, want none", hooks)
	}
	var fired, withheld bool
	for _, e := range events {
		fired = fired || e.Kind == eventKindFiring
		withheld = withheld || (e.Kind == eventKindSuppressed && e.Message == busyActionsWithheld)
	}
	if !fired || !withheld {
		t.Fatalf("events = %+v, want a firing and a withheld notice", events)
	}
}

// An open alert cannot be observed while the monitor's own connection is
// refused: it stays open, and only an observed false recovers it.
func TestOpenAlertHeldWhileUnobservable(t *testing.T) {
	tree := map[string]any{"rules": map[string]any{"conns": map[string]any{
		"type":  "alert",
		"if":    map[string]any{"active": map[string]any{"check": "conns"}},
		"clear": map[string]any{"cycles": 1},
		"then":  map[string]any{"action": "alert", "message": "connections near the limit"},
	}}}
	h := &workerHarness{cache: map[string]checks.Result{"conns": {Check: "conns", Condition: true, OK: true}}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)
	w.RunCycle(context.Background())
	if h.countEvents(eventKindAlert) == 0 && h.countEvents(eventKindFiring) == 0 {
		t.Fatalf("the alert must fire first: %+v", h.events)
	}
	h.cache = map[string]checks.Result{"conns": {Check: "conns", Condition: true, Busy: true, Unavailable: true,
		Message: "sql mariadb: server busy: Too many connections (1040)"}}
	w.RunCycle(context.Background())
	w.RunCycle(context.Background())
	if got := h.countEvents(eventKindRecovered); got != 0 {
		t.Fatalf("an unobservable cycle must not recover the alert: %+v", h.events)
	}
	h.cache = map[string]checks.Result{"conns": {Check: "conns", Condition: true, OK: false}}
	w.RunCycle(context.Background())
	if got := h.countEvents(eventKindRecovered); got != 1 {
		t.Fatalf("an observed false must recover it once, got %d: %+v", got, h.events)
	}
}

// Busy right after Sermo's own operation is the service settling: the notice
// waits for the cooldown, and comes if the server is still busy after it.
func TestBusyEpisodeWaitsAfterOwnOperation(t *testing.T) {
	h := &workerHarness{cache: busyPingCache(), opResult: operation.Result{Status: operation.ResultOK}}
	state := &rules.RemediationState{LastActionAt: t0.Add(-time.Minute)}
	w := h.worker(remediationTree("restart-if-ping-failed", "ping", "restart"), rules.Policy{Cooldown: 15 * time.Minute}, state)
	w.RunCycle(context.Background())
	if got := withheldEvents(h.events); got != 0 {
		t.Fatalf("busy within the cooldown after an operation must wait, got %d: %+v", got, h.events)
	}
	state.LastActionAt = t0.Add(-time.Hour)
	w.RunCycle(context.Background())
	if got := withheldEvents(h.events); got != 1 {
		t.Fatalf("still busy after the cooldown must be announced once, got %d", got)
	}
}
