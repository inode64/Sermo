package cli

import (
	"testing"
	"time"
)

func TestParseArgsSuccess(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		check func(t *testing.T, o options)
	}{
		{"command only", []string{"status"}, func(t *testing.T, o options) {
			t.Helper()
			if o.command != "status" || len(o.args) != 0 {
				t.Fatalf("got %+v", o)
			}
		}},
		{"command + positional", []string{"start", "nginx"}, func(t *testing.T, o options) {
			t.Helper()
			if o.command != "start" || o.service() != "nginx" {
				t.Fatalf("got command=%q service=%q", o.command, o.service())
			}
		}},
		{"--config= form", []string{"--config=/etc/s.yml", "status"}, func(t *testing.T, o options) {
			t.Helper()
			if o.config != "/etc/s.yml" || o.command != "status" {
				t.Fatalf("got %+v", o)
			}
		}},
		{"--config space form", []string{"--config", "/etc/s.yml", "status"}, func(t *testing.T, o options) {
			t.Helper()
			if o.config != "/etc/s.yml" {
				t.Fatalf("config = %q", o.config)
			}
		}},
		{"bool flags", []string{"--json", "--quiet", "--no-cascade", "status"}, func(t *testing.T, o options) {
			t.Helper()
			if !o.json || !o.quiet || !o.noCascade {
				t.Fatalf("got %+v", o)
			}
		}},
		{"--since duration", []string{"sla", "--since", "24h"}, func(t *testing.T, o options) {
			t.Helper()
			if o.since != 24*time.Hour {
				t.Fatalf("since = %v", o.since)
			}
		}},
		{"--notify list", []string{"services", "--notify", "ops,pager", "--notify=team"}, func(t *testing.T, o options) {
			t.Helper()
			want := []string{"ops", "pager", "team"}
			if len(o.notifyNames) != len(want) {
				t.Fatalf("notifyNames = %v", o.notifyNames)
			}
			for i := range want {
				if o.notifyNames[i] != want[i] {
					t.Fatalf("notifyNames = %v, want %v", o.notifyNames, want)
				}
			}
		}},
		{"-- captures literal command", []string{"lock", "build", "--", "echo", "hi"}, func(t *testing.T, o options) {
			t.Helper()
			if o.command != "lock" || o.service() != "build" {
				t.Fatalf("command/service = %q/%q", o.command, o.service())
			}
			if len(o.commandArgs) != 2 || o.commandArgs[0] != "echo" || o.commandArgs[1] != "hi" {
				t.Fatalf("commandArgs = %v", o.commandArgs)
			}
		}},
		{"-- at end is empty, not a panic", []string{"lock", "build", "--"}, func(t *testing.T, o options) {
			t.Helper()
			if len(o.commandArgs) != 0 {
				t.Fatalf("commandArgs = %v, want empty", o.commandArgs)
			}
		}},
		{"--help", []string{"--help"}, func(t *testing.T, o options) {
			t.Helper()
			if !o.help {
				t.Fatal("help not set")
			}
		}},
		{"--limit positive", []string{"events", "--limit", "10"}, func(t *testing.T, o options) {
			t.Helper()
			if o.eventLimit != 10 {
				t.Fatalf("eventLimit = %d, want 10", o.eventLimit)
			}
		}},
		{"--limit unset stays default sentinel", []string{"events"}, func(t *testing.T, o options) {
			t.Helper()
			if o.eventLimit != 0 {
				t.Fatalf("eventLimit = %d, want 0 (unset)", o.eventLimit)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, err := parseArgs(c.args)
			if err != nil {
				t.Fatalf("parseArgs(%v) error = %v", c.args, err)
			}
			c.check(t, o)
		})
	}
}

func TestParseArgsErrors(t *testing.T) {
	cases := [][]string{
		{"--config"},          // missing value
		{"--since"},           // missing value
		{"--since", "nope"},   // bad duration
		{"--timeout", "nope"}, // bad duration
		{"--backend", "nope"}, // bad backend
		{"--bogus"},           // unknown flag
		{"--limit", "0"},      // explicit zero is not a valid count
		{"--limit", "-3"},     // negative
	}
	for _, args := range cases {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("parseArgs(%v) = nil error, want an error", args)
		}
	}
}
