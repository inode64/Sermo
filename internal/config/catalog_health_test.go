package config

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
)

func TestCatalogHealthOverrides(t *testing.T) {
	for _, backend := range []string{"systemd", "openrc"} {
		t.Run(backend, func(t *testing.T) {
			files := map[string]string{
				"sermo.yml": "engine: {backend: " + backend + "}\npaths: {services: [@ROOT@/services]}\ndefaults: {policy: {cooldown: 5m}}\n",
			}
			names := []string{"nginx", "keydb", "slapd", "dovecot", "exim", "mongod", "mysql", "mariadb"}
			for _, name := range names {
				files[filepath.Join("services", name+".yml")] = "name: " + name + "\nuses: " + name + "\nvariables: {host: 192.0.2.45, port: 15432, config: /srv/db/custom.cnf, memory_alert_threshold: 85%}\n"
			}
			cfg, err := loadConfig(t, writeConfig(t, files), WithCatalogDirs(repoCatalogDir(repoRoot(t))))
			if err != nil {
				t.Fatal(err)
			}
			if issues := Validate(cfg); len(issues) != 0 {
				t.Fatal(issues)
			}
			for _, name := range names {
				resolved, errs := cfg.Resolve(name)
				if len(errs) != 0 {
					t.Fatalf("%s: %v", name, errs)
				}
				for key, raw := range nested(t, resolved.Tree, "checks") {
					check := raw.(map[string]any)
					if !slices.Contains([]string{"tcp", "redis", "mysql", "mongodb", "smtp", "ldap", "pop", "sieve", "imap"}, cfgval.String(check["type"])) {
						continue
					}
					if got := cfgval.String(check["host"]); got != "192.0.2.45" {
						t.Errorf("%s/%s host = %q", name, key, got)
					}
					if cfgval.String(check["type"]) == "tcp" && cfgval.String(check["port"]) != "15432" {
						t.Errorf("%s/%s port = %v", name, key, check["port"])
					}
				}
				if name == "mysql" || name == "mariadb" {
					command := cfgval.StringList(nested(t, resolved.Tree, "preflight", "config")["command"])
					if len(command) != 4 || !slices.Equal(command[1:], []string{"--defaults-file=/srv/db/custom.cnf", "--help", "--verbose"}) {
						t.Errorf("%s config command = %v", name, command)
					}
					if got := cfgval.String(nested(t, resolved.Tree, "checks", "alert-if-memory-high")["value"]); got != "85%" {
						t.Errorf("%s memory threshold = %q", name, got)
					}
					if action := nested(t, resolved.Tree, "rules", "alert-if-memory-high", "then")["action"]; action != "alert" {
						t.Errorf("%s memory action = %v", name, action)
					}
					if _, exists := nested(t, resolved.Tree, "rules")["restart-if-memory-high"]; exists {
						t.Errorf("%s still restarts on memory usage", name)
					}
				}
			}
		})
	}
}

func TestCatalogHTTPHealthVerdicts(t *testing.T) {
	for _, service := range []string{"grafana", "prometheus"} {
		watch := "health"
		if service == "prometheus" {
			watch = "ready"
		}
		body := catalogDocByName(t, repoRoot(t), "services", service)
		entry := catalogWatchCheck(t, body, watch)
		if !cfgval.Bool(entry["verify"]) {
			t.Fatalf("%s must verify readiness after start", service)
		}
		for _, tc := range []struct {
			name   string
			status int
			body   string
			want   bool
		}{
			{"healthy", http.StatusOK, `{"database":"ok"}`, true},
			{"unauthorized", http.StatusUnauthorized, `{"database":"ok"}`, false},
			{"missing-route", http.StatusNotFound, `{"database":"ok"}`, false},
			{"unavailable", http.StatusServiceUnavailable, `{"database":"ok"}`, false},
			{"database-failed", http.StatusOK, `{"database":"failed"}`, service == "prometheus"},
			{"missing-field", http.StatusOK, `{}`, service == "prometheus"},
		} {
			t.Run(service+"/"+tc.name, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
				}))
				t.Cleanup(srv.Close)
				check := maps.Clone(entry)
				check["url"] = srv.URL
				built, warnings := checks.Build(map[string]any{watch: check}, checks.Deps{DefaultTimeout: time.Second})
				if len(warnings) != 0 || len(built) != 1 {
					t.Fatalf("build: %v", warnings)
				}
				if result := built[0].Check.Run(context.Background()); result.OK != tc.want {
					t.Fatalf("OK = %v, want %v: %s", result.OK, tc.want, result.Message)
				}
			})
		}
	}
}

func TestCatalogBackupAndTemplateHealth(t *testing.T) {
	root := repoRoot(t)
	postgres := catalogDocByName(t, root, "services", "postgres-%v")
	if got := catalogWatchCheck(t, postgres, "restart-if-port-failed")["host"]; got != "${host}" {
		t.Fatalf("postgres TCP host = %v", got)
	}
	backrest := catalogDocByName(t, root, "services", "backrest")
	blocks := cfgval.StringList(nested(t, backrest, "rules", "block-restart-during-backup-or-restore")["blocks"])
	if !slices.Contains(blocks, "stop") || !slices.Contains(blocks, "restart") {
		t.Fatalf("backrest backup blocks = %v", blocks)
	}
	fpm := catalogDocByName(t, root, "services", "php-fpm%v%s%i")
	if got := nested(t, catalogWatchCheck(t, fpm, "fpm"), "expect", "listen_queue")["value"]; got != "${listen_queue_max}" {
		t.Fatalf("FPM queue threshold = %v", got)
	}
	for _, name := range []string{"redis", "keydb"} {
		body := catalogDocByName(t, root, "services", name)
		watch := nested(t, body, "watches", "alert-if-aof-write-failed")
		if watch["enabled"] != false {
			t.Fatalf("%s AOF check must require opt-in", name)
		}
		if got := nested(t, watch, "check", "expect")["aof_last_write_status"]; got != "ok" {
			t.Fatalf("%s AOF expectation = %v", name, got)
		}
	}
}
