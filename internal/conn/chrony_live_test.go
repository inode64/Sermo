package conn

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestChronyMakeStepLive exercises MakeStep against a real chronyd over its Unix
// command socket. The fake in chrony_test.go proves the wire format we send; only
// a real daemon proves the daemon accepts it.
//
// Opt-in, because it commands whatever daemon the socket belongs to:
//
//	SERMO_CHRONY_SOCKET=/tmp/sermo-chrony-live/chronyd.sock go test ./internal/conn -run MakeStepLive -v
//
// Point it at a private chronyd started with -x (no clock control), never at a
// production daemon and never on a ceph node, where a step can cost a monitor
// its quorum.
func TestChronyMakeStepLive(t *testing.T) {
	socket := os.Getenv("SERMO_CHRONY_SOCKET")
	if socket == "" {
		t.Skip("set SERMO_CHRONY_SOCKET to a private chronyd command socket")
	}
	// Sanitize before it reaches the dialer: the client socket path is derived
	// from this one, so an unchecked environment value is a taint source that
	// gosec follows straight into os.Remove/os.Chmod on the production path.
	socket = filepath.Clean(socket)
	if !filepath.IsAbs(socket) {
		t.Fatalf("SERMO_CHRONY_SOCKET must be an absolute path, got %q", socket)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	err := MakeStep(ctx, socket)
	t.Logf("MakeStep(%s) took %s, err=%v", socket, time.Since(start).Round(time.Millisecond), err)

	// What this test proves is our client against a real daemon: that chronyd
	// parses our 28-byte REQ_MAKESTEP, treats the Unix socket as privileged, and
	// answers in a form we decode. Two outcomes prove that.
	//
	//   success — the daemon accepted and stepped the clock.
	//   failed  — it accepted and could not act, which is what a daemon started
	//             with -x reports, having had clock control disabled.
	//
	// Anything else is a real defect: unauthorized means chronyd did not treat
	// the socket as privileged, invalid means it did not recognise command 43,
	// and a timeout means the exchange never completed.
	switch {
	case err == nil:
		t.Logf("chronyd stepped the clock")
	case strings.HasSuffix(err.Error(), chronyStatusNames[chronyStatusFailed]):
		t.Logf("chronyd accepted the command but has clock control disabled (-x); the exchange is proven")
	default:
		t.Fatalf("MakeStep against a real chronyd failed: %v", err)
	}
}
