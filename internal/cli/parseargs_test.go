package cli

import (
	"reflect"
	"testing"
	"time"
)

type parsedOptionsView struct {
	command, config, service string
	json, quiet, noCascade   bool
	force, lazy, kill, help  bool
	since                    time.Duration
	notify, commandArgs      []string
	eventLimit               int
}

func optionsView(o options) parsedOptionsView {
	return parsedOptionsView{
		command: o.command, config: o.config, service: o.service(), json: o.json, quiet: o.quiet, noCascade: o.noCascade,
		force: o.force, lazy: o.lazy, kill: o.kill, help: o.help, since: o.since,
		notify: o.notifyNames, commandArgs: o.commandArgs, eventLimit: o.eventLimit,
	}
}

func TestParseArgsSuccess(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want parsedOptionsView
	}{
		{"command only", []string{"status"}, parsedOptionsView{command: "status"}},
		{"command + positional", []string{"start", "nginx"}, parsedOptionsView{command: "start", service: "nginx"}},
		{"--config= form", []string{"--config=/etc/s.yml", "status"}, parsedOptionsView{command: "status", config: "/etc/s.yml"}},
		{"--config space form", []string{"--config", "/etc/s.yml", "status"}, parsedOptionsView{command: "status", config: "/etc/s.yml"}},
		{"bool flags", []string{"--json", "--quiet", "--no-cascade", "status"}, parsedOptionsView{command: "status", json: true, quiet: true, noCascade: true}},
		{"umount escalation flags", []string{"umount", "--force", "--lazy", "--kill-blockers", "mount-backup"}, parsedOptionsView{command: "umount", service: "mount-backup", force: true, lazy: true, kill: true}},
		{"--since duration", []string{"sla", "--since", "24h"}, parsedOptionsView{command: "sla", since: 24 * time.Hour}},
		{"--notify list", []string{"services", "--notify", "ops,pager", "--notify=team"}, parsedOptionsView{command: "services", notify: []string{"ops", "pager", "team"}}},
		{"-- captures literal command", []string{"lock", "build", "--", "echo", "hi"}, parsedOptionsView{command: "lock", service: "build", commandArgs: []string{"echo", "hi"}}},
		{"-- at end is empty", []string{"lock", "build", "--"}, parsedOptionsView{command: "lock", service: "build"}},
		{"--help", []string{"--help"}, parsedOptionsView{help: true}},
		{"--limit positive", []string{"events", "--limit", "10"}, parsedOptionsView{command: "events", eventLimit: 10}},
		{"--limit unset", []string{"events"}, parsedOptionsView{command: "events"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, err := parseArgs(c.args)
			if err != nil {
				t.Fatalf("parseArgs(%v) error = %v", c.args, err)
			}
			if got := optionsView(o); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("options = %+v, want %+v", got, c.want)
			}
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
