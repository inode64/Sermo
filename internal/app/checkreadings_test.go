package app

import "testing"

// TestCheckReadingsForAllTypes consolidates the former per-group checkReadings
// tests: for each check type it builds the readings and asserts the formatted
// field values (and, for cert, a minimum reading count).
func TestCheckReadingsForAllTypes(t *testing.T) {
	cases := []struct {
		name     string
		typ      string
		data     map[string]any
		want     map[string]string // field -> formatted Value
		minCount int               // minimum number of readings (0 = unchecked)
	}{
		{
			name: "cert",
			typ:  "cert",
			data: map[string]any{
				"source":               "/etc/ssl/cert.pem",
				"days_left":            30,
				"not_after":            "2026-12-31T00:00:00Z",
				"issuer":               "Test CA",
				"public_key_algorithm": "ECDSA",
				"key_bits":             256,
				"dns_names":            []string{"example.com", "www.example.com"},
			},
			want:     map[string]string{"public_key_algorithm": "ECDSA", "key_bits": "256"},
			minCount: 6,
		},
		{
			name: "count",
			typ:  "count",
			data: map[string]any{"path": "/var/log", "of": "file", "count": 12},
			want: map[string]string{"count": "12"},
		},
		{
			name: "clock",
			typ:  "clock",
			data: map[string]any{
				"server":             "time.example",
				"offset_seconds":     -0.125,
				"offset_abs_seconds": 0.125,
				"stratum":            2,
				"root_dispersion_ms": 10.5,
				"reference_id":       "GPS",
			},
			want: map[string]string{"offset_seconds": "-0.125 s", "offset_abs_seconds": "0.125 s", "stratum": "2"},
		},
		{
			name: "firewall_rules",
			typ:  "firewall_rules",
			data: map[string]any{"backend": "nftables", "rules": uint64(99), "min_rules": 1},
			want: map[string]string{"rules": "99"},
		},
		{
			name: "file",
			typ:  "file",
			data: map[string]any{"path": "/etc/hosts", "size": int64(220)},
			want: map[string]string{"size": "220 B"},
		},
		{
			name: "tcp",
			typ:  "tcp",
			data: map[string]any{"host": "127.0.0.1", "port": 443, "latency_ms": int64(12), "protocol": "tcp"},
			want: map[string]string{"latency_ms": "12 ms"},
		},
		{
			name: "http",
			typ:  "http",
			data: map[string]any{"status": 200, "latency_ms": int64(45)},
			want: map[string]string{"status": "200", "latency_ms": "45 ms"},
		},
		{
			name: "storage",
			typ:  "storage",
			data: map[string]any{"path": "/", "used_pct": 88.5, "free_bytes": uint64(1 << 30)},
			want: map[string]string{"used_pct": "88.50%"},
		},
		{
			name: "pressure",
			typ:  "pressure",
			data: map[string]any{"some_avg60": 2.5, "value": 2.5},
			want: map[string]string{"some_avg60": "2.50%"},
		},
		{
			name: "diskio",
			typ:  "diskio",
			data: map[string]any{"device": "sda", "util_pct": 50.0, "read_bytes": 1024.0},
			want: map[string]string{"util_pct": "50.00%"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			readings := checkReadings(c.typ, c.data)
			if c.minCount > 0 && len(readings) < c.minCount {
				t.Fatalf("%s readings = %+v, want at least %d", c.typ, readings, c.minCount)
			}
			for field, want := range c.want {
				if got := readingByField(readings, field).Value; got != want {
					t.Fatalf("%s reading %q = %q, want %q (%+v)", c.typ, field, got, want, readings)
				}
			}
		})
	}
}

func TestCertCheckReadingsOmitSubjectAndEndWithIssuer(t *testing.T) {
	readings := certCheckReadings(map[string]any{
		"source":               "/etc/ssl/cert.pem",
		"days_left":            30,
		"not_after":            "2026-12-31T00:00:00Z",
		"issuer":               "Test CA",
		"public_key_algorithm": "ECDSA",
		"key_bits":             256,
		"subject":              "CN=example.com",
		"dns_names":            []string{"example.com", "www.example.com"},
	})
	if got := readingByField(readings, "subject").Value; got != "" {
		t.Fatalf("subject reading = %q, want omitted (%+v)", got, readings)
	}
	if len(readings) == 0 {
		t.Fatal("cert readings are empty")
	}
	last := readings[len(readings)-1]
	if last.Field != "issuer" || last.Value != "Test CA" {
		t.Fatalf("last reading = %+v, want issuer Test CA (%+v)", last, readings)
	}
}
