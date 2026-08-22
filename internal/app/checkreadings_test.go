package app

import (
	"testing"

	"sermo/internal/checks"
	"sermo/internal/conn"
)

// TestCheckReadingsForAllTypes consolidates the former per-group checkReadings
// tests: for each check type it builds the readings and asserts the formatted
// field values (and, for cert, a minimum reading count).
// TestMeterChecksDoNotRepeatTheirGaugeAsReadings pins which side of the split a
// numeric check falls on once its type declares graph metrics. A count-vs-limit
// check the panel draws as a gauge must add no rows: the meter already states the
// count, the limit and the utilisation. One with no gauge must add them, because
// otherwise its current sample appears nowhere at all.
func TestMeterChecksDoNotRepeatTheirGaugeAsReadings(t *testing.T) {
	gauged := map[string]any{
		checks.DataKeyAllocated: uint64(2048), checks.DataKeyCount: uint64(1821),
		checks.DataKeyMax: uint64(65536), checks.DataKeyUsedPct: 3.1,
	}
	// The same sample without a ceiling: no percentage, so no gauge, so the count
	// has to read out as a value instead of vanishing.
	unbounded := map[string]any{checks.DataKeyAllocated: uint64(879116), checks.DataKeyCount: uint64(1821)}
	for _, typ := range []string{checks.CheckTypeFDS, checks.CheckTypePIDs, checks.CheckTypeConntrack} {
		t.Run(typ+" gauge only", func(t *testing.T) {
			if got := checkReadings(typ, gauged); len(got) != 0 {
				t.Fatalf("%s repeats its meter as %d readings: %+v", typ, len(got), got)
			}
		})
		t.Run(typ+" reads out when unbounded", func(t *testing.T) {
			if got := checkReadings(typ, unbounded); len(got) == 0 {
				t.Fatalf("%s has no gauge without a ceiling, so its count must read out", typ)
			}
		})
	}
	for _, tc := range []struct {
		typ  string
		data map[string]any
	}{
		{checks.CheckTypeZombies, map[string]any{checks.DataKeyZombies: uint64(3)}},
	} {
		t.Run(tc.typ+" reads out", func(t *testing.T) {
			if got := checkReadings(tc.typ, tc.data); len(got) == 0 {
				t.Fatalf("%s has no gauge, so its sample must show as a reading", tc.typ)
			}
		})
	}
}

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
			// source: chrony adds the local daemon's own diagnostics to the row.
			name: "clock via a local chronyd",
			typ:  "clock",
			data: map[string]any{
				"socket":                "/run/chrony/chronyd.sock",
				"protocol":              "chrony",
				"synchronized":          "true",
				"offset_seconds":        0.000044694,
				"offset_abs_seconds":    0.000044694,
				"stratum":               3,
				"skew_ppm":              0.024,
				"frequency_ppm":         2.56,
				"sources_online":        4.0,
				"sources_unresolved":    0.0,
				"reference_age_seconds": 90.0,
			},
			want: map[string]string{
				"socket":                "/run/chrony/chronyd.sock",
				"stratum":               "3",
				"synchronized":          "true",
				"skew_ppm":              "0.024 ppm",
				"frequency_ppm":         "2.560 ppm",
				"sources_online":        "4",
				"reference_age_seconds": "90 s",
			},
		},
		{
			name: "firewall_rules",
			typ:  "firewall_rules",
			data: map[string]any{"backend": "nftables", "rules": uint64(99), "min_rules": 1},
			want: map[string]string{"rules": "99"},
		},
		{
			// Which unit failed is the actionable half: a unit with no catalog
			// profile appears nowhere else in the dashboard.
			name: "failed_units",
			typ:  checks.CheckTypeFailedUnits,
			data: map[string]any{
				checks.DataKeyBackend: "systemd",
				checks.DataKeyCount:   uint64(2),
				checks.DataKeyUnits:   "backup_kvm.service, cleanup.timer",
			},
			want: map[string]string{"count": "2", "units": "backup_kvm.service, cleanup.timer"},
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
			name: "dbus named service",
			typ:  conn.ProtocolNameDBus,
			data: map[string]any{
				checks.DataKeyProtocol:          conn.ProtocolNameDBus,
				checks.DataKeyDBusAddress:       "unix:path=/run/dbus/system_bus_socket",
				checks.DataKeyDBusBusID:         "bus-id",
				checks.DataKeyDBusUniqueName:    ":1.50",
				checks.DataKeyDBusBusName:       "org.libvirt",
				checks.DataKeyDBusObjectPath:    "/org/libvirt",
				checks.DataKeyDBusOwner:         ":1.42",
				checks.DataKeyDBusProbe:         conn.DBusProbeProperty,
				checks.DataKeyDBusInterface:     "org.libvirt.Connect",
				checks.DataKeyDBusProperty:      "Version",
				checks.DataKeyDBusPropertyValue: "1002003",
			},
			want: map[string]string{
				checks.DataKeyDBusAddress:       "unix:path=/run/dbus/system_bus_socket",
				checks.DataKeyDBusBusID:         "bus-id",
				checks.DataKeyDBusUniqueName:    ":1.50",
				checks.DataKeyDBusBusName:       "org.libvirt",
				checks.DataKeyDBusObjectPath:    "/org/libvirt",
				checks.DataKeyDBusOwner:         ":1.42",
				checks.DataKeyDBusProbe:         conn.DBusProbeProperty,
				checks.DataKeyDBusInterface:     "org.libvirt.Connect",
				checks.DataKeyDBusProperty:      "Version",
				checks.DataKeyDBusPropertyValue: "1002003",
			},
			minCount: 11,
		},
		{
			name: "tcp connections",
			typ:  "tcp_connections",
			data: map[string]any{"port": 21, "count": 12},
			want: map[string]string{"port": "21", "count": "12 connections"},
		},
		{
			name: "ssh idle exposes session protections",
			typ:  "ssh_idle",
			data: map[string]any{"count": 2, "protected_count": 1, "oldest_idle_seconds": 1860.0},
			want: map[string]string{"count": "2 sessions", "protected_count": "1 sessions", "oldest_idle_seconds": "31m"},
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
			name: "gluster cluster",
			typ:  checks.CheckTypeGlusterCluster,
			data: map[string]any{
				checks.DataKeyGlusterPeersConnected:    2,
				checks.DataKeyGlusterPeersExpected:     2,
				checks.DataKeyGlusterVolumesStarted:    2,
				checks.DataKeyGlusterVolumesExpected:   2,
				checks.DataKeyGlusterBricksOnline:      6,
				checks.DataKeyGlusterBricksExpected:    6,
				checks.DataKeyGlusterSelfHealOnline:    6,
				checks.DataKeyGlusterSelfHealTotal:     6,
				checks.DataKeyGlusterHealEntries:       0,
				checks.DataKeyGlusterSplitBrainEntries: 0,
				checks.DataKeyGlusterPeersDisconnected: []string{"zeus"},
				checks.DataKeyGlusterIssues:            []string{"peer zeus is disconnected"},
			},
			want: map[string]string{
				checks.DataKeyGlusterPeersConnected:    "2",
				checks.DataKeyGlusterBricksOnline:      "6",
				checks.DataKeyGlusterHealEntries:       "0",
				checks.DataKeyGlusterPeersDisconnected: "zeus",
				checks.DataKeyGlusterIssues:            "peer zeus is disconnected",
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
			name: "smart names the drive it sampled",
			typ:  "smart",
			data: map[string]any{
				"device": "/dev/sdb", "health": "PASSED",
				"model": "WDC WD20EFRX-68EUZN0", "serial_number": "WD-WCC4M4SZ375K",
				"firmware": "82.00A82", "wwn": "0x50014ee2636af963",
				"capacity_bytes": uint64(2000398934016), "temperature": float64(41),
				"pending_sectors": float64(3), "crc_errors": float64(7),
				"power_cycles": 137, "self_test": "Completed without error at 3468 h",
			},
			want: map[string]string{
				"model": "WDC WD20EFRX-68EUZN0", "serial_number": "WD-WCC4M4SZ375K",
				"firmware": "82.00A82", "wwn": "0x50014ee2636af963",
				"capacity_bytes": "1.82 TiB", "pending_sectors": "3", "crc_errors": "7",
				"power_cycles": "137", "self_test": "Completed without error at 3468 h",
			},
		},
		{
			// A drive that stopped answering still reports what it is and what it
			// last said; the retained rows are labelled so they cannot be read as
			// a current sample.
			name: "smart keeps the last known readings of a missing drive",
			typ:  "smart",
			data: map[string]any{
				"device": "/dev/sda", "health": "missing", "device_state": "missing",
				"model": "WDC WD20EFRX-68E", "last_health": "PASSED",
				"last_seen_seconds": float64(7200), "last_temperature": float64(41),
			},
			want: map[string]string{
				"model": "WDC WD20EFRX-68E", "last_seen_seconds": "2h",
				"last_health": "PASSED", "last_temperature": "41 °C",
			},
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
			name:     "redis renders connected clients",
			typ:      "redis",
			data:     map[string]any{"protocol": "redis", "connected_clients": 12},
			want:     map[string]string{"connected_clients": "12 connections"},
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
		{
			// The growth trio both window checks share: a rise is signed, so the
			// reading says "+3" rather than a bare total.
			name: "count growth window",
			typ:  checks.CheckTypeCount,
			data: map[string]any{
				checks.DataKeyPath: "/var/spool", checks.DataKeyOf: "file",
				checks.DataKeyCount: 4, checks.DataKeyBaselineCount: 1,
				checks.DataKeyGrowthCount: 3, checks.DataKeyWindow: "30m",
			},
			want: map[string]string{
				checks.DataKeyCount: "4", checks.DataKeyBaselineCount: "1",
				checks.DataKeyGrowthCount: "+3", checks.DataKeyWindow: "30m",
			},
			minCount: 6,
		},
		{
			// A count that fell keeps its sign too, so the reading cannot read as
			// growth.
			name: "strays growth window and holders",
			typ:  checks.CheckTypeStrays,
			data: map[string]any{
				checks.DataKeyCount: 2, checks.DataKeyBaselineCount: 6,
				checks.DataKeyGrowthCount: -4, checks.DataKeyWindow: "10m",
				checks.DataKeyPath: "/usr/bin/node", checks.DataKeyPIDs: "300,400",
			},
			want: map[string]string{
				checks.DataKeyBaselineCount: "6", checks.DataKeyGrowthCount: "-4",
				checks.DataKeyWindow: "10m", checks.DataKeyPath: "/usr/bin/node",
				checks.DataKeyPIDs: "300,400",
			},
			minCount: 6,
		},
		{
			// Without a growth bound the check publishes no window at all, and the
			// trio must simply not appear rather than render zeros.
			name: "strays without a growth bound",
			typ:  checks.CheckTypeStrays,
			data: map[string]any{checks.DataKeyCount: 3, checks.DataKeyPath: "/usr/bin/node"},
			want: map[string]string{
				checks.DataKeyBaselineCount: "", checks.DataKeyGrowthCount: "", checks.DataKeyWindow: "",
			},
			minCount: 2,
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

// TestNetCheckReadingsNameTheInterface pins that a net row says which card it
// is, not just which name the operator typed — the same reason a disk row
// carries a serial number.
func TestNetCheckReadingsNameTheInterface(t *testing.T) {
	readings := checkReadings(checks.CheckTypeNet, map[string]any{
		checks.DataKeyInterface:      "eth0",
		checks.DataKeyMetric:         checks.NetMetricState,
		checks.DataKeyValue:          "up",
		checks.DataKeyMAC:            "34:5a:60:00:1c:92",
		checks.DataKeyDriver:         "ice",
		checks.DataKeyBus:            "0000:0a:00.0",
		checks.DataKeyDuplex:         "full",
		checks.DataKeyMTU:            uint64(1500),
		checks.DataKeyCarrierChanges: uint64(7),
	})
	got := map[string]string{}
	for _, r := range readings {
		got[r.Label] = r.Value
	}
	for label, want := range map[string]string{
		"Interface": "eth0", "MAC": "34:5a:60:00:1c:92", "Driver": "ice",
		"Bus": "0000:0a:00.0", "Duplex": "full", "MTU": "1500", "Link changes": "7",
		"State": "up",
	} {
		if got[label] != want {
			t.Errorf("reading %q = %q, want %q (all: %v)", label, got[label], want, got)
		}
	}
}

// TestNetCheckReadingsOmitWhatAnInterfaceDoesNotHave keeps a bridge from
// reporting an empty driver, which would read as a driver that failed to load.
func TestNetCheckReadingsOmitWhatAnInterfaceDoesNotHave(t *testing.T) {
	readings := checkReadings(checks.CheckTypeNet, map[string]any{
		checks.DataKeyInterface: "docker0",
		checks.DataKeyMetric:    checks.NetMetricState,
		checks.DataKeyMAC:       "8a:fe:a7:f7:5a:a0",
		checks.DataKeyKind:      "bridge",
		checks.DataKeyMTU:       uint64(1500),
	})
	for _, r := range readings {
		if r.Label == "Driver" || r.Label == "Bus" || r.Label == "Duplex" {
			t.Errorf("a bridge published %q = %q", r.Label, r.Value)
		}
	}
	var kind string
	for _, r := range readings {
		if r.Label == "Kind" {
			kind = r.Value
		}
	}
	if kind != "bridge" {
		t.Errorf("kind = %q, want bridge", kind)
	}
}
