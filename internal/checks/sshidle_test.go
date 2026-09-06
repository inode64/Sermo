package checks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sermo/internal/process"
	"sermo/internal/utmp"
)

const (
	sshShellPID = 100
	sshdPID     = 90
	sshPrivPID  = 95
	sshPeerPID  = 96
	testTTY     = 34816
	localTTY    = 34817
	testUserID  = 1000
	testGroupID = 2000
)

func sshSnapshot(extra ...process.Identity) map[int]process.Identity {
	snapshot := map[int]process.Identity{
		sshdPID:     {PID: sshdPID, PPID: 1, Exe: "/opt/sermo-test/sshd", ExeOK: true},
		sshShellPID: {PID: sshShellPID, PPID: sshdPID, UID: testUserID, GID: testGroupID, Exe: "/bin/bash", ExeOK: true, TTY: testTTY, TTYOK: true},
	}
	for _, id := range extra {
		snapshot[id.PID] = id
	}
	return snapshot
}

func testSSHConfig(protected ...SSHProtectedProcess) SSHIdleConfig {
	return SSHIdleConfig{IdleFor: 30 * time.Minute, SSHDExes: []string{"/opt/sermo-test/sshd"}, ProtectedProcesses: protected}
}

func testSSHTerminal(now time.Time) func(string) (utmp.Terminal, error) {
	return func(string) (utmp.Terminal, error) {
		return utmp.Terminal{Device: testTTY, AccessedAt: now.Add(-31 * time.Minute)}, nil
	}
}

func testSSHLookup() *process.UserLookup {
	return process.NewUserLookup(process.UserLookupConfig{Mode: process.UserLookupNumeric})
}

type failingSSHSnapshotReader struct{}

func (failingSSHSnapshotReader) PIDs() ([]int, error) { return nil, nil }
func (failingSSHSnapshotReader) Identity(int) (process.Identity, bool) {
	return process.Identity{}, false
}
func (failingSSHSnapshotReader) SnapshotWithError() (map[int]process.Identity, error) {
	return nil, errors.New("snapshot unavailable")
}

func TestSSHIdleSamplerReportsTerminalInputErrors(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		reader   process.Reader
		sessions func() ([]utmp.Session, error)
		want     string
	}{
		{
			name: "sessions",
			sessions: func() ([]utmp.Session, error) {
				return nil, errors.New("sessions unavailable")
			},
			want: "load terminal sessions: sessions unavailable",
		},
		{
			name:   "snapshot",
			reader: failingSSHSnapshotReader{},
			sessions: func() ([]utmp.Session, error) {
				return nil, nil
			},
			want: "read terminal processes: read snapshot: snapshot unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sampler := newSSHIdleSampler(test.reader, testSSHLookup(), test.sessions, testSSHTerminal(now), func() time.Time { return now })
			if _, err := sampler(testSSHConfig()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("sample error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSampleSSHIdle(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	backupFilter, err := process.NewIdentityFilter("/opt/sermo-test/mysqldump", "1001", "")
	if err != nil {
		t.Fatal(err)
	}
	ownerFilter, err := process.NewIdentityFilter("", "1000", "")
	if err != nil {
		t.Fatal(err)
	}
	groupFilter, err := process.NewIdentityFilter("", "", "2000")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		snapshot  map[int]process.Identity
		protected []SSHProtectedProcess
		want      SSHIdleSample
		wantErr   bool
	}{
		{name: "idle SSH terminal", snapshot: sshSnapshot(), want: SSHIdleSample{Count: 1, OldestIdle: 31 * time.Minute}},
		{name: "owner-only protection", snapshot: sshSnapshot(), protected: []SSHProtectedProcess{{Name: "deploy", Filter: ownerFilter}}, want: SSHIdleSample{ProtectedCount: 1}},
		{name: "group-only protection", snapshot: sshSnapshot(), protected: []SSHProtectedProcess{{Name: "dba", Filter: groupFilter}}, want: SSHIdleSample{ProtectedCount: 1}},
		{
			name: "exact protected process", protected: []SSHProtectedProcess{{Name: "backup", Filter: backupFilter}}, want: SSHIdleSample{ProtectedCount: 1},
			snapshot: sshSnapshot(process.Identity{PID: 101, PPID: sshShellPID, UID: 1001, GID: testGroupID, Exe: "/opt/sermo-test/mysqldump", ExeOK: true, TTY: testTTY, TTYOK: true}),
		},
		{
			name: "unreadable protected executable", protected: []SSHProtectedProcess{{Name: "backup", Filter: backupFilter}}, wantErr: true,
			snapshot: sshSnapshot(process.Identity{PID: 101, PPID: sshShellPID, UID: 1001, GID: testGroupID, ExeOK: false, TTY: testTTY, TTYOK: true}),
		},
		{
			name: "missing SSH ancestor", wantErr: true,
			snapshot: map[int]process.Identity{sshShellPID: {PID: sshShellPID, PPID: sshdPID, UID: testUserID, GID: testGroupID, Exe: "/bin/bash", ExeOK: true, TTY: testTTY, TTYOK: true}},
		},
		{
			name: "local terminal ignored", want: SSHIdleSample{},
			snapshot: map[int]process.Identity{sshShellPID: {PID: sshShellPID, PPID: 1, UID: testUserID, GID: testGroupID, Exe: "/bin/bash", ExeOK: true, TTY: testTTY, TTYOK: true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sampleSSHIdle([]utmp.Session{{User: "deploy", Line: "pts/0"}}, tt.snapshot, testSSHLookup(), testSSHTerminal(now), now, testSSHConfig(tt.protected...), mustSSHDFilters(t))
			if (err != nil) != tt.wantErr {
				t.Fatalf("sampleSSHIdle error = %v, wantErr=%v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("sampleSSHIdle = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSampleSSHSessionsSeparatesConsoleAndSSH(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	snapshot := sshSnapshot(
		process.Identity{PID: sshPrivPID, PPID: sshdPID, Exe: "/usr/lib/sshd-session", ExeOK: true},
		process.Identity{PID: sshPeerPID, PPID: sshPrivPID, Exe: "/usr/lib/sshd-session", ExeOK: true, StartTicks: 1234, StartTicksOK: true},
		process.Identity{PID: 101, PPID: 1, UID: testUserID, GID: testGroupID, Exe: "/bin/bash", ExeOK: true, TTY: localTTY, TTYOK: true},
	)
	// The shell sits below the session process, just as sshd-session does on
	// OpenRC hosts. The closer must target that session process, never sshd.
	snapshot[sshShellPID] = process.Identity{PID: sshShellPID, PPID: sshPeerPID, UID: testUserID, GID: testGroupID, Exe: "/bin/bash", ExeOK: true, TTY: testTTY, TTYOK: true}
	terminal := func(line string) (utmp.Terminal, error) {
		switch line {
		case "pts/0":
			return utmp.Terminal{Device: testTTY, AccessedAt: now.Add(-2 * time.Minute)}, nil
		case "tty1":
			return utmp.Terminal{Device: localTTY, AccessedAt: now.Add(-time.Minute)}, nil
		default:
			return utmp.Terminal{}, errors.New("unknown terminal")
		}
	}

	sample, err := sampleSSHSessions([]utmp.Session{{User: "root", Line: "pts/0"}, {User: "console", Line: "tty1"}}, snapshot, terminal, now, mustSSHDFilters(t), testSSHLookup().ResolveUser)
	if err != nil {
		t.Fatal(err)
	}
	if sample.Console != 1 || len(sample.SSH) != 1 {
		t.Fatalf("sample = %+v, want one console and one SSH session", sample)
	}
	got := sample.SSH[0]
	if got.User != "root" || got.Terminal != "pts/0" || got.PID != sshPeerPID || got.StartTicks != 1234 || got.Idle != 2*time.Minute {
		t.Fatalf("SSH session = %+v, want verified SSH peer", got)
	}
	if err := sample.VerifySSHSession(got); err != nil {
		t.Fatalf("VerifySSHSession(%+v): %v", got, err)
	}
	got.StartTicks++
	if err := sample.VerifySSHSession(got); err == nil {
		t.Fatal("VerifySSHSession accepted a recycled PID")
	}
}

func TestSampleSSHSessionsReportsUnknownTerminalAncestry(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	sample, err := sampleSSHSessions(
		[]utmp.Session{{User: "root", Line: "pts/0"}},
		map[int]process.Identity{sshShellPID: {PID: sshShellPID, PPID: sshdPID, TTY: testTTY, TTYOK: true, Exe: "/bin/bash", ExeOK: true}},
		testSSHTerminal(now), now, mustSSHDFilters(t), testSSHLookup().ResolveUser,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(sample.SSH) != 0 || sample.Console != 0 || len(sample.Issues) != 1 {
		t.Fatalf("sample = %+v, want one unavailable terminal issue", sample)
	}
	if got := sample.Issues[0]; got.Terminal != "pts/0" || got.User != "root" || got.Message != "no configured sshd identity in the live process ancestry" {
		t.Fatalf("issue = %+v", got)
	}
}

func TestSampleSSHSessionsReportsSSHDWithAnotherUser(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	filter, err := process.NewIdentityFilter("/opt/sermo-test/sshd", "0", "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := sshSnapshot()
	listener := snapshot[sshdPID]
	listener.UID = 1
	snapshot[sshdPID] = listener
	sample, err := sampleSSHSessions(
		[]utmp.Session{{User: "root", Line: "pts/0"}}, snapshot, testSSHTerminal(now), now,
		[]process.IdentityFilter{filter}, testSSHLookup().ResolveUser,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(sample.Issues) != 1 || len(sample.SSH) != 0 {
		t.Fatalf("sample = %+v, want unsafe ancestry issue", sample)
	}
}

func TestSampleSSHSessionsKeepsVerifiedSessionBesideReplacedBinary(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	const (
		staleTTY      = testTTY + 1
		stalePeerPID  = sshPeerPID + 10
		staleShellPID = sshShellPID + 10
	)
	snapshot := sshSnapshot(
		process.Identity{PID: sshPrivPID, PPID: sshdPID, Exe: "/usr/lib/sshd-session", ExeOK: true},
		process.Identity{PID: sshPeerPID, PPID: sshPrivPID, Exe: "/usr/lib/sshd-session", ExeOK: true, StartTicks: 1234, StartTicksOK: true},
		process.Identity{PID: stalePeerPID, PPID: 1, ExeOK: false, ExePrev: "/usr/lib/sshd-session", StartTicks: 5678, StartTicksOK: true},
		process.Identity{PID: staleShellPID, PPID: stalePeerPID, UID: testUserID, Exe: "/bin/bash", ExeOK: true, TTY: staleTTY, TTYOK: true},
	)
	snapshot[sshShellPID] = process.Identity{PID: sshShellPID, PPID: sshPeerPID, UID: testUserID, Exe: "/bin/bash", ExeOK: true, TTY: testTTY, TTYOK: true}
	terminal := func(line string) (utmp.Terminal, error) {
		switch line {
		case "pts/0":
			return utmp.Terminal{Device: testTTY, AccessedAt: now.Add(-time.Minute)}, nil
		case "pts/1":
			return utmp.Terminal{Device: staleTTY, AccessedAt: now.Add(-time.Hour)}, nil
		default:
			return utmp.Terminal{}, errors.New("unknown terminal")
		}
	}
	sample, err := sampleSSHSessions([]utmp.Session{
		{User: "root", Line: "pts/0", Host: "192.0.2.10"},
		{PID: stalePeerPID, User: "root", Line: "pts/1", Host: "192.0.2.11"},
	}, snapshot, terminal, now, mustSSHDFilters(t), testSSHLookup().ResolveUser)
	if err != nil {
		t.Fatal(err)
	}
	if len(sample.SSH) != 1 || sample.SSH[0].Terminal != "pts/0" || len(sample.Issues) != 1 {
		t.Fatalf("sample = %+v, want verified pts/0 plus unavailable pts/1", sample)
	}
	if got := sample.Issues[0]; got.Terminal != "pts/1" || got.Message != "executable /usr/lib/sshd-session was replaced" || got.PID != stalePeerPID || got.StartTicks != 5678 || !got.Remote {
		t.Fatalf("issue = %+v", got)
	}
}

// GNU screen writes each window into utmp with the origin host and a window
// tag ("192.0.2.10:S.0"), so six windows on a fleet host read as six SSH
// sessions that could never be attributed to sshd and sat in the panel as
// unavailable. A window belongs to the multiplexer session the terminal-session
// sources list; it is neither an SSH session nor an issue, whether its shell
// runs a live or a replaced bash.
func TestSampleSSHSessionsSkipsMultiplexerWindows(t *testing.T) {
	now := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	const (
		screenPID   = 16128
		windowTTY   = testTTY + 20
		staleTTY    = testTTY + 21
		windowShell = 16129
		staleShell  = 17490
	)
	snapshot := map[int]process.Identity{
		screenPID:   {PID: screenPID, PPID: 1, UID: 0, Exe: "/usr/bin/screen-4.9.1", ExeOK: true},
		windowShell: {PID: windowShell, PPID: screenPID, UID: 0, Exe: "/usr/bin/bash", ExeOK: true, TTY: windowTTY, TTYOK: true},
		staleShell:  {PID: staleShell, PPID: screenPID, UID: 0, ExeOK: false, ExePrev: "/usr/bin/bash", TTY: staleTTY, TTYOK: true},
	}
	terminal := func(line string) (utmp.Terminal, error) {
		switch line {
		case "pts/1":
			return utmp.Terminal{Device: windowTTY, AccessedAt: now.Add(-time.Minute)}, nil
		case "pts/2":
			return utmp.Terminal{Device: staleTTY, AccessedAt: now.Add(-time.Hour)}, nil
		default:
			return utmp.Terminal{}, errors.New("unknown terminal")
		}
	}
	sample, err := sampleSSHSessions([]utmp.Session{
		{PID: windowShell, User: "root", Line: "pts/1", Host: "192.0.2.10:S.0"},
		{PID: staleShell, User: "root", Line: "pts/2", Host: "192.0.2.10:S.1"},
	}, snapshot, terminal, now, mustSSHDFilters(t), testSSHLookup().ResolveUser)
	if err != nil {
		t.Fatal(err)
	}
	if len(sample.SSH) != 0 || len(sample.Issues) != 0 || sample.Console != 0 {
		t.Fatalf("sample = %+v, want screen windows left to the terminal-session source", sample)
	}
}

// A tmux client run from an SSH shell is still that SSH session: only an
// ancestor that is a multiplexer server makes a terminal a window.
func TestSampleSSHSessionsKeepsSSHSessionRunningAMultiplexerClient(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	snapshot := sshSnapshot()
	snapshot[sshShellPID+1] = process.Identity{PID: sshShellPID + 1, PPID: sshShellPID, UID: testUserID, Exe: "/usr/bin/tmux", ExeOK: true, TTY: testTTY, TTYOK: true}
	sample, err := sampleSSHSessions(
		[]utmp.Session{{User: "root", Line: "pts/0", Host: "192.0.2.10"}}, snapshot,
		testSSHTerminal(now), now, mustSSHDFilters(t), testSSHLookup().ResolveUser,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(sample.SSH) != 1 || len(sample.Issues) != 0 {
		t.Fatalf("sample = %+v, want the SSH session kept", sample)
	}
}

func TestSampleSSHSessionsDoesNotCountPreservedRemoteTerminalAsConsole(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	snapshot := map[int]process.Identity{
		sshShellPID: {PID: sshShellPID, PPID: 1, UID: testUserID, Exe: "/bin/bash", ExeOK: true, TTY: testTTY, TTYOK: true},
	}
	sample, err := sampleSSHSessions(
		[]utmp.Session{{User: "root", Line: "pts/0", Host: "192.0.2.10"}}, snapshot,
		testSSHTerminal(now), now, mustSSHDFilters(t), testSSHLookup().ResolveUser,
	)
	if err != nil {
		t.Fatal(err)
	}
	if sample.Console != 0 || len(sample.Issues) != 1 {
		t.Fatalf("sample = %+v, want preserved remote terminal issue rather than console", sample)
	}
}

func mustSSHDFilters(t *testing.T) []process.IdentityFilter {
	t.Helper()
	filters, err := sshdFilters([]string{"/opt/sermo-test/sshd"})
	if err != nil {
		t.Fatal(err)
	}
	return filters
}

func TestSSHIdleCheckReportsDataAndFailsClosed(t *testing.T) {
	check := sshIdleCheck{
		name: "idle", timeout: time.Second,
		preds: []levelPred{{field: DataKeyCount, op: ">", value: 0}},
		sampler: func(SSHIdleConfig) (SSHIdleSample, error) {
			return SSHIdleSample{Count: 1, ProtectedCount: 2, OldestIdle: 31 * time.Minute}, nil
		},
	}
	res := check.Run(context.Background())
	if !res.OK || res.Unavailable || res.Data[DataKeyProtectedCount] != 2 {
		t.Fatalf("result = %+v, want firing available check", res)
	}
	check.sampler = func(SSHIdleConfig) (SSHIdleSample, error) { return SSHIdleSample{}, errors.New("no procfs") }
	res = check.Run(context.Background())
	if res.OK || !res.Unavailable {
		t.Fatalf("failed sampler = %+v, want unavailable failure", res)
	}
}

func TestBuildSSHIdleCheckAcceptsOwnerOnlyProtection(t *testing.T) {
	built, warnings := Build(map[string]any{
		"idle": map[string]any{
			"type": CheckTypeSSHIdle, "idle_for": "30m", "sshd_exe": "/usr/sbin/sshd",
			"protected_processes": map[string]any{"deploy": map[string]any{"user": "1000"}},
			"protected_count":     map[string]any{"op": ">", "value": 0},
		},
	}, Deps{DefaultTimeout: time.Second})
	if len(warnings) != 0 || len(built) != 1 {
		t.Fatalf("built=%d warnings=%v, want one check", len(built), warnings)
	}
	if !built[0].Check.(sshIdleCheck).condition {
		t.Fatal("ssh_idle must default to condition reporting")
	}
}

func TestBuildSSHIdleCheckRejectsUnsupportedProtectionField(t *testing.T) {
	_, warnings := Build(map[string]any{
		"idle": map[string]any{
			"type": CheckTypeSSHIdle, "idle_for": "30m", "sshd_exe": "/usr/sbin/sshd",
			"count":               map[string]any{"op": ">", "value": 0},
			"protected_processes": map[string]any{"codex": map[string]any{"cmd": "codex"}},
		},
	}, Deps{DefaultTimeout: time.Second})
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one invalid protected-process warning", warnings)
	}
}

func TestSSHProtectedProcessFields(t *testing.T) {
	want := [3]string{CheckKeyExe, CheckKeyUser, CheckKeyGroup}
	if got := SSHProtectedProcessFields(); got != want {
		t.Fatalf("SSHProtectedProcessFields() = %v, want %v", got, want)
	}
	for _, field := range want {
		if !IsSSHProtectedProcessField(field) {
			t.Fatalf("IsSSHProtectedProcessField(%q) = false, want true", field)
		}
	}
	if IsSSHProtectedProcessField("cmd") {
		t.Fatal("IsSSHProtectedProcessField(cmd) = true, want false")
	}
}
