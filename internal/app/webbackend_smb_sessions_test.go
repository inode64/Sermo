package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
	"sermo/internal/metrics"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/web"
)

const smbAppFixture = `{"sessions":{"11":{"session_id":"11","server_id":{"pid":"101","task_id":"0","vnn":"4294967295","unique_id":"111"},"username":"alice","remote_machine":"192.0.2.1"}}}`

func TestDisabledSMBSourceCannotAuthorizeClose(t *testing.T) {
	sources := smbSessionSources(map[string]any{config.SectionChecks: map[string]any{
		"disabled": map[string]any{checks.CheckKeyType: checks.CheckTypeSMBSessions, "enabled": false, checks.CheckKeyBinary: "/usr/bin/smbstatus"},
		"active":   map[string]any{checks.CheckKeyType: checks.CheckTypeSMBSessions, checks.CheckKeyBinary: "/usr/bin/smbstatus", checks.CheckKeyTimeout: "2s"},
	}})
	if len(sources) != 1 || sources[0].check != "active" || sources[0].timeout != 2*time.Second {
		t.Fatalf("sources=%+v", sources)
	}
	runner := &execxtest.Runner{}
	verify := freshSMBSessionVerifier(&webEntry{smbSessions: sources}, Deps{ExecxRunner: runner})
	if _, err := verify(t.Context(), operation.SMBSessionTarget{Check: "disabled", Identity: "previously-listed"}); err == nil || len(runner.Calls()) != 0 {
		t.Fatalf("disabled source err=%v calls=%v", err, runner.Calls())
	}
}

func smbTestEntry(t *testing.T) (*webEntry, mapProcReader) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "smbd")
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	reader := mapProcReader{
		100: {PID: 100, PPID: 1, Exe: exe, ExeOK: true, UID: 0, StartTicks: 1000, StartTicksOK: true},
		101: {PID: 101, PPID: 100, Exe: exe, ExeOK: true, UID: 0, StartTicks: 1010, StartTicksOK: true},
	}
	entry := &webEntry{
		discoverer:  process.Discoverer{Reader: reader, ResolveUser: func(string) (uint32, bool) { return 0, true }},
		selectors:   []process.Selector{{Name: process.RoleMain, Type: process.SelectorCommandMatch, Exe: exe, User: "root"}},
		smbSessions: []smbSessionSource{{check: "sessions", config: checks.SMBSessionConfig{Binary: "/usr/bin/smbstatus"}, timeout: time.Second}},
	}
	return entry, reader
}

func TestSMBWorkerRequiresStrictServiceChild(t *testing.T) {
	for _, name := range []string{"worker", "listener", "reused", "wrong exe", "wrong uid", "deleted", "orphan", "delegated", "stray", "foreign cgroup", "blocked", "pidfile main", "cmd only", "parent changed"} {
		t.Run(name, func(t *testing.T) {
			entry, reader := smbTestEntry(t)
			procs, _ := entry.discoverer.Discover(entry.selectors)
			byPID := smbProcessIndex(procs)
			worker := byPID[101]
			session := checks.SMBSession{PID: 101, StartTicks: 1010, Identity: "generation"}
			switch name {
			case "listener":
				session.PID = 100
				session.StartTicks = 1000
			case "reused":
				session.StartTicks++
			case "wrong exe":
				worker.Exe = "/usr/bin/other"
			case "wrong uid":
				worker.UID = 1000
			case "deleted":
				worker.ExeOK = false
			case "orphan":
				worker.PPID = 1
			case "delegated":
				worker.Delegated = true
			case "stray":
				worker.Stray = true
			case "foreign cgroup":
				worker.Cgroup = "/other.service"
			case "blocked":
				worker.SignalBlockReason = "unknown ownership"
			case "pidfile main":
				worker.MainPidfile = true
			case "cmd only":
				entry.selectors[0].Exe = ""
			case "parent changed":
				id := reader[100]
				id.StartTicks++
				reader[100] = id
			}
			byPID[101] = worker
			got, err := smbSessionWorker(entry, byPID, session)
			if (err == nil) != (name == "worker") {
				t.Fatalf("worker=%+v err=%v", got, err)
			}
		})
	}
}

func TestSMBCloseRefreshesFullInventoryAndRejectsStaleIdentity(t *testing.T) {
	for _, name := range []string{"unchanged", "impersonated worker", "new login", "reused pid", "listener", "foreign source", "bad inventory", "cancel during refresh"} {
		t.Run(name, func(t *testing.T) {
			entry, reader := smbTestEntry(t)
			if name == "impersonated worker" {
				entry.unit = "samba"
				for pid, id := range reader {
					id.Cgroup = "0::/openrc.samba"
					if pid == 101 {
						id.UID = 1000
					}
					reader[pid] = id
				}
			}
			runner := execxtest.Fixed(execx.Result{Stdout: smbAppFixture}, nil)
			listed, err := checks.SampleSMBSessions(t.Context(), runner, entry.smbSessions[0].config, reader.Identity)
			if err != nil {
				t.Fatal(err)
			}
			fixture := smbAppFixture
			target := operation.SMBSessionTarget{Check: "sessions", Identity: listed[0].Identity}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch name {
			case "new login":
				fixture = strings.ReplaceAll(fixture, "alice", "bob")
			case "reused pid":
				id := reader[101]
				id.StartTicks++
				reader[101] = id
			case "listener":
				id := reader[101]
				id.PPID = 1
				reader[101] = id
			case "foreign source":
				target.Check = "other"
			case "bad inventory":
				fixture = "{}"
			case "cancel during refresh":
				entry.discoverer.FreshBackendPIDs = func() ([]int, error) { cancel(); return []int{100}, nil }
			}
			deps := Deps{ExecxRunner: execxtest.Fixed(execx.Result{Stdout: fixture}, nil), SMBSessionIdentity: reader.Identity}
			got, err := freshSMBSessionVerifier(entry, deps)(ctx, target)
			if (err == nil) != (name == "unchanged" || name == "impersonated worker") {
				t.Fatalf("process=%+v err=%v", got, err)
			}
			if name == "impersonated worker" && (got.UID != 1000 || got.Cgroup != "0::/openrc.samba") {
				t.Fatalf("lost worker identity for pidfd revalidation: %+v", got)
			}
		})
	}
}

func TestSMBImpersonatedWorkerRequiresOwnServiceCgroup(t *testing.T) {
	for _, tc := range []struct {
		name, unit, cgroup string
		want               bool
	}{
		{name: "OpenRC", unit: "samba", cgroup: "0::/openrc.samba", want: true},
		{name: "systemd", unit: "smb.service", cgroup: "0::/system.slice/smb.service", want: true},
		{name: "no unit", cgroup: "0::/openrc.samba"},
		{name: "root cgroup", unit: "samba", cgroup: "0::/"},
		{name: "slice", unit: "samba", cgroup: "0::/system.slice"},
		{name: "foreign unit", unit: "samba", cgroup: "0::/openrc.other"},
		{name: "colliding prefix", unit: "samba", cgroup: "0::/openrc.samba-other"},
		{name: "unreadable", unit: "samba"},
		{name: "parent changed", unit: "samba", cgroup: "0::/openrc.samba"},
		{name: "parent moved", unit: "samba", cgroup: "0::/openrc.samba"},
		{name: "parent impersonated", unit: "samba", cgroup: "0::/openrc.samba"},
		{name: "wrong executable", unit: "samba", cgroup: "0::/openrc.samba"},
		{name: "delegated", unit: "samba", cgroup: "0::/openrc.samba"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry, reader := smbTestEntry(t)
			entry.unit = tc.unit
			for pid, id := range reader {
				id.Cgroup = tc.cgroup
				if pid == 101 {
					id.UID = 1000
				}
				reader[pid] = id
			}
			procs, warnings := entry.discoverer.Discover(entry.selectors)
			if len(warnings) != 0 {
				t.Fatal(warnings)
			}
			byPID := smbProcessIndex(procs)
			parent, worker := reader[100], byPID[101]
			switch tc.name {
			case "parent changed":
				parent.StartTicks++
			case "parent moved":
				parent.Cgroup = "0::/openrc.other"
			case "parent impersonated":
				parent.UID = 1000
			case "wrong executable":
				worker.Exe = "/usr/bin/other"
			case "delegated":
				worker.Delegated = true
			}
			reader[100], byPID[101] = parent, worker
			got, err := smbSessionWorker(entry, byPID, checks.SMBSession{PID: 101, StartTicks: 1010, Identity: "generation"})
			if (err == nil) != tc.want {
				t.Fatalf("worker=%+v err=%v", got, err)
			}
			if tc.want && (got.UID != worker.UID || got.Cgroup != worker.Cgroup) {
				t.Fatalf("lost worker identity: %+v", got)
			}
		})
	}
}

func TestSMBSessionsUsePublishedDataAndRejectAReusedPID(t *testing.T) {
	entry, reader := smbTestEntry(t)
	entry.checkTypes = map[string]string{"sessions": checks.CheckTypeSMBSessions}
	entry.interval = time.Minute
	snaps := NewSnapshots()
	snaps.publishWithCheckTypes("smb", map[string]checks.Result{"sessions": {
		OK: true, Data: map[string]any{checks.DataKeySMBSessions: []checks.SMBSession{{PID: 101, StartTicks: 1010, User: "alice", Client: "192.0.2.1", SessionCount: 1, Identity: "generation"}}},
	}}, map[string]bool{"sessions": true}, entry.checkTypes)
	b := &WebBackend{order: []string{"smb"}, entries: map[string]*webEntry{"smb": entry}, snapshots: snaps, terminalProcessReader: reader}
	got := b.Sessions(context.Background())
	if len(got.SMB) != 1 || !got.SMB[0].CanClose || len(got.Sources) != 1 || got.Sources[0].State != web.SessionSourceAvailable {
		t.Fatalf("inventory=%+v", got)
	}
	id := reader[101]
	id.StartTicks++
	reader[101] = id
	got = b.Sessions(t.Context())
	if len(got.SMB) != 1 || got.SMB[0].CanClose || got.SMB[0].CPUReady || got.SMB[0].MemoryReady {
		t.Fatalf("reused PID inventory=%+v", got)
	}
	entry.disabled = true
	got = b.Sessions(t.Context())
	if len(got.SMB) != 0 || got.Sources[0].State != web.SessionSourceUnavailable {
		t.Fatalf("disabled inventory=%+v", got)
	}
}

func TestSMBHostInventoryFreshnessAndWorkerMetrics(t *testing.T) {
	_, reader := smbTestEntry(t)
	reader[102] = process.Identity{PID: 102, PPID: 101, StartTicks: 1020, StartTicksOK: true}
	sample := checks.SMBSession{PID: 101, StartTicks: 1010, User: "alice", Client: "192.0.2.1", SessionCount: 2, Identity: "generation"}
	now := time.Now()
	snaps := NewWatchSnapshots()
	snaps.now = func() time.Time { return now }
	snaps.publish("sessions", checks.CheckTypeSMBSessions, checks.Result{OK: true, Data: map[string]any{checks.DataKeySMBSessions: []checks.SMBSession{sample}}})
	metricReader := &liveCPUReader{cpu: map[int]uint64{101: 100, 102: 1000}, rss: map[int]uint64{101: 4096, 102: 9999}, hz: 100, ncpu: 1}
	collector := metrics.New(metricReader)
	collector.Now = func() time.Time { return now }
	b := &WebBackend{watchOrder: []string{"sessions"}, watches: map[string]*webWatch{"sessions": {checkType: checks.CheckTypeSMBSessions, interval: time.Minute}},
		watchSnapshots: snaps, terminalProcessReader: reader, sessionMetricCollector: collector, now: func() time.Time { return now }}
	first := b.Sessions(t.Context())
	if len(first.SMB) != 1 || first.SMB[0].CanClose || first.SMB[0].RSS != 4096 || first.SMB[0].CPUReady {
		t.Fatalf("first=%+v", first)
	}
	now = now.Add(time.Second)
	metricReader.cpu[101] += 25
	metricReader.cpu[102] += 1000
	second := b.Sessions(t.Context())
	if !second.SMB[0].CPUReady || second.SMB[0].CPU != 25 || second.SMB[0].RSS != 4096 {
		t.Fatalf("worker metrics=%+v", second)
	}
	now = now.Add(time.Hour)
	stale := b.Sessions(t.Context())
	if len(stale.SMB) != 0 || stale.Sources[0].State != web.SessionSourceCollecting {
		t.Fatalf("stale=%+v", stale)
	}
	snaps.publish("sessions", checks.CheckTypeSMBSessions, checks.Result{Unavailable: true, Message: "permission denied"})
	unavailable := b.Sessions(t.Context())
	if len(unavailable.SMB) != 0 || unavailable.Sources[0].State != web.SessionSourceUnavailable {
		t.Fatalf("unavailable=%+v", unavailable)
	}
}
