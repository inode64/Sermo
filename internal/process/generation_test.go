package process

import (
	"os"
	"testing"
)

func TestGenerationExited(t *testing.T) {
	for _, tc := range []struct {
		name          string
		id            Identity
		known         bool
		readErr       error
		gone, wantErr bool
	}{
		{name: "live", id: Identity{StartTicks: 100, StartTicksOK: true}, known: true},
		{name: "reused", id: Identity{StartTicks: 101, StartTicksOK: true}, known: true, gone: true},
		{name: "zombie", id: Identity{StartTicks: 100, StartTicksOK: true, State: ProcStateZombie}, known: true, gone: true},
		{name: "absent", readErr: os.ErrNotExist, gone: true},
		{name: "permission", readErr: os.ErrPermission, wantErr: true},
		{name: "malformed", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gone, err := generationExited(96, 100, func(int) (Identity, bool) { return tc.id, tc.known }, func(string) ([]byte, error) { return nil, tc.readErr })
			if gone != tc.gone || (err != nil) != tc.wantErr {
				t.Fatalf("gone=%v err=%v", gone, err)
			}
		})
	}
}

func TestSameGenerationNeedsPIDAndStartTicks(t *testing.T) {
	daemon := Process{PID: 100, StartTicks: 10, Exe: "/usr/sbin/sshd"}
	for _, tc := range []struct {
		name  string
		other Process
		want  bool
	}{
		{name: "same generation, other fields differ", other: Process{PID: 100, StartTicks: 10, Exe: "/usr/bin/other"}, want: true},
		{name: "recycled pid", other: Process{PID: 100, StartTicks: 11}},
		{name: "other pid", other: Process{PID: 101, StartTicks: 10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := daemon.SameGeneration(tc.other); got != tc.want {
				t.Fatalf("SameGeneration = %v, want %v", got, tc.want)
			}
			if got := ContainsGeneration([]Process{{PID: 7, StartTicks: 1}, tc.other}, daemon); got != tc.want {
				t.Fatalf("ContainsGeneration = %v, want %v", got, tc.want)
			}
		})
	}
}
