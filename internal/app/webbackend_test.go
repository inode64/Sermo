package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/volume"
	web "sermo/internal/web"
)

func TestWebBackendEventsNilLog(t *testing.T) {
	b := &WebBackend{
		entries: map[string]*webEntry{"web": {}},
	}
	if got := b.Events(context.Background(), 10); got != nil {
		t.Fatalf("Events with nil log = %v, want nil", got)
	}
	events, ok := b.ServiceEvents(context.Background(), "web", 10)
	if !ok {
		t.Fatal("ServiceEvents should find configured service")
	}
	if events != nil {
		t.Fatalf("ServiceEvents with nil log = %v, want nil", events)
	}
	if _, ok := b.ServiceEvents(context.Background(), "missing", 10); ok {
		t.Fatal("unknown service must not be found")
	}
}

func TestWebBackendDetailRanFlag(t *testing.T) {
	snaps := NewSnapshots()
	snaps.Publish("web", map[string]checks.Result{
		"fast": {Check: "fast", OK: true, Message: "ok"},
		"slow": {Check: "slow", OK: true, Message: "cached"},
	}, map[string]bool{"fast": true})

	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {
				displayName: "web",
				checkNames:  []string{"fast", "slow"},
				checkTypes:  map[string]string{"fast": "tcp", "slow": "http"},
				status:      func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
			},
		},
		snapshots: snaps,
	}

	detail, ok := b.Detail(context.Background(), "web")
	if !ok {
		t.Fatal("detail not found")
	}
	byName := map[string]bool{}
	for _, c := range detail.Checks {
		byName[c.Name] = c.Ran
	}
	if !byName["fast"] {
		t.Fatal("fast check should show ran=true in web detail")
	}
	if byName["slow"] {
		t.Fatal("interval-cached slow check should show ran=false in web detail")
	}
}

func TestWebBackendViewMonitorSource(t *testing.T) {
	at := time.Date(2026, 6, 7, 14, 0, 0, 0, time.UTC)
	store := newFakeStore()
	store.now = func() time.Time { return at }
	if err := store.SetActive("web", false, state.SourceCLI); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {unit: "nginx", backend: "systemd"},
		},
		store: store,
	}

	svc := b.view(context.Background(), "web", b.entries["web"])
	if svc.Monitored || svc.MonitorSource != state.SourceCLI {
		t.Fatalf("service = %+v", svc)
	}
	wantAt := at.UTC().Format(time.RFC3339)
	if svc.MonitorChangedAt != wantAt {
		t.Fatalf("MonitorChangedAt = %q, want %q", svc.MonitorChangedAt, wantAt)
	}
}

func TestWebBackendViewInterval(t *testing.T) {
	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {unit: "nginx", backend: "systemd", interval: 10 * time.Second},
		},
	}
	svc := b.view(context.Background(), "web", b.entries["web"])
	if svc.Interval != "10s" {
		t.Fatalf("Interval = %q, want %q", svc.Interval, "10s")
	}
}

func TestWebBackendMonitoringStatusAvoidsServiceViewWork(t *testing.T) {
	store := newFakeStore()
	if err := store.SetActive("paused", false, state.SourceCLI); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	statusCalls := 0
	b := &WebBackend{
		order: []string{"active", "paused", "disabled"},
		entries: map[string]*webEntry{
			"active": {
				status: func(context.Context) (servicemgr.Status, error) {
					statusCalls++
					return servicemgr.StatusActive, nil
				},
			},
			"paused": {
				status: func(context.Context) (servicemgr.Status, error) {
					statusCalls++
					return servicemgr.StatusActive, nil
				},
			},
			"disabled": {disabled: true},
		},
		store: store,
	}

	got := b.MonitoringStatus(context.Background())
	if got.Total != 3 || got.Monitored != 1 || got.Paused != 2 {
		t.Fatalf("MonitoringStatus = %+v, want total=3 monitored=1 paused=2", got)
	}
	if statusCalls != 0 {
		t.Fatalf("MonitoringStatus called service status %d times, want 0", statusCalls)
	}
}

func TestWebBackendLastEventIndexes(t *testing.T) {
	events := NewEventLog(10)
	t0 := time.Date(2026, 6, 7, 14, 0, 0, 0, time.UTC)
	add := func(at time.Time, e Event) {
		events.now = func() time.Time { return at }
		events.Add(e)
	}
	add(t0, Event{Service: "web", Kind: "action", Action: "start", Status: "ok"})
	add(t0.Add(time.Minute), Event{Service: "db", Kind: "action", Action: "restart", Status: "ok"})
	add(t0.Add(2*time.Minute), Event{Watch: "disk", Kind: "notify", Message: "sent"})
	add(t0.Add(3*time.Minute), Event{Watch: "disk", Kind: "error", Message: "ignored"})
	add(t0.Add(4*time.Minute), Event{Service: "web", Kind: "action", Action: "restart", Status: "blocked"})
	add(t0.Add(5*time.Minute), Event{Watch: "disk", Kind: "hook-failed", Message: "failed"})

	b := &WebBackend{events: events}

	services := b.lastServiceEvents()
	if got := services["web"]; got == nil || got.Action != "restart" || got.Status != "blocked" {
		t.Fatalf("web last event = %+v, want restart/blocked", got)
	}
	if got := services["db"]; got == nil || got.Action != "restart" || got.Status != "ok" {
		t.Fatalf("db last event = %+v, want restart/ok", got)
	}

	activities := b.lastWatchActivities()
	wantAt := t0.Add(5 * time.Minute).Format(time.RFC3339)
	if got := activities["disk"]; got.Kind != "hook-failed" || got.At != wantAt {
		t.Fatalf("disk activity = %+v, want hook-failed at %s", got, wantAt)
	}
}

func TestWebBackendApplicationsCache(t *testing.T) {
	calls := 0
	b := &WebBackend{
		applicationsList: func(context.Context) []web.Application {
			calls++
			name := "first"
			if calls > 1 {
				name = "second"
			}
			return []web.Application{{Name: name}}
		},
	}

	first := b.Applications(context.Background())
	if calls != 1 || len(first) != 1 || first[0].Name != "first" {
		t.Fatalf("first Applications = %v, calls=%d", first, calls)
	}
	first[0].Name = "mutated"

	second := b.Applications(context.Background())
	if calls != 1 || len(second) != 1 || second[0].Name != "first" {
		t.Fatalf("cached Applications = %v, calls=%d; want cached first", second, calls)
	}

	b.applicationsAt = time.Now().Add(-applicationsCacheTTL - time.Nanosecond)
	third := b.Applications(context.Background())
	if calls != 2 || len(third) != 1 || third[0].Name != "second" {
		t.Fatalf("expired Applications = %v, calls=%d; want refreshed second", third, calls)
	}
}

func TestWebBackendWatchPolarityUsesSharedHealthTypes(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"autofs": map[string]any{"check": map[string]any{"type": "autofs"}},
			"count":  map[string]any{"check": map[string]any{"type": "count"}},
			"mysql":  map[string]any{"check": map[string]any{"type": "mysql"}},
			"ports":  map[string]any{"check": map[string]any{"type": "ports"}},
			"ws":     map[string]any{"check": map[string]any{"type": "ws"}},
		},
	}}}

	b, warns := NewWebBackend(cfg, Deps{})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	got := map[string]bool{}
	for _, w := range b.Watches(context.Background()) {
		got[w.Name] = w.FireOnFail
	}
	for _, name := range []string{"autofs", "mysql", "ports", "ws"} {
		if !got[name] {
			t.Fatalf("%s watch should be health-style: %v", name, got)
		}
	}
	if got["count"] {
		t.Fatalf("count watch should be condition-style: %v", got)
	}
}

func TestWebBackendWatchesExposeMonitorMode(t *testing.T) {
	store := newFakeStore()
	if err := store.SetActive(watchMonitorKey("disk-root"), false, state.SourceConfig); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"disk-root": map[string]any{
				"monitor": config.MonitorDisabled,
				"check":   map[string]any{"type": "disk", "path": "/"},
			},
		},
	}}}

	b, warns := NewWebBackend(cfg, Deps{Monitor: store})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	watches := b.Watches(context.Background())
	if len(watches) != 1 {
		t.Fatalf("got %d watches", len(watches))
	}
	if watches[0].Monitor != config.MonitorDisabled || watches[0].Monitored {
		t.Fatalf("watch monitor view = %+v", watches[0])
	}
}

func TestWebBackendKernelWatchReadings(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"mem-pressure": map[string]any{"check": map[string]any{
				"type":       "pressure",
				"resource":   "memory",
				"some_avg60": map[string]any{"op": ">", "value": 10},
			}},
			"entropy": map[string]any{"check": map[string]any{
				"type":  "entropy",
				"avail": map[string]any{"op": "<", "value": 200},
			}},
			"zombies": map[string]any{"check": map[string]any{
				"type":  "zombies",
				"count": map[string]any{"op": ">", "value": 20},
			}},
			"conntrack": map[string]any{"check": map[string]any{
				"type":  "conntrack",
				"count": map[string]any{"op": ">", "value": 100},
			}},
		},
	}}}

	b, warns := NewWebBackend(cfg, Deps{
		PressureSampler: func(resource string) (checks.PressureSample, error) {
			if resource != "memory" {
				t.Fatalf("pressure resource = %q, want memory", resource)
			}
			return checks.PressureSample{
				Some: checks.PressureAverages{Avg10: 1.25, Avg60: 2.5, Avg300: 3.75},
				Full: checks.PressureAverages{Avg10: 0.5, Avg60: 0.75, Avg300: 1},
			}, nil
		},
		ConntrackSampler: func() (checks.ConntrackSample, error) {
			return checks.ConntrackSample{Count: 25, Max: 100}, nil
		},
		EntropySampler: func() (uint64, bool) { return 123, true },
		ZombieSampler:  func() (uint64, bool) { return 4, true },
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	byName := map[string]web.Watch{}
	for _, w := range b.Watches(context.Background()) {
		byName[w.Name] = w
	}

	pressure := byName["mem-pressure"]
	if !strings.Contains(pressure.Summary, "pressure memory") || pressure.State != TargetStateOK {
		t.Fatalf("pressure watch = %+v", pressure)
	}
	if got := readingByField(pressure.Readings, "some_avg60").Value; got != "2.50%" {
		t.Fatalf("pressure some_avg60 reading = %q, want 2.50%%", got)
	}
	if got := conditionByField(pressure.Conditions, "some_avg60").Value; got != "10" {
		t.Fatalf("pressure condition = %q, want 10", got)
	}

	entropy := byName["entropy"]
	if got := readingByField(entropy.Readings, "avail").Value; got != "123 bits" {
		t.Fatalf("entropy reading = %q, want 123 bits", got)
	}
	if conditionByField(entropy.Conditions, "avail").Op != "<" {
		t.Fatalf("entropy conditions = %+v", entropy.Conditions)
	}

	zombies := byName["zombies"]
	if got := readingByField(zombies.Readings, "count").Value; got != "4" {
		t.Fatalf("zombies reading = %q, want 4", got)
	}
	if conditionByField(zombies.Conditions, "count").Op != ">" {
		t.Fatalf("zombies conditions = %+v", zombies.Conditions)
	}

	conntrack := byName["conntrack"]
	if conntrack.Meter == nil || conntrack.Meter.Kind != "conntrack" || conntrack.Meter.Count != 25 || conntrack.Meter.Max != 100 {
		t.Fatalf("conntrack meter = %+v", conntrack.Meter)
	}
	if conditionByField(conntrack.Conditions, "count").Value != "100" {
		t.Fatalf("conntrack conditions = %+v", conntrack.Conditions)
	}
}

func TestWebBackendKernelWatchReadingErrorMarksWatchFailed(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"mem-pressure": map[string]any{"check": map[string]any{
				"type":       "pressure",
				"resource":   "memory",
				"some_avg60": map[string]any{"op": ">", "value": 10},
			}},
		},
	}}}
	b, warns := NewWebBackend(cfg, Deps{
		PressureSampler: func(string) (checks.PressureSample, error) {
			return checks.PressureSample{}, errors.New("PSI disabled")
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	watches := b.Watches(context.Background())
	if len(watches) != 1 {
		t.Fatalf("watches = %+v, want one", watches)
	}
	w := watches[0]
	if w.State != TargetStateFailed || len(w.Readings) != 1 || w.Readings[0].Error != "PSI disabled" {
		t.Fatalf("watch = %+v, want failed with PSI error reading", w)
	}
}

func TestWebBackendOomNetICMPAndPidsReadings(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"oom": map[string]any{"check": map[string]any{"type": "oom"}},
			"pid-table": map[string]any{"check": map[string]any{
				"type":     "pids",
				"used_pct": map[string]any{"op": ">=", "value": 90},
			}},
			"net-eth0": map[string]any{
				"check": map[string]any{"type": "net", "interface": "eth0"},
				"metrics": map[string]any{
					"state":   map[string]any{"on": "change"},
					"errors":  map[string]any{"delta": map[string]any{"op": ">", "value": 10}},
					"address": map[string]any{"expect": "present"},
					"speed":   map[string]any{"on": "change"},
				},
			},
			"ping-gw": map[string]any{
				"check": map[string]any{"type": "icmp", "host": "8.8.8.8", "count": 3},
				"metrics": map[string]any{
					"state":   map[string]any{"on": "change"},
					"latency": map[string]any{"threshold": map[string]any{"op": ">", "value": 100}},
				},
			},
		},
	}}}

	b, warns := NewWebBackend(cfg, Deps{
		OomSampler:  func() (uint64, bool) { return 7, true },
		PidsSampler: func() (checks.PidsSample, error) { return checks.PidsSample{Threads: 123, Max: 1000}, nil },
		NetSampler: func(iface string) (checks.NetSample, error) {
			if iface != "eth0" {
				t.Fatalf("net iface = %q, want eth0", iface)
			}
			return checks.NetSample{
				State:      "up",
				SpeedMbps:  1000,
				SpeedKnown: true,
				Counters:   map[string]uint64{"rx_errors": 2, "tx_errors": 3},
				Addrs:      []string{"192.0.2.10"},
			}, nil
		},
		PingSampler: func(host, iface string, count int, timeout time.Duration) (checks.PingSample, error) {
			if host != "8.8.8.8" || iface != "" || count != 3 {
				t.Fatalf("ping args host=%q iface=%q count=%d", host, iface, count)
			}
			if timeout <= 0 {
				t.Fatal("ping timeout must be bounded")
			}
			return checks.PingSample{Reachable: true, RTTms: 42.5, RTTKnown: true}, nil
		},
		DefaultTimeout: time.Second,
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	byName := map[string]web.Watch{}
	for _, w := range b.Watches(context.Background()) {
		byName[w.Name] = w
	}

	oom := byName["oom"]
	if got := readingByField(oom.Readings, "total").Value; got != "7" {
		t.Fatalf("oom reading = %q, want 7", got)
	}
	if conditionByField(oom.Conditions, "delta").Op != ">" {
		t.Fatalf("oom default condition = %+v", oom.Conditions)
	}

	pids := byName["pid-table"]
	if pids.Meter == nil || pids.Meter.Kind != "pids" || pids.Meter.Count != 123 || pids.Meter.Max != 1000 {
		t.Fatalf("pids meter = %+v", pids.Meter)
	}
	if !strings.Contains(pids.Summary, "123/1000") {
		t.Fatalf("pids summary = %q", pids.Summary)
	}

	netWatch := byName["net-eth0"]
	if got := readingByField(netWatch.Readings, "state").Value; got != "up" {
		t.Fatalf("net state reading = %q, want up", got)
	}
	if got := readingByField(netWatch.Readings, "speed").Value; got != "1000 Mbps" {
		t.Fatalf("net speed reading = %q, want 1000 Mbps", got)
	}
	if got := readingByField(netWatch.Readings, "errors").Value; got != "5" {
		t.Fatalf("net errors reading = %q, want 5", got)
	}
	if got := conditionByField(netWatch.Conditions, "errors.delta").Value; got != "10" {
		t.Fatalf("net conditions = %+v", netWatch.Conditions)
	}

	icmp := byName["ping-gw"]
	if got := readingByField(icmp.Readings, "state").Value; got != "up" {
		t.Fatalf("icmp state reading = %q, want up", got)
	}
	if got := readingByField(icmp.Readings, "latency").Value; got != "42.5 ms" {
		t.Fatalf("icmp latency reading = %q, want 42.5 ms", got)
	}
	if got := conditionByField(icmp.Conditions, "latency.threshold").Value; got != "100" {
		t.Fatalf("icmp conditions = %+v", icmp.Conditions)
	}
}

func TestWebBackendProcessWatchReadings(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"hot-workers": map[string]any{
				"check": map[string]any{
					"type":   "process",
					"name":   "apache2",
					"user":   "apache",
					"for":    "5m",
					"cpu":    map[string]any{"op": ">", "value": 80},
					"memory": map[string]any{"op": ">", "value": 524288000},
					"io":     map[string]any{"op": ">", "value": 10485760},
					"gone":   true,
				},
			},
		},
	}}}
	sampler := &webProcSampler{samples: []ProcInfo{
		{PID: 42, CPUTicks: 20, RSS: 100, IOBytes: 500, HasIO: true},
		{PID: 7, CPUTicks: 30, RSS: 200},
	}}
	b, warns := NewWebBackend(cfg, Deps{ProcSampler: sampler})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	watches := b.Watches(context.Background())
	if len(watches) != 1 {
		t.Fatalf("watches = %+v, want one", watches)
	}
	w := watches[0]
	if sampler.match != (ProcMatch{Name: "apache2", User: "apache"}) {
		t.Fatalf("process sample match = %+v", sampler.match)
	}
	if !strings.Contains(w.Summary, "2 matching processes") || !strings.Contains(w.Summary, "rss 300 bytes") {
		t.Fatalf("process summary = %q", w.Summary)
	}
	if got := readingByField(w.Readings, "pids").Value; got != "7, 42" {
		t.Fatalf("process pids reading = %q, want sorted pids", got)
	}
	if got := readingByField(w.Readings, "rss").Value; got != "300 bytes" {
		t.Fatalf("process rss reading = %q, want 300 bytes", got)
	}
	if got := readingByField(w.Readings, "cpu_ticks").Value; got != "50" {
		t.Fatalf("process cpu ticks reading = %q, want 50", got)
	}
	if got := readingByField(w.Readings, "io").Value; got != "500 bytes" {
		t.Fatalf("process io reading = %q, want 500 bytes", got)
	}
	if conditionByField(w.Conditions, "for").Value != "5m" ||
		conditionByField(w.Conditions, "gone").Value != "true" ||
		conditionByField(w.Conditions, "cpu").Op != ">" ||
		conditionByField(w.Conditions, "memory").Value != "524288000" ||
		conditionByField(w.Conditions, "io").Value != "10485760" {
		t.Fatalf("process conditions = %+v", w.Conditions)
	}
}

func TestWebBackendAdditionalHostWatchReadings(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"autofs-net": map[string]any{"check": map[string]any{
				"type": "autofs",
				"path": "/net",
			}},
			"diskio-root": map[string]any{"check": map[string]any{
				"type":     "diskio",
				"device":   "sda",
				"util_pct": map[string]any{"op": ">", "value": 80},
			}},
			"edac": map[string]any{"check": map[string]any{
				"type": "edac",
				"ue":   map[string]any{"op": ">", "value": 0},
			}},
			"raid": map[string]any{"check": map[string]any{
				"type":     "raid",
				"degraded": map[string]any{"op": ">", "value": 0},
			}},
			"route-wan": map[string]any{"check": map[string]any{
				"type":      "route",
				"family":    "ipv4",
				"interface": "ppp0",
			}},
			"sensors": map[string]any{"check": map[string]any{
				"type": "sensors",
				"temp": map[string]any{"op": ">", "value": 70},
			}},
		},
	}}}
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	diskSamples := []checks.DiskIOSample{
		{ReadsCompleted: 10, SectorsRead: 100, ReadTicksMs: 100, WritesCompleted: 10, SectorsWritten: 200, WriteTicksMs: 100, IOTicksMs: 1000},
		{ReadsCompleted: 20, SectorsRead: 102, ReadTicksMs: 130, WritesCompleted: 20, SectorsWritten: 204, WriteTicksMs: 120, IOTicksMs: 1500},
	}
	diskCalls := 0
	b, warns := NewWebBackend(cfg, Deps{
		MountSampler: func() ([]checks.Mount, error) {
			return []checks.Mount{{MountPoint: "/net", FSType: "autofs"}}, nil
		},
		DiskIOSampler: func(device string) (checks.DiskIOSample, error) {
			if device != "sda" {
				t.Fatalf("diskio device = %q, want sda", device)
			}
			sample := diskSamples[min(diskCalls, len(diskSamples)-1)]
			diskCalls++
			return sample, nil
		},
		EdacSampler: func() (checks.EdacCounts, error) {
			return checks.EdacCounts{CE: 2, UE: 1, Present: true}, nil
		},
		RaidSampler: func() (checks.RaidStatus, error) {
			return checks.RaidStatus{Arrays: 2, Degraded: 1, Recovering: 1, DegradedNames: []string{"md0"}}, nil
		},
		RouteSampler: func(family string) ([]checks.DefaultRoute, error) {
			if family != "ipv4" {
				t.Fatalf("route family = %q, want ipv4", family)
			}
			return []checks.DefaultRoute{{Iface: "ppp0", Gateway: "192.0.2.1"}}, nil
		},
		SensorSampler: func() ([]checks.SensorReading, error) {
			return []checks.SensorReading{
				{Chip: "coretemp", Kind: "temp", Label: "Package", Value: 82.5},
				{Chip: "nct", Kind: "fan", Label: "fan1", Value: 900},
				{Chip: "nct", Kind: "in", Label: "12V", Value: 11.9},
			}, nil
		},
		Now: func() time.Time { return now },
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	_ = b.Watches(context.Background()) // primes diskio's rate baseline.
	now = now.Add(time.Second)
	byName := map[string]web.Watch{}
	for _, w := range b.Watches(context.Background()) {
		byName[w.Name] = w
	}

	autofs := byName["autofs-net"]
	if got := readingByField(autofs.Readings, "mountpoints").Value; got != "/net" {
		t.Fatalf("autofs mountpoints = %q, want /net", got)
	}
	if !strings.Contains(autofs.Summary, "/net active") {
		t.Fatalf("autofs summary = %q", autofs.Summary)
	}

	diskio := byName["diskio-root"]
	if got := readingByField(diskio.Readings, "util_pct").Value; got != "50.00%" {
		t.Fatalf("diskio util = %q, want 50.00%%", got)
	}
	if got := readingByField(diskio.Readings, "read_bytes").Value; got != "1024 B/s" {
		t.Fatalf("diskio read = %q, want 1024 B/s", got)
	}

	edac := byName["edac"]
	if got := readingByField(edac.Readings, "ue").Value; got != "1" {
		t.Fatalf("edac ue = %q, want 1", got)
	}
	if conditionByField(edac.Conditions, "ue").Op != ">" {
		t.Fatalf("edac conditions = %+v", edac.Conditions)
	}

	raid := byName["raid"]
	if got := readingByField(raid.Readings, "degraded_arrays").Value; got != "md0" {
		t.Fatalf("raid degraded arrays = %q, want md0", got)
	}

	route := byName["route-wan"]
	if got := readingByField(route.Readings, "gateway").Value; got != "192.0.2.1" {
		t.Fatalf("route gateway = %q, want 192.0.2.1", got)
	}
	if conditionByField(route.Conditions, "interface").Value != "ppp0" {
		t.Fatalf("route conditions = %+v", route.Conditions)
	}

	sensors := byName["sensors"]
	if got := readingByField(sensors.Readings, "temp").Value; got != "82.5 C" {
		t.Fatalf("sensors temp = %q, want 82.5 C", got)
	}
	if got := readingByField(sensors.Readings, "fan").Value; got != "900 RPM" {
		t.Fatalf("sensors fan = %q, want 900 RPM", got)
	}
}

func TestWebBackendPidsReadingErrorMarksWatchFailed(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"pid-table": map[string]any{"check": map[string]any{
				"type":     "pids",
				"used_pct": map[string]any{"op": ">=", "value": 90},
			}},
		},
	}}}
	b, warns := NewWebBackend(cfg, Deps{
		PidsSampler: func() (checks.PidsSample, error) {
			return checks.PidsSample{}, errors.New("loadavg failed")
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	watches := b.Watches(context.Background())
	if len(watches) != 1 {
		t.Fatalf("watches = %+v, want one", watches)
	}
	w := watches[0]
	if w.State != TargetStateFailed || len(w.Readings) != 1 || w.Readings[0].Error != "loadavg failed" {
		t.Fatalf("watch = %+v, want failed with pids error reading", w)
	}
}

func readingByField(readings []web.WatchReading, field string) web.WatchReading {
	for _, reading := range readings {
		if reading.Field == field {
			return reading
		}
	}
	return web.WatchReading{}
}

func conditionByField(conditions []web.WatchCondition, field string) web.WatchCondition {
	for _, condition := range conditions {
		if condition.Field == field {
			return condition
		}
	}
	return web.WatchCondition{}
}

type webProcSampler struct {
	match   ProcMatch
	samples []ProcInfo
}

func (s *webProcSampler) Sample(match ProcMatch) []ProcInfo {
	s.match = match
	return s.samples
}

// fakeSwapReader is the minimal metrics.Reader (plus optional TotalSwap) the
// swap watch view needs.
type fakeSwapReader struct {
	total, used uint64
}

func (fakeSwapReader) ProcessCPU(int) (uint64, bool)        { return 0, false }
func (fakeSwapReader) ProcessRSS(int) (uint64, bool)        { return 0, false }
func (fakeSwapReader) ProcessIO(int) (uint64, uint64, bool) { return 0, 0, false }
func (fakeSwapReader) ProcessFDs(int) (uint64, bool)        { return 0, false }
func (fakeSwapReader) ProcessThreads(int) (uint64, bool)    { return 0, false }
func (fakeSwapReader) TotalMemory() (uint64, uint64, bool)  { return 1 << 30, 1 << 29, true }
func (fakeSwapReader) SystemCPU() (uint64, uint64, bool)    { return 0, 0, false }
func (fakeSwapReader) LoadAverages() (float64, float64, float64, bool) {
	return 0, 0, 0, false
}
func (fakeSwapReader) NumCPU() int         { return 1 }
func (fakeSwapReader) ClockTicks() float64 { return 100 }
func (r fakeSwapReader) TotalSwap() (uint64, uint64, bool) {
	return r.total, r.used, true
}

type countingSystemReader struct {
	memoryCalls int
	loadCalls   int
	swapCalls   int
}

func (*countingSystemReader) ProcessCPU(int) (uint64, bool)        { return 0, false }
func (*countingSystemReader) ProcessRSS(int) (uint64, bool)        { return 0, false }
func (*countingSystemReader) ProcessIO(int) (uint64, uint64, bool) { return 0, 0, false }
func (*countingSystemReader) ProcessFDs(int) (uint64, bool)        { return 0, false }
func (*countingSystemReader) ProcessThreads(int) (uint64, bool)    { return 0, false }
func (*countingSystemReader) SystemCPU() (uint64, uint64, bool)    { return 0, 0, false }
func (*countingSystemReader) NumCPU() int                          { return 4 }
func (*countingSystemReader) ClockTicks() float64                  { return 100 }

func (r *countingSystemReader) TotalMemory() (uint64, uint64, bool) {
	r.memoryCalls++
	return 1000, 250, true
}

func (r *countingSystemReader) LoadAverages() (float64, float64, float64, bool) {
	r.loadCalls++
	return 1, 2, 3, true
}

func (r *countingSystemReader) TotalSwap() (uint64, uint64, bool) {
	r.swapCalls++
	return 2000, 500, true
}

func TestWebBackendSwapWatchIncludesUsage(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"swap": map[string]any{
				"check": map[string]any{"type": "swap"},
				"metrics": map[string]any{
					"usage": map[string]any{
						"used_pct": map[string]any{"op": ">=", "value": 80},
						"then":     map[string]any{"notify": []any{"none"}},
					},
				},
			},
		},
	}}}
	b, warns := NewWebBackend(cfg, Deps{Collector: metrics.New(fakeSwapReader{total: 2048, used: 512})})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	watches := b.Watches(context.Background())
	if len(watches) != 1 || watches[0].Swap == nil {
		t.Fatalf("watches = %+v, want one with swap usage", watches)
	}
	sw := watches[0].Swap
	if sw.TotalBytes != 2048 || sw.UsedBytes != 512 || sw.FreeBytes != 1536 || sw.UsedPct != 25 {
		t.Fatalf("swap view = %+v, want 512/2048 used (25%%, 1536 free)", sw)
	}
}

func TestWebBackendWatchesShareSystemSnapshot(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"load":   map[string]any{"check": map[string]any{"type": "load"}},
			"memory": map[string]any{"check": map[string]any{"type": "memory"}},
			"swap":   map[string]any{"check": map[string]any{"type": "swap"}},
		},
	}}}
	reader := &countingSystemReader{}
	collector := metrics.New(reader)
	collector.SystemFreshness = 0
	b, warns := NewWebBackend(cfg, Deps{Collector: collector})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	watches := b.Watches(context.Background())
	if len(watches) != 3 {
		t.Fatalf("watches = %+v, want 3", watches)
	}
	if reader.memoryCalls != 1 || reader.loadCalls != 1 || reader.swapCalls != 1 {
		t.Fatalf("system samples memory/load/swap = %d/%d/%d, want 1/1/1", reader.memoryCalls, reader.loadCalls, reader.swapCalls)
	}
}

func TestWebBackendStorageWatchIncludesFilesystemDetails(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"storage-data": map[string]any{
				"interval": "45s",
				"check": map[string]any{
					"type":     "storage",
					"path":     "/data/app",
					"mounted":  true,
					"free_pct": map[string]any{"op": "<", "value": 15},
					"free_bytes": map[string]any{
						"op":    "<",
						"value": "10G",
					},
				},
				"then": map[string]any{
					"notify": []any{"ops", "pager"},
					"expand": map[string]any{"by": "5G"},
					"hook": map[string]any{
						"command": []any{"/usr/local/bin/sermo-disk-alert", "--path", "/data/app"},
					},
				},
			},
		},
	}}}
	usagePath := ""
	mountSampled := false
	b, warns := NewWebBackend(cfg, Deps{
		DiskUsage: func(path string) (checks.DiskStats, error) {
			usagePath = path
			return checks.DiskStats{
				UsedPct:       87.5,
				FreePct:       12.5,
				TotalBytes:    1000,
				FreeBytes:     125,
				InodesTotal:   100,
				InodesFree:    20,
				InodesUsedPct: 80,
				InodesFreePct: 20,
			}, nil
		},
		MountSampler: func() ([]checks.Mount, error) {
			mountSampled = true
			return []checks.Mount{
				{Device: "/dev/root", MountPoint: "/", FSType: "ext4", Options: []string{"rw"}},
				{Device: "/dev/mapper/data", MountPoint: "/data", FSType: "xfs", Options: []string{"rw", "noatime"}},
			}, nil
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	watches := b.Watches(context.Background())
	if len(watches) != 1 {
		t.Fatalf("got %d watches, want 1: %+v", len(watches), watches)
	}
	w := watches[0]
	if usagePath != "/data/app" || !mountSampled {
		t.Fatalf("samplers not called as expected: usagePath=%q mountSampled=%v", usagePath, mountSampled)
	}
	if w.Name != "storage-data" || w.Interval != "45s" || w.CheckType != "storage" {
		t.Fatalf("watch identity = %+v", w)
	}
	if !slices.Equal(w.Notifiers, []string{"ops", "pager"}) {
		t.Fatalf("notifiers = %v, want ops,pager", w.Notifiers)
	}
	if !slices.Equal(w.HookCommand, []string{"/usr/local/bin/sermo-disk-alert", "--path", "/data/app"}) {
		t.Fatalf("hook command = %v", w.HookCommand)
	}
	if w.Summary == "" || !strings.Contains(w.Summary, "/data/app") || !strings.Contains(w.Summary, "xfs") {
		t.Fatalf("summary = %q, want path and filesystem", w.Summary)
	}
	if len(w.Conditions) != 3 {
		t.Fatalf("conditions = %+v, want free_pct/free_bytes/mounted", w.Conditions)
	}
	cond := map[string]web.WatchCondition{}
	for _, c := range w.Conditions {
		cond[c.Field] = c
	}
	if cond["free_pct"].Op != "<" || cond["free_pct"].Value != "15" {
		t.Fatalf("free_pct condition = %+v", cond["free_pct"])
	}
	if cond["free_bytes"].Op != "<" || cond["free_bytes"].Value != "10G" {
		t.Fatalf("free_bytes condition = %+v", cond["free_bytes"])
	}
	if cond["mounted"].Op != "==" || cond["mounted"].Value != "true" {
		t.Fatalf("mounted condition = %+v", cond["mounted"])
	}
	if w.Disk == nil {
		t.Fatal("storage watch should include live filesystem info")
	}
	if w.Disk.MountPoint != "/data" || w.Disk.Device != "/dev/mapper/data" || w.Disk.FileSystem != "xfs" {
		t.Fatalf("disk mount info = %+v", w.Disk)
	}
	if w.Disk.FreeBytes != 125 || w.Disk.UsedBytes != 875 || w.Disk.FreePct != 12.5 || w.Disk.InodesFree != 20 {
		t.Fatalf("disk usage info = %+v", w.Disk)
	}
	if w.Expand == nil || w.Expand.ByBytes != 5<<30 {
		t.Fatalf("expand info = %+v, want 5G", w.Expand)
	}
}

func TestWebBackendNotifiersExposeEnabledState(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"notifiers": map[string]any{
			"muted": map[string]any{"enabled": false, "type": "slack", "webhook": "https://hooks.example/x"},
			"ops":   map[string]any{"type": "email", "dsn": "smtp://x", "from": "x@y", "to": []any{"a@b"}},
		},
	}}}
	b, warns := NewWebBackend(cfg, Deps{})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	got := b.Notifiers(context.Background())
	if len(got) != 2 {
		t.Fatalf("notifiers = %+v, want 2 entries", got)
	}
	byName := map[string]web.Notifier{}
	for _, n := range got {
		byName[n.Name] = n
	}
	if !byName["ops"].Enabled {
		t.Fatalf("ops should default enabled: %+v", byName["ops"])
	}
	if byName["muted"].Enabled {
		t.Fatalf("muted should be disabled: %+v", byName["muted"])
	}
}

func TestWebBackendExpandWatchUsesConfiguredPathAndSize(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"storage-data": map[string]any{
				"check": map[string]any{
					"type":     "storage",
					"path":     "/data/app",
					"used_pct": map[string]any{"op": ">=", "value": 90},
				},
				"then": map[string]any{"expand": map[string]any{"by": "5G"}},
			},
		},
	}}}
	exp := &fakeExpander{res: volume.Result{VG: "vg0", LV: "data", GrewBytes: 5 << 30}}
	var events []Event
	b, warns := NewWebBackend(cfg, Deps{
		VolumeExpander:   exp,
		OperationTimeout: time.Second,
		Emit:             func(e Event) { events = append(events, e) },
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	res := b.ExpandWatch(context.Background(), "storage-data")
	if !res.OK {
		t.Fatalf("ExpandWatch failed: %+v", res)
	}
	if !slices.Equal(exp.calls, []string{"/data/app:5368709120"}) {
		t.Fatalf("expand calls = %v, want configured path and 5G", exp.calls)
	}
	if len(events) != 1 || events[0].Watch != "storage-data" || events[0].Kind != "expand" || events[0].Action != "expand" || events[0].Status != "ok" {
		t.Fatalf("events = %+v, want successful expand event", events)
	}
}

func TestWebBackendExpandWatchRejectsUnconfiguredAction(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"storage-data": map[string]any{
				"check": map[string]any{
					"type":     "storage",
					"path":     "/data/app",
					"used_pct": map[string]any{"op": ">=", "value": 90},
				},
				"then": map[string]any{"notify": []any{"ops"}},
			},
		},
	}}}
	exp := &fakeExpander{res: volume.Result{VG: "vg0", LV: "data", GrewBytes: 5 << 30}}
	b, warns := NewWebBackend(cfg, Deps{VolumeExpander: exp, OperationTimeout: time.Second})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	res := b.ExpandWatch(context.Background(), "storage-data")
	if res.OK || !strings.Contains(res.Message, "no then.expand") {
		t.Fatalf("ExpandWatch = %+v, want missing expand rejection", res)
	}
	if len(exp.calls) != 0 {
		t.Fatalf("expand must not run without then.expand, calls=%v", exp.calls)
	}
}

func TestWebBackendStorageWatchReportsSamplerErrors(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"storage-data": map[string]any{
				"check": map[string]any{
					"type":     "storage",
					"path":     "/data",
					"free_pct": map[string]any{"op": "<", "value": 15},
				},
				"then": map[string]any{"notify": []any{"ops"}},
			},
		},
	}}}
	b, warns := NewWebBackend(cfg, Deps{
		DiskUsage: func(string) (checks.DiskStats, error) {
			return checks.DiskStats{}, errors.New("statfs failed")
		},
		MountSampler: func() ([]checks.Mount, error) {
			return nil, errors.New("mount table failed")
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	watches := b.Watches(context.Background())
	if len(watches) != 1 || watches[0].Disk == nil {
		t.Fatalf("watch disk info = %+v", watches)
	}
	if watches[0].Disk.SampleError != "statfs failed" || watches[0].Disk.MountSampleError != "mount table failed" {
		t.Fatalf("disk errors = %+v", watches[0].Disk)
	}
	if !strings.Contains(watches[0].Summary, "statfs failed") {
		t.Fatalf("summary = %q, want statfs error", watches[0].Summary)
	}
}

func TestWebBackendDetailAtTimestamp(t *testing.T) {
	t0 := time.Date(2026, 6, 7, 12, 30, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	snaps := NewSnapshots()
	snaps.now = func() time.Time { return t0 }
	snaps.Publish("web", map[string]checks.Result{
		"fast": {Check: "fast", OK: true},
		"slow": {Check: "slow", OK: true},
	}, map[string]bool{"fast": true, "slow": true})

	snaps.now = func() time.Time { return t1 }
	snaps.Publish("web", map[string]checks.Result{
		"fast": {Check: "fast", OK: true},
		"slow": {Check: "slow", OK: true, Message: "cached"},
	}, map[string]bool{"fast": true})

	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {
				checkNames: []string{"fast", "slow", "new"},
				checkTypes: map[string]string{"fast": "tcp", "slow": "http", "new": "command"},
			},
		},
		snapshots: snaps,
	}

	detail, ok := b.Detail(context.Background(), "web")
	if !ok {
		t.Fatal("detail not found")
	}
	byName := map[string]struct {
		ran bool
		at  string
	}{}
	for _, c := range detail.Checks {
		byName[c.Name] = struct {
			ran bool
			at  string
		}{c.Ran, c.At}
	}
	wantT1 := t1.UTC().Format(time.RFC3339)
	wantT0 := t0.UTC().Format(time.RFC3339)
	if !byName["fast"].ran || byName["fast"].at != wantT1 {
		t.Fatalf("fast = %+v, want ran=true at=%s", byName["fast"], wantT1)
	}
	if byName["slow"].ran || byName["slow"].at != wantT0 {
		t.Fatalf("cached slow = %+v, want ran=false at=%s", byName["slow"], wantT0)
	}
	if byName["new"].ran || byName["new"].at != "" {
		t.Fatalf("unobserved new = %+v", byName["new"])
	}
}

func TestWebBackendIncludesDisabledServices(t *testing.T) {
	// NewWebBackend (and thus the web list) must include services that have
	// `enabled: false` so they are visible in the dashboard for the operator
	// to know they exist and can be activated (by editing config + reload).
	b := &WebBackend{
		order: []string{"mysql", "web"},
		entries: map[string]*webEntry{
			"mysql": {displayName: "MySQL", category: "database", unit: "mysqld", backend: "systemd", disabled: true},
			"web": {
				displayName: "Web",
				category:    "frontend",
				unit:        "nginx",
				backend:     "systemd",
				status:      func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
			},
		},
	}

	svcs := b.Services(context.Background())
	if len(svcs) != 2 {
		t.Fatalf("got %d services, want 2 (including the disabled one)", len(svcs))
	}
	byName := map[string]web.Service{}
	for _, s := range svcs {
		byName[s.Name] = s
	}

	dis := byName["mysql"]
	if dis.Enabled || dis.Status != "disabled" || dis.State != TargetStateDisabled || dis.Monitored {
		t.Fatalf("disabled service = %+v, want Enabled=false, Status=disabled, State=disabled, Monitored=false", dis)
	}
	if dis.Category != "database" {
		t.Fatalf("disabled service category = %q, want database", dis.Category)
	}

	en := byName["web"]
	if !en.Enabled || en.Status == "disabled" || en.State != TargetStateMonitorized {
		t.Fatalf("normal service = %+v, want Enabled=true", en)
	}
	if en.Category != "frontend" {
		t.Fatalf("normal service category = %q, want frontend", en.Category)
	}

	// Detail for disabled should succeed (returns basic info) but have no checks etc.
	d, ok := b.Detail(context.Background(), "mysql")
	if !ok || d.Name != "mysql" || d.Enabled {
		t.Fatalf("detail for disabled: ok=%v d=%+v", ok, d)
	}
	if len(d.Checks) != 0 {
		t.Fatalf("disabled detail should not expose checks")
	}

	// Operate must be rejected for disabled
	res := b.Operate(context.Background(), "mysql", "restart")
	if res.OK {
		t.Fatal("operate on disabled must fail")
	}
	if res.Message == "" || !strings.Contains(res.Message, "disabled") {
		t.Fatalf("operate error message should mention disabled: %q", res.Message)
	}

	// Preflight on disabled should not be found (no engine)
	if _, ok := b.Preflight(context.Background(), "mysql"); ok {
		t.Fatal("preflight on disabled should not succeed")
	}
}

func TestWebBackendConfigRenderAndDiff(t *testing.T) {
	root := t.TempDir()
	daemons := filepath.Join(root, "daemons")
	enabled := filepath.Join(root, "enabled")
	if err := os.MkdirAll(daemons, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(root, "sermo.yml")
	daemonPath := filepath.Join(daemons, "web-daemon.yml")
	basePath := filepath.Join(enabled, "base.yml")
	webPath := filepath.Join(enabled, "web.yml")
	if err := os.WriteFile(globalPath, []byte(`
paths:
  catalog: [`+daemons+`]
  includes: [`+enabled+`]
defaults:
  policy:
    cooldown: 5m
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(daemonPath, []byte(`
kind: daemon
name: web-daemon
checks:
  tcp:
    type: tcp
    host: 127.0.0.1
    port: 80
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(basePath, []byte(`
kind: service
name: base
uses: web-daemon
service: base.service
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(webPath, []byte(`
kind: service
name: web
uses: web-daemon
service: web.service
checks:
  tcp:
    port: 8080
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(globalPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	b, warnings := NewWebBackend(cfg, Deps{Backend: "systemd", Manager: fakeManager{}, ExecxRunner: execx.CommandRunner{}})
	if len(warnings) > 0 {
		t.Fatalf("NewWebBackend warnings: %v", warnings)
	}

	rendered, ok, err := b.ConfigRender(context.Background(), "web", "yaml")
	if err != nil || !ok {
		t.Fatalf("ConfigRender: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(rendered.Content, "web.service") || !strings.Contains(rendered.Content, "8080") {
		t.Fatalf("rendered content missing resolved values:\n%s", rendered.Content)
	}
	wantSources := []string{globalPath, daemonPath, webPath}
	for _, want := range wantSources {
		if !slices.Contains(rendered.SourceFiles, want) {
			t.Fatalf("source files = %v, missing %s", rendered.SourceFiles, want)
		}
	}

	diff, ok, err := b.ConfigDiff(context.Background(), "base", "web")
	if err != nil || !ok {
		t.Fatalf("ConfigDiff: ok=%v err=%v", ok, err)
	}
	if diff.Identical || len(diff.Removed) == 0 || len(diff.Added) == 0 {
		t.Fatalf("diff = %+v, want changed lines", diff)
	}
	if _, ok, err := b.ConfigRender(context.Background(), "missing", "yaml"); err != nil || ok {
		t.Fatalf("missing render: ok=%v err=%v", ok, err)
	}
}

// fakeEnvRunnerForWeb is used to inject a custom execx runner via Deps.ExecxRunner
// and verify that hooks in watches built for the web backend receive the expected env.
type fakeEnvRunnerForWeb struct {
	calls []struct {
		env  []string
		name string
		args []string
	}
}

func (f *fakeEnvRunnerForWeb) Run(ctx context.Context, name string, args ...string) (execx.Result, error) {
	return execx.Result{}, nil
}
func (f *fakeEnvRunnerForWeb) RunEnv(ctx context.Context, env []string, name string, args ...string) (execx.Result, error) {
	f.calls = append(f.calls, struct {
		env  []string
		name string
		args []string
	}{env, name, args})
	return execx.Result{ExitCode: 0}, nil
}

func TestWebBackendPropagatesCustomExecxRunnerToWatchHooks(t *testing.T) {
	// Minimal config with a watch that has a hook. The check is a simple command that always succeeds.
	cfgContent := `
paths:
  catalog: []
  includes: []
  runtime: /tmp
defaults:
  policy:
    cooldown: 1m
watches:
  test-hook-watch:
    check:
      type: command
      command: ["/bin/true"]
    then:
      hook:
        command: ["/bin/custom-web-hook", "web-alert"]
`
	globalPath := filepath.Join(t.TempDir(), "sermo.yml")
	if err := os.WriteFile(globalPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(globalPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	fake := &fakeEnvRunnerForWeb{}
	deps := Deps{
		Backend:        "systemd",
		Manager:        fakeManager{},
		ExecxRunner:    fake,
		DefaultTimeout: time.Second,
		Now:            time.Now,
		Emit:           func(Event) {},
	}

	// NewWebBackend will internally call BuildWatches with the deps, propagating ExecxRunner
	// to the OSHookRunner for the watch.
	_, warnings := NewWebBackend(cfg, deps)
	if len(warnings) > 0 {
		t.Fatalf("NewWebBackend warnings: %v", warnings)
	}

	// To exercise the hook, we simulate a watch cycle. Since watches are built internally,
	// we use BuildWatches directly with same deps to get the watch and run it (this mirrors
	// what NewWebBackend does for watch part).
	watches, wwarns := BuildWatches(cfg, deps, 30*time.Second)
	if len(wwarns) != 0 || len(watches) != 1 {
		t.Fatalf("expected 1 watch, warnings=%v", wwarns)
	}
	w := watches[0]
	// Command check is health-style (FireOnFail=true by default), so success (OK=true) means !fired.
	// Override to force fire on success for this test (we only care about runner being called with env).
	w.FireOnFail = false
	w.RunCycle(context.Background())

	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 call to custom execx runner from web backend watch hook, got %d", len(fake.calls))
	}
	call := fake.calls[0]
	if call.name != "/bin/custom-web-hook" || len(call.args) != 1 || call.args[0] != "web-alert" {
		t.Fatalf("wrong argv to custom runner: %s %v", call.name, call.args)
	}
	// Verify specific env from the watch (SERMO_WATCH and data from the command check result if any, plus type)
	hasWatch := false
	hasType := false
	for _, e := range call.env {
		if e == "SERMO_WATCH=test-hook-watch" {
			hasWatch = true
		}
		if e == "SERMO_CHECK_TYPE=command" {
			hasType = true
		}
	}
	if !hasWatch || !hasType {
		t.Fatalf("custom runner via webbackend Deps did not receive expected SERMO_ env: %v", call.env)
	}
}
