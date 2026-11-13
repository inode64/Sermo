package checks

import (
	"context"
	"errors"
	"testing"

	"sermo/internal/process"
)

func TestSupervisedProcessResults(t *testing.T) {
	for _, tt := range []struct {
		name                    string
		err                     error
		wantOK, wantUnavailable bool
	}{
		{name: "ready", wantOK: true},
		{name: "not ready", err: process.ErrSupervisedNotReady},
		{name: "uncertain", err: errors.New("unreadable identity"), wantUnavailable: true},
		{name: "timeout", err: context.DeadlineExceeded, wantUnavailable: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := supervisedProcessCheck{name: "worker", sample: func(context.Context, process.SupervisedProcessSpec) (process.SupervisedProcessSample, error) {
				return process.SupervisedProcessSample{WorkerPID: 21, SupervisorPID: 22}, tt.err
			}}
			result := c.Run(t.Context())
			if result.OK != tt.wantOK || result.Unavailable != tt.wantUnavailable {
				t.Fatalf("result %+v", result)
			}
		})
	}
}

func TestSupervisedProcessConfiguration(t *testing.T) {
	entry := map[string]any{CheckKeyExe: "/usr/bin/worker", CheckKeyUser: "worker", CheckKeyCgroup: "/openrc.worker", CheckKeySupervisor: map[string]any{CheckKeyExe: "/usr/bin/supervisor", CheckKeyUser: "root"}}
	if _, err := SupervisedProcessConfig(entry); err != nil {
		t.Fatal(err)
	}
	entry[CheckKeyRespawnGrace] = "10s"
	if _, err := SupervisedProcessConfig(entry); err == nil {
		t.Fatal("accepted grace that exhausts timeout")
	}
	entry[CheckKeyTimeout] = "15s"
	if _, err := SupervisedProcessConfig(entry); err != nil {
		t.Fatal(err)
	}
	entry[CheckKeyCgroup] = "/"
	if _, err := SupervisedProcessConfig(entry); err == nil {
		t.Fatal("accepted root cgroup")
	}
}
