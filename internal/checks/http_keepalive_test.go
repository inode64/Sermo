package checks

import (
	"context"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// connCountingServer serves 200 and counts every TCP connection the server
// accepts, so a test can tell a fresh connection per probe from a pooled one.
func connCountingServer(t *testing.T, tls bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var accepted atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			accepted.Add(1)
		}
	}
	if tls {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv, &accepted
}

// A probe must go through the target's accept path every cycle: a keep-alive
// connection opened before a daemon stopped accepting keeps answering and hides
// the outage (an OTLP receiver that exhausted its file descriptors was reported
// ready for hours that way).
func TestHTTPCheckOpensANewConnectionPerRun(t *testing.T) {
	cases := []struct {
		name  string
		tls   bool
		entry map[string]any
	}{
		{name: "default client", entry: map[string]any{}},
		{name: "cert client", tls: true, entry: map[string]any{"cert_verify": false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, accepted := connCountingServer(t, tc.tls)
			entry := map[string]any{"type": "http", "url": srv.URL, "expect_status": 200}
			maps.Copy(entry, tc.entry)
			built, warns := Build(map[string]any{"h": entry}, Deps{DefaultTimeout: time.Second})
			if len(warns) > 0 {
				t.Fatalf("build warns: %v", warns)
			}
			for i := range 2 {
				if res := built[0].Check.Run(context.Background()); !res.OK {
					t.Fatalf("run %d = %+v", i, res)
				}
			}
			if got := accepted.Load(); got != 2 {
				t.Fatalf("server accepted %d connection(s) for 2 probes, want 2 (one per probe)", got)
			}
		})
	}
}
