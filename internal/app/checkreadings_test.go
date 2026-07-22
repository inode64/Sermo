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
			// Byte counts and rates render through the canonical byte formatter
			// (IEC units, comma thousands, dot decimal) on every surface.
			name: "diskio canonical byte rates",
			typ:  "diskio",
			data: map[string]any{"device": "sda", "util_pct": 50.0, "read_bytes": 1024.0, "write_bytes": 2555904.0, "await_ms": 1.5},
			want: map[string]string{"read_bytes": "1.02 KB/s", "write_bytes": "2.56 MB/s", "util_pct": "50%"},
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
			data: map[string]any{"path": "/etc/hosts", "size": int64(220), "age": "2d3h"},
			want: map[string]string{"size": "220 B", "age": "2d3h"},
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
			want: map[string]string{"used_pct": "88.5%"},
		},
		{
			name: "pressure",
			typ:  "pressure",
			data: map[string]any{"some_avg60": 2.5, "value": 2.5},
			want: map[string]string{"some_avg60": "2.5%"},
		},
		{
			name: "raid",
			typ:  "raid",
			data: map[string]any{"arrays": 1, "degraded": 0, "recovering": 1, "array": "md0", "raid_operation": "recovery", "raid_progress_pct": 12.6, "total_bytes": uint64(50)},
			want: map[string]string{"raid_progress_pct": "12.6%", "total_bytes": "50 B"},
		},
		{
			name: "lvm",
			typ:  "lvm",
			data: map[string]any{
				"health":         "ok",
				"volume_group":   "vg0",
				"logical_volume": "root",
				"lvm_reasons":    "",
				"vg_free_bytes":  float64(50),
				"vg_size_bytes":  float64(1000),
				"vg_used_bytes":  float64(950),
				"free_pct":       5.0,
			},
			want: map[string]string{
				"volume_group":   "vg0",
				"logical_volume": "root",
				"lvm_reasons":    "none",
				"vg_free_bytes":  "50 B",
				"free_pct":       "5.0%",
			},
		},
		{
			name: "net exposes observed metric",
			typ:  "net",
			data: map[string]any{"interface": "eth0", "metric": "errors", "value": 3, "total": 51},
			want: map[string]string{"interface": "eth0", "errors": "3 (total 51)"},
		},
		{
			name: "smart formats power-on time as a duration",
			typ:  "smart",
			data: map[string]any{"power_on_hours": float64(12000)},
			want: map[string]string{"power_on_hours": "16mo 20d"},
		},
		{
			name: "sql exposes observed scalar and condition",
			typ:  "sql",
			data: map[string]any{"result": "51", "op": ">", "threshold": "50"},
			want: map[string]string{"result": "51", "threshold": "> 50"},
		},
		{
			// process_count/users store their count as an int; the reading must
			// still render (regression: a bare float64 assertion dropped it).
			name:     "process_count renders its integer count",
			typ:      "process_count",
			data:     map[string]any{"count": 12, "value": float64(12)},
			want:     map[string]string{"count": "12 processes"},
			minCount: 1,
		},
		{
			name:     "users renders its integer count",
			typ:      "users",
			data:     map[string]any{"count": 3, "value": float64(3)},
			want:     map[string]string{"count": "3 users"},
			minCount: 1,
		},
		{
			// numericData also coerces uint64 (the type level count checks such
			// as fds/pids/conntrack use), not only int, so a graphable metric
			// stored unsigned still renders.
			name:     "users renders an unsigned count",
			typ:      "users",
			data:     map[string]any{"count": uint64(5)},
			want:     map[string]string{"count": "5 users"},
			minCount: 1,
		},
		{
			// A metric check surfaces the observed value with its unit, labelled
			// by the metric, instead of only its event message.
			name:     "metric exposes the observed value",
			typ:      "metric",
			data:     map[string]any{"type": "metric", "scope": "host", "metric": "cpu", "op": ">", "threshold": "80", "value": 82.5, "unit": "%"},
			want:     map[string]string{"value": "82.5%"},
			minCount: 1,
		},
		{
			// Rehydrated from the JSON state store, a cert's DNS names arrive as
			// []any, not []string; the reading must still render (regression:
			// the bare []string assertion dropped it after a daemon restart).
			name:     "cert dns names survive json hydration",
			typ:      "cert",
			data:     map[string]any{"source": "/c.pem", "dns_names": []any{"example.com", "www.example.com"}},
			want:     map[string]string{"dns_names": "example.com, www.example.com"},
			minCount: 1,
		},
		{
			// Same hydration path for RAID members: []any of maps, not
			// []RaidArrayStatus. The per-array reading must still render.
			name: "raid members survive json hydration",
			typ:  "raid",
			data: map[string]any{"arrays": 1, "raid_members": []any{
				map[string]any{"Name": "md0", "Degraded": false, "Operation": "recovery", "HasProgress": true, "ProgressPct": 12.6},
			}},
			want:     map[string]string{"raid_array_md0": "good · recovery 12.6%"},
			minCount: 1,
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
