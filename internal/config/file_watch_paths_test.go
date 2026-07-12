package config

import (
	"reflect"
	"strings"
	"testing"

	"sermo/internal/checks"
)

func TestFileWatchPaths(t *testing.T) {
	tests := []struct {
		name    string
		check   map[string]any
		want    []string
		wantErr string
	}{
		{
			name:  "legacy path",
			check: map[string]any{checks.CheckKeyPath: "/srv/a"},
			want:  []string{"/srv/a"},
		},
		{
			name:  "paths list",
			check: map[string]any{checks.CheckKeyPaths: []any{"/srv/a", "/srv/b"}},
			want:  []string{"/srv/a", "/srv/b"},
		},
		{
			name:  "paths string slice",
			check: map[string]any{checks.CheckKeyPaths: []string{"/srv/a", "/srv/b"}},
			want:  []string{"/srv/a", "/srv/b"},
		},
		{
			name:    "both aliases",
			check:   map[string]any{checks.CheckKeyPath: "/srv/a", checks.CheckKeyPaths: []any{"/srv/b"}},
			wantErr: "only one",
		},
		{
			name:    "paths must be list",
			check:   map[string]any{checks.CheckKeyPaths: "/srv/a"},
			wantErr: "non-empty list",
		},
		{
			name:    "duplicate path",
			check:   map[string]any{checks.CheckKeyPaths: []any{"/srv/a", "/srv/a"}},
			wantErr: "duplicate",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FileWatchPaths(tt.check)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("FileWatchPaths() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("FileWatchPaths() = %v, want %v", got, tt.want)
			}
		})
	}
}
