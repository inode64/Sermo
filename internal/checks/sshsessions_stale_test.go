package checks

import (
	"fmt"
	"os"
	"testing"

	"sermo/internal/process"
	"sermo/internal/utmp"
)

func TestLiveTerminalSessionsRequiresProvenAbsence(t *testing.T) {
	for _, test := range []struct {
		name        string
		present     bool
		terminalErr error
		processErr  error
		want        int
	}{
		{name: "closed", terminalErr: fmt.Errorf("terminal: %w", os.ErrNotExist), processErr: os.ErrNotExist},
		{name: "visible leader", present: true, terminalErr: os.ErrNotExist, processErr: os.ErrNotExist, want: 1},
		{name: "live terminal", processErr: os.ErrNotExist, want: 1},
		{name: "terminal permission", terminalErr: os.ErrPermission, processErr: os.ErrNotExist, want: 1},
		{name: "unreadable leader", terminalErr: os.ErrNotExist, processErr: os.ErrPermission, want: 1},
		{name: "leader appeared", terminalErr: os.ErrNotExist, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := make(map[int]process.Identity)
			if test.present {
				snapshot[sshShellPID] = process.Identity{PID: sshShellPID}
			}
			got := liveTerminalSessions([]utmp.Session{{PID: sshShellPID, Line: "pts/0"}}, snapshot,
				func(string) (utmp.Terminal, error) { return utmp.Terminal{}, test.terminalErr },
				func(path string) ([]byte, error) {
					if path != process.PIDPath(sshShellPID, process.ProcFileStat) {
						t.Fatalf("unexpected path %s", path)
					}
					return nil, test.processErr
				})
			if len(got) != test.want {
				t.Fatalf("sessions = %+v, want %d", got, test.want)
			}
		})
	}
}
