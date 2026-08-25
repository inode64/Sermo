package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"sermo/internal/config"
)

func TestFetchDaemonServiceStateHTTP(t *testing.T) {
	t.Setenv(config.EnvWebPassword, "secret")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/services/mysql" {
			http.NotFound(w, r)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Basic YWRtaW46c2VjcmV0" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeDaemonAPITestJSON(w, map[string]string{"state": "starting"})
	}))
	defer srv.Close()

	root, global, cfg := daemonAPITestConfig(t, srv.URL, `
web:
  address: HOST
  port: PORT
paths:
  services: [SERVICES]
defaults:
  policy: { cooldown: 5m }
`)
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(servicesDir, "mysql.yml"), `
name: mysql
service: mysql.service
`)

	cfg, err := config.Load(global)
	if err != nil {
		t.Fatal(err)
	}

	app := App{LoadConfig: func(string, ...config.Option) (*config.Config, error) { return cfg, nil }}
	opts := options{config: global}

	st, ok := app.fetchDaemonServiceState(context.Background(), opts, "mysql")
	if !ok {
		t.Fatal("fetchDaemonServiceState() ok = false, want true")
	}
	if st != "starting" {
		t.Fatalf("state = %q, want starting", st)
	}
}

func TestFetchDaemonWatchStateHTTP(t *testing.T) {
	srv := daemonAPIStub("/api/watches", []map[string]string{{"name": "storage-root", "state": "starting"}})
	defer srv.Close()

	_, global, cfg := daemonAPITestConfig(t, srv.URL, `
web:
  address: HOST
  port: PORT
paths:
  watches: [WATCHES]
`)
	app := App{LoadConfig: func(string, ...config.Option) (*config.Config, error) { return cfg, nil }}
	opts := options{config: global}

	st, ok := app.fetchDaemonWatchState(context.Background(), opts, "storage-root")
	if !ok || st != "starting" {
		t.Fatalf("fetchDaemonWatchState() = (%q, %v), want (starting, true)", st, ok)
	}
}

func TestFetchEventsHTTP(t *testing.T) {
	tests := []struct {
		name    string
		service string
		path    string
		payload any
	}{
		{
			name:    "global cursor page",
			path:    "/api/events",
			payload: map[string]any{"events": []event{{Service: "web", Kind: "alert", Message: "down"}}, "has_more": true},
		},
		{
			name:    "service event array",
			service: "web",
			path:    "/api/services/web/events",
			payload: []event{{Service: "web", Kind: "recovered", Message: "up"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(config.EnvWebPassword, "secret")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					http.NotFound(w, r)
					return
				}
				if r.URL.Query().Get(daemonAPIQueryLimit) != "7" {
					http.Error(w, "unexpected limit", http.StatusBadRequest)
					return
				}
				if auth := r.Header.Get("Authorization"); auth != "Basic YWRtaW46c2VjcmV0" {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				writeDaemonAPITestJSON(w, tc.payload)
			}))
			defer srv.Close()

			_, global, cfg := daemonAPITestConfig(t, srv.URL, `
web:
  address: HOST
  port: PORT
paths:
`)
			app := App{LoadConfig: func(string, ...config.Option) (*config.Config, error) { return cfg, nil }}

			events, err := app.fetchEvents(context.Background(), options{config: global}, tc.service, 7)
			if err != nil {
				t.Fatalf("fetchEvents() error = %v", err)
			}
			if len(events) != 1 || events[0].Service != "web" {
				t.Fatalf("fetchEvents() = %+v, want one web event", events)
			}
		})
	}
}

func TestProbeDaemonWatchHTTP(t *testing.T) {
	t.Setenv(config.EnvWebPassword, "secret")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/watches/disk-speed/probe" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get(daemonWebCSRFHeader) != daemonWebCSRFValue {
			http.Error(w, "missing csrf", http.StatusForbidden)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Basic YWRtaW46c2VjcmV0" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeDaemonAPITestJSON(w, daemonWatchProbe{OK: true, Message: "hdparm /dev/sda read=166.67 MB/s", Readings: []daemonWatchReading{{Field: "read", Label: "Read", Value: "166.67 MB/s"}}})
	}))
	defer srv.Close()

	_, global, cfg := daemonAPITestConfig(t, srv.URL, `
web:
  address: HOST
  port: PORT
paths:
  watches: [WATCHES]
`)
	app := App{LoadConfig: func(string, ...config.Option) (*config.Config, error) { return cfg, nil }}
	result, err := app.probeDaemonWatch(context.Background(), options{config: global}, "disk-speed")
	if err != nil || !result.OK || len(result.Readings) != 1 || result.Readings[0].Value != "166.67 MB/s" {
		t.Fatalf("probe result=%+v err=%v", result, err)
	}
}

// The daemon rejects a mutation that does not name the generation it was aimed
// at, so every CLI probe answered 428 and no manual probe ever ran. The client
// must read the generation before it writes.
func TestProbeDaemonWatchSendsTheBackendGeneration(t *testing.T) {
	const generation = "7"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(daemonWebGenerationHeader, generation)
		if r.Method == http.MethodGet && r.URL.Path == "/api/watches" {
			writeDaemonAPITestJSON(w, []daemonWatchDetail{})
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/watches/diskio-sdd/probe" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get(daemonWebGenerationHeader); got != generation {
			http.Error(w, "X-Sermo-Generation header is required", http.StatusPreconditionRequired)
			return
		}
		writeDaemonAPITestJSON(w, daemonWatchProbe{OK: true, Message: "diskio sdd util 0.0%"})
	}))
	defer srv.Close()

	_, global, cfg := daemonAPITestConfig(t, srv.URL, `
web:
  address: HOST
  port: PORT
paths:
  watches: [WATCHES]
`)
	app := App{LoadConfig: func(string, ...config.Option) (*config.Config, error) { return cfg, nil }}
	result, err := app.probeDaemonWatch(context.Background(), options{config: global}, "diskio-sdd")
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if !result.OK {
		t.Fatalf("probe result = %+v, want ok", result)
	}
}

func TestFetchDaemonApplicationStatesHTTP(t *testing.T) {
	srv := daemonAPIStub("/api/applications", []map[string]string{{"name": "git", "state": "starting"}})
	defer srv.Close()

	_, global, cfg := daemonAPITestConfig(t, srv.URL, `
web:
  address: HOST
  port: PORT
paths:
`)
	app := App{LoadConfig: func(string, ...config.Option) (*config.Config, error) { return cfg, nil }}
	opts := options{config: global}

	states := app.fetchDaemonApplicationStates(context.Background(), opts)
	if got := states["git"]; got != "starting" {
		t.Fatalf("states[git] = %q, want starting; map=%v", got, states)
	}
}

func daemonAPITestConfig(t *testing.T, serverURL, template string) (root, global string, cfg *config.Config) {
	t.Helper()
	root = t.TempDir()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	// Pin paths.runtime inside the temp root: the daemon web token lives there,
	// and these tests must not pick up (or miss) the one a real sermod wrote.
	runtimeEntry := "  runtime: " + filepath.Join(root, "run") + "\n"
	content := template
	if strings.Contains(content, "paths:\n") {
		content = strings.Replace(content, "paths:\n", "paths:\n"+runtimeEntry, 1)
	} else {
		content += "\npaths:\n" + runtimeEntry
	}
	content = strings.ReplaceAll(content, "HOST", u.Hostname())
	content = strings.ReplaceAll(content, "PORT", strconv.Itoa(port))
	content = strings.ReplaceAll(content, "SERVICES", filepath.Join(root, "services"))
	content = strings.ReplaceAll(content, "WATCHES", filepath.Join(root, "watches"))
	global = filepath.Join(root, "sermo.yml")
	mustWrite(t, global, content)
	cfg, err = config.Load(global)
	if err != nil {
		t.Fatal(err)
	}
	return root, global, cfg
}

// daemonAPIStub serves payload as JSON at path and 404s every other request.
func daemonAPIStub(path string, payload any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		writeDaemonAPITestJSON(w, payload)
	}))
}

func writeDaemonAPITestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
