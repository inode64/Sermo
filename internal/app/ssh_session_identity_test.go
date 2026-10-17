package app

import (
	"testing"

	"sermo/internal/checks"
	"sermo/internal/operation"
	"sermo/internal/process"
)

func TestFreshSSHSessionVerifierPreservesSignalIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		exe     string
		exeOK   bool
		sudo    bool
		changed bool
		wantOK  bool
	}{
		{name: "ordinary", exe: "/usr/lib/sshd-session", exeOK: true, wantOK: true},
		{name: "sudo", exe: "/usr/bin/sudo", exeOK: true, sudo: true, wantOK: true},
		{name: "unreadable executable", exe: "/usr/lib/sshd-session"},
		{name: "missing executable", exeOK: true},
		{name: "relative executable", exe: "sshd-session", exeOK: true},
		{name: "recycled pid", exe: "/usr/lib/sshd-session", exeOK: true, changed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := checks.SSHSession{PID: 96, StartTicks: 1234, Terminal: "pts/1", Exe: tc.exe, ExeOK: tc.exeOK, UID: 81, Residual: tc.sudo}
			if tc.sudo {
				session.Sudo = &checks.SudoSessionBoundary{MonitorPID: 97, MonitorStartTicks: 1235}
			}
			sample := checks.SSHSessionSample{SSH: []checks.SSHSession{session}}
			verify := freshSSHSessionVerifier(Deps{SSHSessionVerifier: func(checks.SSHSessionConfig) (checks.SSHSessionSample, error) { return sample, nil }}, nil)
			target := operation.SessionTarget{PID: session.PID, StartTicks: session.StartTicks, Terminal: session.Terminal}
			if tc.changed {
				target.StartTicks++
			}
			boundary, err := verify(t.Context(), target)
			if (err == nil) != tc.wantOK {
				t.Fatalf("boundary=%+v error=%v", boundary, err)
			}
			if tc.wantOK && (boundary.Exe != tc.exe || boundary.UID != session.UID || boundary.Residual != tc.sudo) {
				t.Fatalf("lost identity: %+v", boundary)
			}
			if tc.wantOK && tc.sudo && boundary.MonitorPID != session.Sudo.MonitorPID {
				t.Fatalf("lost sudo monitor: %+v", boundary)
			}
			if !tc.changed && sshSessionsToWeb(sample)[0].CanClose != tc.wantOK {
				t.Fatal("web capability does not reflect available identity")
			}
		})
	}
}

func TestFreshSSHSessionVerifierKeepsDeletedFileEvidence(t *testing.T) {
	session := checks.SSHSession{PID: 96, StartTicks: 1234, Terminal: "pts/1", ExePrev: "/usr/lib64/misc/sshd-session", ExeFile: process.ExecutableFile{Device: 1, Inode: 99}, Cgroup: "0::/openrc.sshd"}
	sample := checks.SSHSessionSample{SSH: []checks.SSHSession{session}}
	verify := freshSSHSessionVerifier(Deps{SSHSessionVerifier: func(checks.SSHSessionConfig) (checks.SSHSessionSample, error) { return sample, nil }}, nil)
	boundary, err := verify(t.Context(), operation.SessionTarget{PID: 96, StartTicks: 1234, Terminal: "pts/1"})
	if err != nil || boundary.ExePrev != session.ExePrev || boundary.ExeFile != session.ExeFile || boundary.Cgroup != session.Cgroup || !sshSessionsToWeb(sample)[0].CanClose {
		t.Fatalf("boundary=%+v err=%v", boundary, err)
	}
	sample.SSH[0].ExeFile = process.ExecutableFile{}
	if _, err := verify(t.Context(), operation.SessionTarget{PID: 96, StartTicks: 1234, Terminal: "pts/1"}); err == nil {
		t.Fatal("unverified deleted executable accepted")
	}
}
