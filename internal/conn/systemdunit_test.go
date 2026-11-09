package conn

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestSystemdUnitProperty(t *testing.T) {
	const unitPath = dbus.ObjectPath("/org/freedesktop/systemd1/unit/systemd_2dcoredump_2esocket")
	tests := []struct {
		name      string
		getUnit   *dbus.Call
		property  *dbus.Call
		want      any
		wantErr   string
		wantProbe bool
	}{
		{
			name:      "loaded unit",
			getUnit:   &dbus.Call{Body: []any{unitPath}},
			property:  &dbus.Call{Body: []any{dbus.MakeVariant(uint32(9))}},
			want:      uint32(9),
			wantProbe: true,
		},
		{
			// GetUnit, not LoadUnit: a unit systemd has not loaded is an error,
			// never loaded on the check's behalf.
			name:    "unit not loaded",
			getUnit: &dbus.Call{Err: errors.New("Unit systemd-coredump.socket not loaded.")},
			wantErr: "find systemd unit systemd-coredump.socket",
		},
		{
			name:      "property read fails",
			getUnit:   &dbus.Call{Body: []any{unitPath}},
			property:  &dbus.Call{Err: errors.New("Unknown property")},
			wantErr:   "read org.freedesktop.systemd1.Socket.NAccepted of systemd-coredump.socket",
			wantProbe: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manager := &recordingDBusCaller{body: tc.getUnit.Body, err: tc.getUnit.Err}
			unit := &recordingDBusCaller{}
			if tc.property != nil {
				unit.body, unit.err = tc.property.Body, tc.property.Err
			}
			probed := false
			got, err := systemdUnitProperty(context.Background(), manager, func(path dbus.ObjectPath) dbusMethodCaller {
				probed = true
				if path != unitPath {
					t.Fatalf("unit object path = %q, want %q", path, unitPath)
				}
				return unit
			}, "systemd-coredump.socket", "org.freedesktop.systemd1.Socket", "NAccepted")
			if manager.method != systemdGetUnit || manager.flags != dbusReadOnlyCallFlags {
				t.Fatalf("manager call = %s flags %v, want read-only %s", manager.method, manager.flags, systemdGetUnit)
			}
			if probed != tc.wantProbe {
				t.Fatalf("unit object probed = %v, want %v", probed, tc.wantProbe)
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("systemdUnitProperty() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("value = %v (%T), want %v", got, got, tc.want)
			}
			if unit.method != dbusPropertiesGet || unit.flags != dbusReadOnlyCallFlags {
				t.Fatalf("unit call = %s flags %v, want read-only %s", unit.method, unit.flags, dbusPropertiesGet)
			}
		})
	}
}
