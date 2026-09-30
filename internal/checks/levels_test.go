package checks

import (
	"context"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx/execxtest"
	"sermo/internal/metrics"
	"sermo/internal/severity"
)

// storageLevels is the canonical disk ladder: warning at 80%, error at 95%,
// critical at 99%.
func storageLevels() map[string]any {
	return map[string]any{
		CheckKeyType:     CheckTypeStorage,
		CheckKeyPath:     "/",
		fieldUsedPct:     pred(">=", "80%"),
		CheckKeySeverity: "warning",
		CheckKeyLevels: map[string]any{
			"error":    map[string]any{fieldUsedPct: pred(">=", "95%")},
			"critical": map[string]any{fieldUsedPct: pred(">=", "99%")},
		},
	}
}

func TestValidateLevelsShape(t *testing.T) {
	tests := []struct {
		name  string
		typ   string
		entry map[string]any
		want  string
	}{
		{"not a mapping", CheckTypeStorage, map[string]any{fieldUsedPct: pred(">", 1), CheckKeyLevels: "high"}, "must be a mapping"},
		{"empty", CheckTypeStorage, map[string]any{fieldUsedPct: pred(">", 1), CheckKeyLevels: map[string]any{}}, "at least one level"},
		{"ok is not a level", CheckTypeStorage, map[string]any{fieldUsedPct: pred(">", 1), CheckKeyLevels: map[string]any{"ok": map[string]any{fieldUsedPct: pred(">", 2)}}}, "levels.ok is not a severity"},
		{"empty level", CheckTypeStorage, map[string]any{fieldUsedPct: pred(">", 1), CheckKeyLevels: map[string]any{"error": map[string]any{}}}, "must restate at least one of"},
		{"scalar level", CheckTypeStorage, map[string]any{fieldUsedPct: pred(">", 1), CheckKeyLevels: map[string]any{"error": 95}}, "must be a mapping of thresholds"},
		{"foreign key", CheckTypeStorage, map[string]any{fieldUsedPct: pred(">", 1), CheckKeyLevels: map[string]any{"error": map[string]any{"load1": pred(">", 2)}}}, "levels.error.load1 is not a threshold"},
		{"bad value", CheckTypeStorage, map[string]any{fieldUsedPct: pred(">", 1), CheckKeyLevels: map[string]any{"error": map[string]any{fieldUsedPct: pred("~", 2)}}}, "invalid op"},
		{"unsupported type", CheckTypeTCP, map[string]any{CheckKeyLevels: map[string]any{"error": map[string]any{"x": 1}}}, "not supported on a tcp check"},
		{"stateful net metric", CheckTypeNet, map[string]any{CheckKeyMetric: NetMetricState, CheckKeyLevels: map[string]any{"error": map[string]any{CheckKeyDelta: pred(">", 1)}}}, "no ordered threshold"},
		{"mount-only storage", CheckTypeStorage, map[string]any{CheckKeyMounted: true, CheckKeyLevels: map[string]any{"error": map[string]any{fieldUsedPct: pred(">", 2)}}}, "need a base threshold"},
		{"substring stdout", CheckTypeCommand, map[string]any{CheckKeyExpectStdout: "ok", CheckKeyLevels: map[string]any{"error": map[string]any{CheckKeyExpectStdout: pred("<=", 9)}}}, "need a base threshold"},
		{"unordered stdout level", CheckTypeCommand, map[string]any{CheckKeyExpectStdout: pred("<=", 1), CheckKeyLevels: map[string]any{"error": map[string]any{CheckKeyExpectStdout: pred("==", 9)}}}, "ordered comparison"},
		{"cert without window", CheckTypeCert, map[string]any{CheckKeyLevels: map[string]any{"error": map[string]any{CheckKeyExpiresInDays: 7}}}, "need a base threshold"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidateLevels(tt.typ, tt.entry, "")
			if !strings.Contains(strings.Join(got.Errors, "\n"), tt.want) {
				t.Fatalf("errors = %q, want one containing %q", got.Errors, tt.want)
			}
		})
	}
}

func TestValidateLevelsAcceptsDefaultedAndDisabled(t *testing.T) {
	// oom fires on any kill by default, so a level may add the threshold.
	oom := map[string]any{CheckKeyLevels: map[string]any{"critical": map[string]any{CheckKeyDelta: pred(">=", 5)}}}
	if got := ValidateLevels(CheckTypeOOM, oom, ""); len(got.Errors)+len(got.Ignored) != 0 || len(got.kept) != 1 {
		t.Fatalf("oom levels = %+v, want one kept tier", got)
	}
	// false drops an inherited block or tier.
	disabled := storageLevels()
	disabled[CheckKeyLevels] = false
	if got := ValidateLevels(CheckTypeStorage, disabled, severity.Warning); len(got.Errors) != 0 || len(got.kept) != 0 {
		t.Fatalf("levels: false = %+v, want nothing", got)
	}
	tier := storageLevels()
	tier[CheckKeyLevels].(map[string]any)["error"] = false
	if got := ValidateLevels(CheckTypeStorage, tier, severity.Warning); len(got.kept) != 1 || got.kept[0].level != severity.Critical {
		t.Fatalf("levels.error: false = %+v, want only critical", got)
	}
}

func TestValidateLevelsIgnoresLaxTiers(t *testing.T) {
	tests := []struct {
		name     string
		typ      string
		entry    map[string]any
		declared severity.Level
		want     string
		kept     int
	}{
		{"base raised above a catalog level", CheckTypeStorage, map[string]any{
			fieldUsedPct: pred(">=", "96%"),
			CheckKeyLevels: map[string]any{
				"error":    map[string]any{fieldUsedPct: pred(">=", "95%")},
				"critical": map[string]any{fieldUsedPct: pred(">=", "99%")},
			},
		}, severity.Warning, "levels.error.used_pct >= 95 is not stricter than >= 96", 1},
		{"opposite direction", CheckTypeStorage, map[string]any{
			fieldFreePct:   pred("<", 20),
			CheckKeyLevels: map[string]any{"error": map[string]any{fieldFreePct: pred(">", 5)}},
		}, severity.Warning, "opposite direction", 0},
		{"not above the declared severity", CheckTypeStorage, map[string]any{
			fieldUsedPct:   pred(">=", 80),
			CheckKeyLevels: map[string]any{"error": map[string]any{fieldUsedPct: pred(">=", 95)}},
		}, severity.Error, "levels.error is not above the check's severity error", 0},
		{"equality cannot tighten", CheckTypeStorage, map[string]any{
			fieldUsedPct:   pred("==", 50),
			CheckKeyLevels: map[string]any{"error": map[string]any{fieldUsedPct: pred("==", 60)}},
		}, severity.Warning, "only an ordered comparison", 0},
		{"undeclared floor is error", CheckTypeStorage, map[string]any{
			fieldUsedPct:   pred(">=", 80),
			CheckKeyLevels: map[string]any{"warning": map[string]any{fieldUsedPct: pred(">=", 95)}},
		}, "", "not above the check's severity error", 0},
		// Another reports: leaves the tiers nothing to grade — typically tiers
		// a catalog check brought to an override that made it a sensor.
		{"non-default reports", CheckTypeStorage, map[string]any{
			fieldUsedPct:    pred(">=", 80),
			CheckKeyReports: ReportsHealth,
			CheckKeyLevels:  map[string]any{"error": map[string]any{fieldUsedPct: pred(">=", 95)}},
		}, severity.Warning, "levels grade the default reports: condition, not health; ignored", 0},
		// A defaulted type's built-in threshold (any kill) is the base a
		// level must tighten.
		{"loosens the implicit threshold", CheckTypeOOM, map[string]any{
			CheckKeyLevels: map[string]any{"critical": map[string]any{CheckKeyDelta: pred("<", 3)}},
		}, "", "opposite direction", 0},
		{"restates the implicit threshold", CheckTypeRAID, map[string]any{
			CheckKeyLevels: map[string]any{"critical": map[string]any{fieldDegraded: pred(">=", 0)}},
		}, "", "not stricter than > 0", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidateLevels(tt.typ, tt.entry, tt.declared)
			if len(got.Errors) != 0 {
				t.Fatalf("errors = %q, want none: a lax tier is ignored, not invalid", got.Errors)
			}
			if !strings.Contains(strings.Join(got.Ignored, "\n"), tt.want) || len(got.kept) != tt.kept {
				t.Fatalf("ignored = %q kept = %d, want %q and %d kept", got.Ignored, len(got.kept), tt.want, tt.kept)
			}
		})
	}
}

func TestStorageLevelsGradeTheSameSample(t *testing.T) {
	tests := []struct {
		used float64
		ok   bool
		want severity.Level
	}{
		{70, false, severity.Warning}, // healthy: severity is only the declaration
		{85, true, severity.Warning},
		{96, true, severity.Error},
		{99.5, true, severity.Critical},
	}
	for _, tt := range tests {
		check, err := BuildInline("disk", storageLevels(), Deps{StorageUsage: fakeStorage(tt.used, 100-tt.used, 1, 100)})
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		res := check.Run(context.Background())
		if res.OK != tt.ok || res.Severity != tt.want {
			t.Errorf("used %.1f%%: ok=%v severity=%q, want ok=%v severity=%q", tt.used, res.OK, res.Severity, tt.ok, tt.want)
		}
	}
	// An undeclared base is an error; a level still raises it.
	entry := storageLevels()
	delete(entry, CheckKeySeverity)
	delete(entry[CheckKeyLevels].(map[string]any), "error")
	check, err := BuildInline("disk", entry, Deps{StorageUsage: fakeStorage(99.5, 0.5, 1, 100)})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := check.Run(context.Background()).Severity; got != severity.Critical {
		t.Fatalf("undeclared base at 99.5%% = %q, want critical", got)
	}
}

func TestLevelsNeverGradeAnUnavailableSample(t *testing.T) {
	failing := func(string) (StorageStats, error) { return StorageStats{}, context.DeadlineExceeded }
	check, err := BuildInline("disk", storageLevels(), Deps{StorageUsage: failing})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if res := check.Run(context.Background()); !res.Unavailable || res.Severity != severity.Warning {
		t.Fatalf("unavailable sample = %+v, want unavailable at the declared warning", res)
	}
}

func TestBuildRejectsMalformedLevels(t *testing.T) {
	entry := storageLevels()
	entry[CheckKeyLevels] = map[string]any{"urgent": map[string]any{fieldUsedPct: pred(">=", 99)}}
	if _, err := BuildInline("disk", entry, Deps{}); err == nil || !strings.Contains(err.Error(), "levels.urgent") {
		t.Fatalf("build error = %v, want the malformed tier named", err)
	}
}

func TestSingleThresholdLevels(t *testing.T) {
	oom := func(kills ...uint64) OomSamplerFunc {
		i := 0
		return func() (uint64, bool) { v := kills[i]; i++; return v, true }
	}
	entry := map[string]any{
		CheckKeyType:   CheckTypeOOM,
		CheckKeyLevels: map[string]any{"critical": map[string]any{CheckKeyDelta: pred(">=", 5)}},
	}
	check, err := BuildInline("oom", entry, Deps{OomSampler: oom(10, 11, 17)})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if res := check.Run(context.Background()); res.OK || res.Severity.Valid() {
		t.Fatalf("baseline = %+v, want no fire and no grade", res)
	}
	if res := check.Run(context.Background()); !res.OK || res.Severity.Resolved() != severity.Error {
		t.Fatalf("one kill = ok %v severity %q, want an error", res.OK, res.Severity)
	}
	if res := check.Run(context.Background()); !res.OK || res.Severity != severity.Critical {
		t.Fatalf("six kills = ok %v severity %q, want critical", res.OK, res.Severity)
	}
}

func TestMetricLevels(t *testing.T) {
	reading := metrics.Reading{Percent: 60, HasPercent: true, Ready: true}
	source := func(string, string) (metrics.Reading, bool) { return reading, true }
	entry := map[string]any{
		CheckKeyType: CheckTypeMetric, CheckKeyName: "memory", CheckKeyOp: ">", CheckKeyValue: "30%",
		CheckKeySeverity: "warning",
		CheckKeyLevels:   map[string]any{"error": map[string]any{CheckKeyOp: ">", CheckKeyValue: "50%"}},
	}
	check, err := BuildInline("mem", entry, Deps{Metrics: source})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if res := check.Run(context.Background()); !res.OK || res.Severity != severity.Error {
		t.Fatalf("60%% = ok %v severity %q, want error", res.OK, res.Severity)
	}
	reading.Percent = 40
	if res := check.Run(context.Background()); !res.OK || res.Severity != severity.Warning {
		t.Fatalf("40%% = ok %v severity %q, want warning", res.OK, res.Severity)
	}
	// A level in the other form cannot tighten a percentage threshold.
	entry[CheckKeyLevels] = map[string]any{"error": map[string]any{CheckKeyOp: ">", CheckKeyValue: "500"}}
	if got := ValidateLevels(CheckTypeMetric, entry, severity.Warning); len(got.Ignored) != 1 || !strings.Contains(got.Ignored[0], "same form") {
		t.Fatalf("mixed forms = %+v, want the tier ignored", got)
	}
}

func TestCommandQueueLevels(t *testing.T) {
	entry := func() map[string]any {
		return map[string]any{
			CheckKeyType:         CheckTypeCommand,
			CheckKeyCommand:      []any{"exim", "-bpc"},
			CheckKeyExpectStdout: pred("<=", 200),
			CheckKeySeverity:     "warning",
			CheckKeyLevels:       map[string]any{"error": map[string]any{CheckKeyExpectStdout: pred("<=", 1000)}},
		}
	}
	tests := []struct {
		stdout string
		ok     bool
		want   severity.Level
	}{
		{"150\n", true, severity.Warning},
		{"500\n", false, severity.Warning},
		{"1500\n", false, severity.Error},
		{"queue busy\n", false, severity.Warning}, // not a number: never escalates
	}
	for _, tt := range tests {
		check, err := BuildInline("queue", entry(), Deps{Runner: execxtest.Outputs(tt.stdout)})
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		res := check.Run(context.Background())
		if res.OK != tt.ok || res.Severity != tt.want {
			t.Errorf("stdout %q: ok=%v severity=%q, want ok=%v severity=%q", tt.stdout, res.OK, res.Severity, tt.ok, tt.want)
		}
	}
}

func TestCertSelfGradeAndLevels(t *testing.T) {
	cert := func(daysLeft int) CertSample {
		s := healthyCert()
		s.NotAfter = time.Now().Add(time.Duration(daysLeft)*24*time.Hour + time.Hour)
		return s
	}
	entry := func(declared string) map[string]any {
		e := map[string]any{
			CheckKeyType: CheckTypeCert, CheckKeyHost: "api.example.com", CheckKeyExpiresInDays: 21,
			CheckKeyLevels: map[string]any{"error": map[string]any{CheckKeyExpiresInDays: 7}},
		}
		if declared != "" {
			e[CheckKeySeverity] = declared
		}
		return e
	}
	tests := []struct {
		name     string
		declared string
		days     int
		want     severity.Level
	}{
		{"renewal window is an advisory", "", 14, severity.Warning},
		{"inside the error window", "", 3, severity.Error},
		{"expired is critical", "", -2, severity.Critical},
		{"a declaration beats the self-grade", "warning", -2, severity.Error},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check, err := BuildInline("cert", entry(tt.declared), Deps{CertSampler: fakeCert(cert(tt.days))})
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if res := check.Run(context.Background()); res.OK || res.Severity != tt.want {
				t.Fatalf("ok=%v severity=%q, want a failure graded %q", res.OK, res.Severity, tt.want)
			}
		})
	}
	// A broken chain on a certificate far from expiry is an error.
	broken := healthyCert()
	broken.VerifyError = "unknown authority"
	check, err := BuildInline("cert", map[string]any{CheckKeyType: CheckTypeCert, CheckKeyHost: "api.example.com"},
		Deps{CertSampler: fakeCert(broken)})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if res := check.Run(context.Background()); res.OK || res.Severity != severity.Error {
		t.Fatalf("broken chain = ok %v severity %q, want error", res.OK, res.Severity)
	}
}

func TestLevelFloorAndGradableTypes(t *testing.T) {
	floor := func(typ string) severity.Level { return gradeSupports[typ].levelFloor() }
	if floor(CheckTypeSmart) != severity.Warning || floor(CheckTypeCert) != severity.Warning || floor(CheckTypeStorage) != severity.Error {
		t.Fatal("level floors do not match the types' self-grades")
	}
	for typ := range gradeSupports {
		if _, known := TypeInfoFor(typ); !known {
			t.Errorf("gradable type %q is not a registered check type", typ)
		}
		support := gradeSupports[typ]
		if support.kind == gradePredicates && support.keys == nil && len(PredicateFieldsFor(typ)) == 0 {
			t.Errorf("gradable type %q grades predicates but declares none", typ)
		}
	}
}
