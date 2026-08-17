package process

import (
	"testing"

	"github.com/goccy/go-yaml"
)

// FuzzParseSelectors ensures untrusted process selector trees never panic.
func FuzzParseSelectors(f *testing.F) {
	f.Add([]byte("pidfile: /run/svc.pid\nprocesses:\n  main:\n    exe: /usr/sbin/svc\n    user: root\n"))
	f.Add([]byte("processes:\n  main: not-a-map\n"))
	f.Add([]byte("processes:\n  main:\n    cmd: \"[\"\n"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, source []byte) {
		tree, ok := fuzzYAMLMap(source)
		if !ok {
			return
		}
		_, _ = ParseSelectors(tree)
	})
}

// FuzzParseStopPolicy ensures untrusted stop_policy trees never panic.
func FuzzParseStopPolicy(f *testing.F) {
	f.Add([]byte("stop_policy:\n  graceful_timeout: 10s\n  force_kill: true\n  kill_only_if:\n    users: [mysql]\n    exe_any: [/usr/sbin/mysqld]\n"))
	f.Add([]byte("stop_policy:\n  graceful_timeout: not-a-duration\n"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, source []byte) {
		tree, ok := fuzzYAMLMap(source)
		if !ok {
			return
		}
		_, _ = ParseStopPolicy(tree)
	})
}

// FuzzParseReapPolicy fuzzes the other parser that produces kill authority. It
// must never panic and must never hand back a configured selector from malformed
// input: a KillSelector that reports Configured() is one `sermoctl reap --apply`
// would act on.
func FuzzParseReapPolicy(f *testing.F) {
	f.Add([]byte("reap:\n  kill_only_if:\n    users: [root]\n    exe_any: [/usr/bin/dbus-daemon]\n"))
	f.Add([]byte("reap:\n  kill_only_if:\n    users: [root]\n"))
	f.Add([]byte("reap: root\n"))
	f.Add([]byte("reap:\n  kill_if: {}\n"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, source []byte) {
		tree, ok := fuzzYAMLMap(source)
		if !ok {
			return
		}
		selector, warnings := ParseReapPolicy(tree)
		if !selector.Configured() {
			return
		}
		// A configured selector must carry both halves: users alone would authorize
		// on UID, exe_any alone on the binary, and either would widen what a reap
		// may signal beyond what the operator wrote.
		if len(selector.Users) == 0 || len(selector.ExeAny) == 0 {
			t.Fatalf("configured selector from %q lacks a half: users=%v exe_any=%v (warnings %v)",
				source, selector.Users, selector.ExeAny, warnings)
		}
	})
}

// FuzzParseSignal ensures signal name parsing never panics on arbitrary input.
func FuzzParseSignal(f *testing.F) {
	f.Add("HUP")
	f.Add("sighup")
	f.Add("SIGHUP")
	f.Add("NOTASIGNAL")
	f.Add("")

	f.Fuzz(func(_ *testing.T, name string) {
		_, _ = ParseSignal(name)
	})
}

// FuzzParseKillSignal ensures kill-signal name parsing never panics.
func FuzzParseKillSignal(f *testing.F) {
	f.Add("TERM")
	f.Add("KILL")
	f.Add("sigterm")
	f.Add("HUP")
	f.Add("")

	f.Fuzz(func(_ *testing.T, name string) {
		_, _ = ParseKillSignal(name)
	})
}

func fuzzYAMLMap(source []byte) (map[string]any, bool) {
	var tree map[string]any
	if err := yaml.Unmarshal(source, &tree); err != nil || tree == nil {
		return nil, false
	}
	return tree, true
}
