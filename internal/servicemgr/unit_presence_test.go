package servicemgr

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
)

func TestNetworkdOptionalUnitPresence(t *testing.T) {
	const query = "systemctl show -p LoadState --value -- storage.service"
	for _, tc := range []struct {
		name, state      string
		exit             int
		err              error
		present, wantErr bool
	}{
		{name: "loaded", state: "loaded\n", present: true},
		{name: "missing", state: "not-found\n"},
		{name: "masked is not absent", state: "masked\n", present: true},
		{name: "broken is not absent", state: "bad-setting\n", present: true},
		{name: "empty", wantErr: true},
		{name: "query failure", state: "not-found\n", exit: 1, wantErr: true},
		{name: "permission", err: errors.New("permission denied"), wantErr: true},
		{name: "timeout", err: context.DeadlineExceeded, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &execxtest.Runner{ByLine: map[string]execx.Result{query: {Stdout: tc.state, ExitCode: tc.exit}}, Errs: map[string]error{query: tc.err}}
			got, err := (systemdManager{runner: runner}).UnitExists(t.Context(), "storage")
			if got != tc.present || (err != nil) != tc.wantErr {
				t.Fatalf("presence=%v err=%v", got, err)
			}
		})
	}
}

func TestOpenRCOptionalUnitPresence(t *testing.T) {
	for _, tc := range []struct {
		name, unit               string
		readErr                  error
		cancel, present, wantErr bool
	}{
		{name: "present", unit: "helper", present: true},
		{name: "absent", unit: "helper", readErr: fs.ErrNotExist},
		{name: "unreadable", unit: "helper", readErr: fs.ErrPermission, wantErr: true},
		{name: "unsafe path", unit: "../helper", wantErr: true},
		{name: "canceled", unit: "helper", cancel: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			mgr := openrcManager{readFile: func(path string) ([]byte, error) {
				if path != "/etc/init.d/helper" {
					t.Fatalf("path=%s", path)
				}
				return nil, tc.readErr
			}}
			got, err := mgr.UnitExists(ctx, tc.unit)
			if got != tc.present || (err != nil) != tc.wantErr {
				t.Fatalf("presence=%v err=%v", got, err)
			}
		})
	}
}
