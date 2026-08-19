package config

import (
	"slices"
	"testing"
)

func TestResolveServiceLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		tree map[string]any
		want ServiceLifecycle
	}{
		{
			name: "init discovered default",
			tree: map[string]any{},
			want: ServiceLifecycle{ProcessMode: ServiceProcessInit, ControlMode: ServiceControlInit, RestartMode: RestartModeStaged},
		},
		{
			name: "explicit resident",
			tree: map[string]any{SectionProcesses: map[string]any{"main": map[string]any{"exe": "/usr/sbin/demo"}}},
			want: ServiceLifecycle{ProcessMode: ServiceProcessResident, ControlMode: ServiceControlInit, RestartMode: RestartModeStaged},
		},
		{
			name: "no resident process",
			tree: map[string]any{SectionProcesses: map[string]any{}},
			want: ServiceLifecycle{ProcessMode: ServiceProcessNone, ControlMode: ServiceControlInit, RestartMode: RestartModeStaged},
		},
		{
			name: "named pidfile is resident",
			tree: map[string]any{SectionProcesses: map[string]any{}, ServiceKeyPidfiles: map[string]any{"main": "/run/demo.pid"}},
			want: ServiceLifecycle{ProcessMode: ServiceProcessResident, ControlMode: ServiceControlInit, RestartMode: RestartModeStaged},
		},
		{
			name: "external control",
			tree: map[string]any{SectionControl: map[string]any{EntryKeyType: "docker"}},
			want: ServiceLifecycle{ProcessMode: ServiceProcessInit, ControlMode: ServiceControlExternal, RestartMode: RestartModeStaged},
		},
		{
			name: "full operational contract",
			tree: map[string]any{
				keyAllowDependencies: true,
				ServiceKeyRestartPolicy: map[string]any{
					RestartPolicyKeyMode: string(RestartModeNative),
				},
				ServiceKeyAlsoService: map[string]any{backendSystemd: []any{"demo.socket"}},
				SectionProcesses: map[string]any{
					"main": map[string]any{"exe": "/usr/bin/demo"},
					"worker": map[string]any{
						"exe":                   "/usr/bin/demo-worker",
						processDelegatedTestKey: true,
					},
				},
			},
			want: ServiceLifecycle{
				ProcessMode:       ServiceProcessResident,
				ControlMode:       ServiceControlInit,
				RestartMode:       RestartModeNative,
				AuxiliaryUnits:    []string{"demo.socket"},
				AllowDependencies: true,
				SocketActivated:   true,
				DelegatedRoles:    []string{"worker"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveServiceLifecycle(tt.tree, backendSystemd)
			if err != nil {
				t.Fatalf("ResolveServiceLifecycle: %v", err)
			}
			if got.ProcessMode != tt.want.ProcessMode || got.ControlMode != tt.want.ControlMode ||
				got.RestartMode != tt.want.RestartMode || got.AllowDependencies != tt.want.AllowDependencies ||
				got.SocketActivated != tt.want.SocketActivated ||
				!slices.Equal(got.AuxiliaryUnits, tt.want.AuxiliaryUnits) ||
				!slices.Equal(got.DelegatedRoles, tt.want.DelegatedRoles) {
				t.Fatalf("lifecycle = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestResolveServiceLifecycleSocketActivationIsSystemdOnly(t *testing.T) {
	t.Parallel()
	tree := map[string]any{ServiceKeyService: map[string]any{
		backendSystemd: []any{"demo.socket"},
		backendOpenRC:  []any{"demo"},
	}}
	systemd, err := ResolveServiceLifecycle(tree, backendSystemd)
	if err != nil {
		t.Fatal(err)
	}
	openrc, err := ResolveServiceLifecycle(tree, backendOpenRC)
	if err != nil {
		t.Fatal(err)
	}
	if !systemd.SocketActivated || openrc.SocketActivated {
		t.Fatalf("socket activation: systemd=%t openrc=%t", systemd.SocketActivated, openrc.SocketActivated)
	}
}

const processDelegatedTestKey = "delegated"
