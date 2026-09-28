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
			want: ServiceLifecycle{ProcessMode: ServiceProcessInit},
		},
		{
			name: "explicit resident",
			tree: map[string]any{SectionProcesses: map[string]any{"main": map[string]any{"exe": "/usr/sbin/demo"}}},
			want: ServiceLifecycle{ProcessMode: ServiceProcessResident},
		},
		{
			name: "no resident process",
			tree: map[string]any{SectionProcesses: map[string]any{}},
			want: ServiceLifecycle{ProcessMode: ServiceProcessNone},
		},
		{
			name: "named pidfile is resident",
			tree: map[string]any{SectionProcesses: map[string]any{}, ServiceKeyPidfiles: map[string]any{"main": "/run/demo.pid"}},
			want: ServiceLifecycle{ProcessMode: ServiceProcessResident},
		},
		{
			name: "full operational contract",
			tree: map[string]any{
				ServiceKeyAlsoService: map[string]any{backendSystemd: []any{"demo.socket"}},
				SectionProcesses: map[string]any{
					"main":   map[string]any{"exe": "/usr/bin/demo"},
					"worker": map[string]any{"exe": "/usr/bin/demo-worker"},
				},
			},
			want: ServiceLifecycle{
				ProcessMode:    ServiceProcessResident,
				AuxiliaryUnits: []string{"demo.socket"},
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
			if got.ProcessMode != tt.want.ProcessMode ||
				!slices.Equal(got.AuxiliaryUnits, tt.want.AuxiliaryUnits) {
				t.Fatalf("lifecycle = %+v, want %+v", got, tt.want)
			}
		})
	}
}
