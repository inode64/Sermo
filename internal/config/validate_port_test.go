package config

import (
	"fmt"
	"slices"
	"testing"
)

func TestValidateOptionalTCPPort(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]any
		want   []string
	}{
		{name: "absent", fields: map[string]any{}},
		{name: "integer", fields: map[string]any{"port": 443}},
		{name: "string", fields: map[string]any{"port": "443"}},
		{name: "zero", fields: map[string]any{"port": 0}, want: []string{`checks.test.port "0" must be an integer in 1..65535`}},
		{name: "too high", fields: map[string]any{"port": 65536}, want: []string{`checks.test.port "65536" must be an integer in 1..65535`}},
		{name: "not numeric", fields: map[string]any{"port": "https"}, want: []string{`checks.test.port "https" must be an integer in 1..65535`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			validateOptionalTCPPort("checks.test", tt.fields, func(format string, args ...any) {
				got = append(got, fmt.Sprintf(format, args...))
			})
			if !slices.Equal(got, tt.want) {
				t.Fatalf("issues = %v, want %v", got, tt.want)
			}
		})
	}
}
