package app

import (
	"testing"

	"sermo/internal/checks"
	"sermo/internal/operation"
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
