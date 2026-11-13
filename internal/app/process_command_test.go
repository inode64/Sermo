package app

import (
	"regexp"
	"testing"
	"time"

	"sermo/internal/execx/execxtest"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

func TestProcessCommandGuardObservesExternalJobFreshly(t *testing.T) {
	for _, action := range []string{"start", "stop", "restart", "reload", "resume", "repair", "pause"} {
		t.Run(action, func(t *testing.T) {
			for _, unreadable := range []bool{false, true} {
				host := serviceOwnershipReader{}
				cached := process.NewCachingReader(host, time.Hour)
				stopped := false
				events := 0
				tree := map[string]any{
					"processes": map[string]any{"main": map[string]any{"exe": "/opt/app/server", "user": "0"}},
					"rules": map[string]any{"packages": map[string]any{
						"type": "guard", "blocks": []any{"start", "stop", "restart", "reload", "resume"},
						"if":   map[string]any{"not": map[string]any{"process": map[string]any{"cmd": "(^|/)emerge($| )", "state": "absent", "timeout": "1s"}}},
						"then": map[string]any{"action": "block", "message": "package update"},
					}},
				}
				runtime := BuildServiceRuntime(t.Context(), ServiceRuntimeConfig{
					Service: "web", Unit: "web", Tree: tree,
					RecordOperation: func(operation.Result) { events++ },
					Deps: Deps{Backend: servicemgr.BackendOpenRC, Manager: fakeManager{stopped: &stopped}, Runtime: t.TempDir(),
						ProcReader: cached, ExecxRunner: &execxtest.Runner{}},
				})
				if got := runtime.CheckDeps.ProcessesAny(t.Context(), nil, "", regexp.MustCompile("emerge")); got != process.StateAbsent {
					t.Fatalf("initial state = %s", got)
				}
				host[42] = process.Identity{PID: 42, PPID: 1, StartTicks: 42, StartTicksOK: true,
					Exe: "/usr/bin/python3", ExeOK: true, State: "S", Cmdline: []string{"python3", "/usr/bin/emerge", "world"}, CmdlineUnreadable: unreadable}
				res := runtime.Engine.Do(t.Context(), action)
				want := operation.ResultBlocked
				if unreadable {
					want = operation.ResultFailed
				}
				if res.Status != want || stopped || events != 1 {
					t.Fatalf("unreadable=%v result=%+v stopped=%v events=%d", unreadable, res, stopped, events)
				}
			}
		})
	}
}

func TestProcWatchCommandFilterAndUnavailableArgv(t *testing.T) {
	match := ProcMatch{Cmd: regexp.MustCompile(`(^|/)emerge($| )`)}
	id := process.Identity{PID: 42, Cmdline: []string{"python3", "/usr/bin/emerge", "world"}}
	if matched, known := procMatchesWithLookup(match, id, nil); !matched || !known {
		t.Fatal("cmd-only selector did not match the interpreted script")
	}
	match.Name = "bash"
	if matched, known := procMatchesWithLookup(match, id, nil); matched || !known {
		t.Fatal("cmd ignored the name filter")
	}
	match.Name = ""
	id.CmdlineUnreadable = true
	sampler := osProcSampler{host: serviceOwnershipReader{42: id}, counters: fakeProcCounters{}}
	if samples, ok := sampler.Sample(match); ok || len(samples) != 0 {
		t.Fatalf("unreadable cmd = %v, %v, want unavailable", samples, ok)
	}
	entry := map[string]any{"then": map[string]any{"hook": map[string]any{"command": []any{"/bin/report"}}}}
	check := map[string]any{"type": "process", "cmd": "emerge", "gone": true}
	if watch, warning := buildProcWatch("packages", entry, check, Deps{}, time.Second); watch == nil || warning != "" {
		t.Fatalf("cmd-only watch = %v, %s", watch, warning)
	}
	entry["then"] = map[string]any{"kill": map[string]any{"signal": "TERM"}}
	if watch, warning := buildProcWatch("packages", entry, check, Deps{}, time.Second); watch != nil || warning == "" {
		t.Fatalf("cmd-only watch acquired kill authority: %v, %s", watch, warning)
	}
}

func TestProcWatchUnreadableCommandAfterOtherFilters(t *testing.T) {
	id := process.Identity{PID: 42, Exe: "/usr/bin/python3", ExeOK: true, User: "root", CmdlineUnreadable: true}
	for _, match := range []ProcMatch{
		{Name: "bash", Cmd: regexp.MustCompile("emerge")},
		{User: "nobody", Cmd: regexp.MustCompile("emerge")},
	} {
		sampler := osProcSampler{host: serviceOwnershipReader{42: id}, counters: fakeProcCounters{}}
		if samples, ok := sampler.Sample(match); !ok || len(samples) != 0 {
			t.Fatalf("unrelated unreadable cmd with %+v = %v, %v, want complete empty sample", match, samples, ok)
		}
	}
}
