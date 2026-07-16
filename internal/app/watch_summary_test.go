package app

import (
	"testing"
	"time"

	"sermo/internal/checks"
)

func TestAddSummaryAge(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want time.Duration
		ok   bool
	}{
		{name: "valid", env: map[string]string{sermoEnvAgeSeconds: "90"}, want: 90 * time.Second, ok: true},
		{name: "missing"},
		{name: "invalid", env: map[string]string{sermoEnvAgeSeconds: "nope"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := map[string]any{}
			addSummaryAge(data, tc.env)
			age, ok := data[checks.DataKeyAge]
			if ok != tc.ok {
				t.Fatalf("age present = %v, want %v; data=%v", ok, tc.ok, data)
			}
			if tc.ok && (age != tc.want || data[checks.DataKeyValue] != tc.want) {
				t.Fatalf("age data = %v, want %v", data, tc.want)
			}
		})
	}
}
