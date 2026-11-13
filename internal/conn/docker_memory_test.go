package conn

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDockerMemorySamples(t *testing.T) {
	for _, tt := range []struct {
		name, inspect, stats, pct, limit, limited string
		wantErr                                   bool
	}{
		{name: "below threshold without rounding", inspect: `{"Id":"abc","State":{"Running":true},"HostConfig":{"Memory":100000}}`, stats: `{"id":"abc","memory_stats":{"usage":79999,"limit":100000}}`, pct: "79.999", limit: "100000", limited: "true"},
		{name: "limited", inspect: `{"Id":"abc","State":{"Running":true},"HostConfig":{"Memory":1000}}`, stats: `{"id":"abc","memory_stats":{"usage":800,"limit":1000}}`, pct: "80", limit: "1000", limited: "true"},
		{name: "unlimited", inspect: `{"Id":"abc","State":{"Running":true},"HostConfig":{"Memory":0}}`, stats: `{"id":"abc","memory_stats":{"usage":800,"limit":9999}}`, pct: "0", limit: "0", limited: "false"},
		{name: "effective limit", inspect: `{"Id":"abc","State":{"Running":true},"HostConfig":{"Memory":2000}}`, stats: `{"id":"abc","memory_stats":{"usage":800,"limit":1000}}`, pct: "80", limit: "1000", limited: "true"},
		{name: "missing usage", inspect: `{"Id":"abc","State":{"Running":true},"HostConfig":{"Memory":1000}}`, stats: `{"id":"abc","memory_stats":{"limit":1000}}`, wantErr: true},
		{name: "replaced container", inspect: `{"Id":"abc","State":{"Running":true},"HostConfig":{"Memory":1000}}`, stats: `{"id":"other","memory_stats":{"usage":800,"limit":1000}}`, wantErr: true},
		{name: "stopped", inspect: `{"Id":"abc","State":{"Running":false},"HostConfig":{"Memory":1000}}`, wantErr: true},
		{name: "missing configured limit", inspect: `{"Id":"abc","State":{"Running":true}}`, wantErr: true},
		{name: "zero limit", inspect: `{"Id":"abc","State":{"Running":true},"HostConfig":{"Memory":1000}}`, stats: `{"id":"abc","memory_stats":{"usage":800,"limit":0}}`, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mux := dockerMux()
			mux.HandleFunc("/containers/app/json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tt.inspect)) })
			mux.HandleFunc("/containers/abc/stats", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("stream") != "false" || r.URL.Query().Get("one-shot") != "true" {
					t.Errorf("unbounded stats request: %s", r.URL)
				}
				_, _ = w.Write([]byte(tt.stats))
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			got, err := probeDocker(t, server.URL, Config{Query: "app", Params: map[string]string{ParamKeyDockerMemory: ParamValueTrue}})
			if (err != nil) != tt.wantErr {
				t.Fatalf("%+v, %v", got, err)
			}
			if !tt.wantErr && (got.Extra[dockerMemoryUsedPct] != tt.pct || got.Extra[dockerMemoryLimit] != tt.limit || got.Extra[dockerMemoryLimited] != tt.limited) {
				t.Fatalf("%+v", got.Extra)
			}
		})
	}
}
