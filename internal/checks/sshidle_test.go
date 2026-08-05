package checks

import (
	"context"
	"errors"
	"testing"
	"time"

	"sermo/internal/process"
	"sermo/internal/utmp"
)

const (
	sshShellPID = 100
	sshdPID     = 90
	testTTY     = 34816
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
		base:  base{name: "idle", timeout: time.Second},
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
