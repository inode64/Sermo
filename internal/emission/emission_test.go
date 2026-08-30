package emission

import "testing"

func TestMerge(t *testing.T) {
	tests := []struct {
		name     string
		raw      any
		fallback Policy
		want     Policy
	}{
		{
			name:     "non-map keeps fallback",
			raw:      []any{ModeEveryCycle},
			fallback: Policy{Events: "invalid", Notify: ""},
			want:     Policy{Events: "invalid", Notify: ""},
		},
		{
			name:     "partial valid override",
			raw:      map[string]any{KeyEvents: ModeEveryCycle},
			fallback: Default(),
			want:     Policy{Events: ModeEveryCycle, Notify: ModeOnChange},
		},
		{
			name: "invalid values are ignored",
			raw: map[string]any{
				KeyEvents: "sometimes",
				KeyNotify: 42,
			},
			fallback: Policy{Events: ModeOnChange, Notify: ModeEveryCycle},
			want:     Policy{Events: ModeOnChange, Notify: ModeEveryCycle},
		},
		{
			name: "both valid values override",
			raw: map[string]any{
				KeyEvents: ModeOnChange,
				KeyNotify: ModeEveryCycle,
			},
			fallback: Policy{},
			want:     Policy{Events: ModeOnChange, Notify: ModeEveryCycle},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Merge(tt.raw, tt.fallback); got != tt.want {
				t.Fatalf("Merge() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
