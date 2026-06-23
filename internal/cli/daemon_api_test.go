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
  password: secret
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
kind: service
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/watches" {
			http.NotFound(w, r)
			return
		}
		writeDaemonAPITestJSON(w, []map[string]string{{"name": "storage-root", "state": "starting"}})
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
	opts := options{config: global}

	st, ok := app.fetchDaemonWatchState(context.Background(), opts, "storage-root")
	if !ok || st != "starting" {
		t.Fatalf("fetchDaemonWatchState() = (%q, %v), want (starting, true)", st, ok)
	}
}

func TestFetchDaemonApplicationStatesHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/applications" {
			http.NotFound(w, r)
			return
		}
		writeDaemonAPITestJSON(w, []map[string]string{{"name": "git", "state": "starting"}})
	}))
	defer srv.Close()

	_, global, cfg := daemonAPITestConfig(t, srv.URL, `
web:
  address: HOST
  port: PORT
paths:
  catalog: [CATALOG]
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
	content := template
	content = strings.ReplaceAll(content, "HOST", u.Hostname())
	content = strings.ReplaceAll(content, "PORT", strconv.Itoa(port))
	content = strings.ReplaceAll(content, "SERVICES", filepath.Join(root, "services"))
	content = strings.ReplaceAll(content, "WATCHES", filepath.Join(root, "watches"))
	content = strings.ReplaceAll(content, "CATALOG", filepath.Join(root, "catalog"))
	global = filepath.Join(root, "sermo.yml")
	mustWrite(t, global, content)
	cfg, err = config.Load(global)
	if err != nil {
		t.Fatal(err)
	}
	return root, global, cfg
}

func writeDaemonAPITestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
