package conn

import (
	"strings"
	"testing"
)

func TestWireFieldsRejectValuesThatDoNotFit(t *testing.T) {
	tests := []struct {
		name  string
		n     int
		fits  bool
		field func(int) error
	}{
		{name: "byte max", n: wireByteMax, fits: true, field: func(n int) error { _, err := wireByte("p", "f", n); return err }},
		{name: "byte overflow", n: wireByteMax + 1, field: func(n int) error { _, err := wireByte("p", "f", n); return err }},
		{name: "uint16 max", n: wireUint16Max, fits: true, field: func(n int) error { _, err := wireUint16("p", "f", n); return err }},
		{name: "uint16 overflow", n: wireUint16Max + 1, field: func(n int) error { _, err := wireUint16("p", "f", n); return err }},
		{name: "uint32 zero", n: 0, fits: true, field: func(n int) error { _, err := wireUint32("p", "f", n); return err }},
		{name: "negative", n: -1, field: func(n int) error { _, err := wireUint32("p", "f", n); return err }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.field(tc.n)
			if tc.fits && err != nil {
				t.Fatalf("%d must fit: %v", tc.n, err)
			}
			if !tc.fits && (err == nil || !strings.Contains(err.Error(), "does not fit")) {
				t.Fatalf("%d must be rejected, got %v", tc.n, err)
			}
		})
	}
}
