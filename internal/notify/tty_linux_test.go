//go:build linux

package notify

import (
	"context"
	"errors"
	"sermo/internal/strutil"
	"strings"
	"testing"
	"time"
)

func TestTTYNotifierTargetsFilterUsersAndUnsafeLines(t *testing.T) {
	n := &ttyNotifier{users: strutil.Set([]string{"root"}), devRoot: "/dev"}
	got := n.targetTTYs([]ttySession{
		{User: "root", Line: "pts/0"},
		{User: "root", Line: "../pts/1"},
		{User: "fran", Line: "pts/2"},
		{User: "root", Line: "pts/0"},
	})
	if len(got) != 1 || got[0] != "/dev/pts/0" {
		t.Fatalf("targetTTYs = %v, want [/dev/pts/0]", got)
	}
}

func TestWallNotifierTargetsAllUsers(t *testing.T) {
	n, err := buildWall("wall", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	wn := n.(*ttyNotifier)
	got := wn.targetTTYs([]ttySession{
		{User: "root", Line: "pts/0"},
		{User: "fran", Line: "pts/1"},
	})
	if len(got) != 2 || got[0] != "/dev/pts/0" || got[1] != "/dev/pts/1" {
		t.Fatalf("wall targetTTYs = %v, want every active terminal", got)
	}
	if wn.Type() != "wall" {
		t.Fatalf("wall type = %q", wn.Type())
	}
}

func TestTTYNotifierSendToTargetsWritesEachTarget(t *testing.T) {
	var paths []string
	n := &ttyNotifier{
		name: "tty",
		writeTTY: func(_ context.Context, path string, payload []byte) error {
			paths = append(paths, path)
			if !strings.Contains(string(payload), "Subject") || strings.Contains(string(payload), "\x1b") {
				t.Fatalf("payload was not rendered/sanitized: %q", string(payload))
			}
			return nil
		},
		hostname: func() (string, error) { return "host\x1b[31m", nil },
		now:      func() time.Time { return time.Unix(0, 0).UTC() },
	}
	if err := n.sendToTargets(context.Background(), []string{"/dev/pts/0", "/dev/tty1"}, Message{Subject: "Subject"}); err != nil {
		t.Fatalf("sendToTargets error = %v", err)
	}
	if len(paths) != 2 || paths[0] != "/dev/pts/0" || paths[1] != "/dev/tty1" {
		t.Fatalf("write paths = %v", paths)
	}
}

func TestTTYPayloadSanitizesControlSequences(t *testing.T) {
	payload := string(ttyPayload(Message{Subject: "bad\x1bsubject", Body: "line\x00two"}, "host", time.Unix(0, 0).UTC()))
	if strings.Contains(payload, "\x1b") || strings.Contains(payload, "\x00") {
		t.Fatalf("payload contains unsafe control sequence: %q", payload)
	}
	if !strings.Contains(payload, "bad?subject") || !strings.Contains(payload, "line?two") {
		t.Fatalf("payload did not preserve sanitized text: %q", payload)
	}
}

func TestTTYNotifierPartialFailureReportsError(t *testing.T) {
	n := &ttyNotifier{
		name: "tty",
		writeTTY: func(_ context.Context, path string, _ []byte) error {
			if path == "/dev/pts/1" {
				return errors.New("denied")
			}
			return nil
		},
		hostname: func() (string, error) { return "host", nil },
		now:      func() time.Time { return time.Unix(0, 0).UTC() },
	}
	targets := n.targetTTYs([]ttySession{{User: "root", Line: "pts/0"}, {User: "root", Line: "pts/1"}})
	if len(targets) != 2 {
		t.Fatalf("targets = %v", targets)
	}
	err := n.sendToTargets(context.Background(), targets, Message{Subject: "s"})
	if err == nil || !strings.Contains(err.Error(), "delivered to 1 terminal") {
		t.Fatalf("partial failure error = %v", err)
	}
}
