package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sermo/internal/appinspect"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/rules"
)

func TestArtifactWatchInterval(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want time.Duration
	}{
		{
			name: "default",
			cfg:  &config.Config{},
			want: DefaultEngineArtifactInterval,
		},
		{
			name: "engine override",
			cfg: &config.Config{Global: config.Global{Raw: map[string]any{
				config.SectionEngine: map[string]any{config.EngineKeyArtifactInterval: "7m"},
			}}},
			want: 7 * time.Minute,
		},
		{
			name: "library override",
			cfg: &config.Config{
				Global:       config.Global{Raw: map[string]any{config.SectionEngine: map[string]any{config.EngineKeyArtifactInterval: "7m"}}},
				LibraryNames: []string{"demo"},
				Libraries: map[string]*config.Document{
					"demo": {Name: "demo", Body: map[string]any{"interval": "11m"}},
				},
			},
			want: 11 * time.Minute,
		},
		{
			name: "app override",
			cfg: &config.Config{
				Global:   config.Global{Raw: map[string]any{config.SectionEngine: map[string]any{config.EngineKeyArtifactInterval: "7m"}}},
				AppNames: []string{"demo"},
				Apps: map[string]*config.Document{
					"demo": {Name: "demo", Body: map[string]any{"interval": "9m"}},
				},
			},
			want: 9 * time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			category := config.CategoryLibrary
			if tt.name == "app override" {
				category = config.CategoryApp
			}
			if got := artifactWatchInterval(tt.cfg, category, "demo"); got != tt.want {
				t.Fatalf("artifactWatchInterval() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestArtifactSamplesShareAppVersion(t *testing.T) {
	samples := NewArtifactSamples()
	samples.RegisterApp("demo")
	w := &Worker{artifactSamples: samples, appVersions: map[string]string{}, appVersionsLast: map[string]string{}}

	samples.StoreAppVersion("demo", "1.2.3", appinspect.StatusOK)
	if changed, err := w.changedAppVersion(context.Background(), "demo", 3); err != nil || changed {
		t.Fatalf("first app sample = changed:%t err:%v, want false nil", changed, err)
	}
	samples.StoreAppVersion("demo", "1.3.0", appinspect.StatusOK)
	if changed, err := w.changedAppVersion(context.Background(), "demo", 3); err != nil || !changed {
		t.Fatalf("updated app sample = changed:%t err:%v, want true nil", changed, err)
	}
}

func TestChangedRulePaths(t *testing.T) {
	tree := map[string]any{rules.SectionRules: map[string]any{
		"one": map[string]any{rules.RuleFieldIf: map[string]any{rules.ConditionChanged: map[string]any{rules.FieldPath: "/etc/demo.conf"}}},
		"two": map[string]any{rules.RuleFieldIf: map[string]any{rules.ConditionChanged: map[string]any{rules.FieldPath: "/etc/demo.conf"}}},
		"three": map[string]any{rules.RuleFieldIf: map[string]any{rules.ConditionAnd: []any{
			map[string]any{rules.ConditionChanged: map[string]any{rules.FieldPath: "/etc/other.conf"}},
		}}},
	}}
	paths := changedRulePaths(tree)
	if len(paths) != 2 || paths[0] != "/etc/demo.conf" || paths[1] != "/etc/other.conf" {
		t.Fatalf("changedRulePaths = %v", paths)
	}
	apps := changedRuleApps(tree)
	if len(apps) != 0 {
		t.Fatalf("changedRuleApps = %v, want none", apps)
	}
}

func TestChangedRuleApps(t *testing.T) {
	tree := map[string]any{rules.SectionRules: map[string]any{
		"one": map[string]any{rules.RuleFieldIf: map[string]any{rules.ConditionChanged: map[string]any{rules.FieldApp: "demo"}}},
		"two": map[string]any{rules.RuleFieldIf: map[string]any{rules.ConditionOr: []any{
			map[string]any{rules.ConditionChanged: map[string]any{rules.FieldApp: "demo"}},
			map[string]any{rules.ConditionChanged: map[string]any{rules.FieldApp: "other"}},
		}}},
	}}
	apps := changedRuleApps(tree)
	if len(apps) != 2 || apps[0] != "demo" || apps[1] != "other" {
		t.Fatalf("changedRuleApps = %v", apps)
	}
}

func TestServiceArtifactInterval(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		config.SectionEngine: map[string]any{config.EngineKeyArtifactInterval: "7m"},
	}}}
	if got := serviceArtifactInterval(cfg, map[string]any{}); got != 7*time.Minute {
		t.Fatalf("default service artifact interval = %s, want 7m", got)
	}
	if got := serviceArtifactInterval(cfg, map[string]any{config.EntryKeyInterval: "45s"}); got != 45*time.Second {
		t.Fatalf("service artifact override = %s, want 45s", got)
	}
}

func TestArtifactSamplesDeferChangeUntilSampled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "libdemo.so")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	samples := NewArtifactSamples()
	samples.RegisterFile(path)
	baseline := map[string]string{}

	if changed, err := artifactPathChanged(baseline, path, samples); err != nil || changed {
		t.Fatalf("unsampled library = changed:%t err:%v, want false nil", changed, err)
	}
	samples.StoreFile(path)
	if changed, err := artifactPathChanged(baseline, path, samples); err != nil || changed {
		t.Fatalf("first sample = changed:%t err:%v, want false nil", changed, err)
	}
	if err := os.WriteFile(path, []byte("second value"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := artifactPathChanged(baseline, path, samples); err != nil || changed {
		t.Fatalf("stale sample = changed:%t err:%v, want false nil", changed, err)
	}
	samples.StoreFile(path)
	if changed, err := artifactPathChanged(baseline, path, samples); err != nil || !changed {
		t.Fatalf("updated sample = changed:%t err:%v, want true nil", changed, err)
	}
}

func TestArtifactPathChangedUsesCachedSampleWithoutFingerprinting(t *testing.T) {
	const path = "/etc/demo.conf"
	samples := NewArtifactSamples()
	samples.RegisterFile(path)
	samples.StoreFile(path)
	fingerprint, _, _ := samples.FileFingerprint(path)
	baseline := map[string]string{path: fingerprint}

	if changed, err := artifactPathChangedWithFingerprint(baseline, path, samples, func(string) string {
		t.Fatal("cached artifact must not call the direct fingerprinter")
		return ""
	}); err != nil || changed {
		t.Fatalf("cached artifact = changed:%t err:%v, want false nil", changed, err)
	}
}

func TestAcknowledgeChangesRefreshesArtifactSample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "demo.conf")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	samples := NewArtifactSamples()
	samples.RegisterFile(path)
	samples.StoreFile(path)
	if err := os.WriteFile(path, []byte("changed after sample"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := fileFingerprint(path)

	w := &Worker{artifactSamples: samples, libBaseline: map[string]string{path: "previous"}}
	w.acknowledgeChanges()
	if got := w.libBaseline[path]; got != want {
		t.Fatalf("acknowledged fingerprint = %q, want refreshed sample %q", got, want)
	}
	if got, _, _ := samples.FileFingerprint(path); got != want {
		t.Fatalf("cached fingerprint = %q, want refreshed sample %q", got, want)
	}
}

func TestArtifactSamplesCacheAppStatus(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		wantErr bool
	}{
		{name: "missing binary", status: appinspect.StatusNotInstalled},
		{name: "missing required version", status: appinspect.StatusPrefixNotInstalled + " version mismatch"},
		{name: "probe failure", status: appinspect.StatusPrefixError + " exit 1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			samples := NewArtifactSamples()
			samples.RegisterApp("demo")
			runner := &sequenceRunner{stdout: []string{"demo v1.2.3"}}
			w := &Worker{
				artifactSamples: samples,
				appVersionCmd:   map[string]appVersionCmd{"demo": {argv: []string{"demo", "--version"}}},
				appVersions:     map[string]string{},
				appVersionsLast: map[string]string{},
				CheckDeps:       checks.Deps{Runner: runner},
			}
			samples.StoreAppVersion("demo", "", tt.status)

			changed, err := w.changedAppVersion(context.Background(), "demo", 3)
			if changed || (err != nil) != tt.wantErr {
				t.Fatalf("cached app status %q = changed:%t err:%v, want false error:%t", tt.status, changed, err, tt.wantErr)
			}
			if runner.calls != 0 {
				t.Fatalf("worker must not re-run a cached app probe, calls=%d", runner.calls)
			}
		})
	}
}

func TestBuildArtifactWatchesSamplesChangedMissingApp(t *testing.T) {
	missingBinary := filepath.Join(t.TempDir(), "missing-app")
	cfg := &config.Config{
		Global: config.Global{Raw: map[string]any{
			config.SectionEngine: map[string]any{config.EngineKeyArtifactInterval: "7m"},
		}},
		AppNames: []string{"demo"},
		Apps: map[string]*config.Document{
			"demo": {Name: "demo", Kind: config.CategoryApp, Body: map[string]any{
				"variables": map[string]any{"binary": missingBinary},
				"preflight": map[string]any{
					"version": map[string]any{"type": "command", "command": []any{"${binary}", "--version"}},
				},
			}},
		},
		ServiceNames: []string{"web"},
		Services: map[string]*config.Document{
			"web": {Name: "web", Kind: config.CategoryService, Body: map[string]any{
				"service": "web",
				"apps":    []string{"demo"},
				"rules": map[string]any{
					"restart-after-app-change": map[string]any{
						rules.RuleFieldIf: map[string]any{rules.ConditionChanged: map[string]any{rules.FieldApp: "demo"}},
					},
				},
			}},
		},
	}
	samples := NewArtifactSamples()
	watches := BuildArtifactWatches(t.Context(), cfg, Deps{ArtifactSamples: samples})
	var sampler *Watch
	for _, watch := range watches {
		if watch.Name == artifactWatchNamePrefix+"demo" {
			sampler = watch
			break
		}
	}
	if sampler == nil || sampler.Cycle == nil {
		t.Fatalf("artifact app watches = %+v, want silent sampler for demo", watches)
	}
	if sampler.Interval != 7*time.Minute {
		t.Fatalf("sampler interval = %s, want 7m", sampler.Interval)
	}

	sampler.Cycle(context.Background())
	_, status, sampled := samples.AppVersion("demo")
	if !sampled || status != appinspect.StatusNotInstalled {
		t.Fatalf("missing app sample = sampled:%t status:%q, want true %q", sampled, status, appinspect.StatusNotInstalled)
	}
}

func TestBuildArtifactPathWatchesSampleSilently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "demo.conf")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		ServiceNames: []string{"web"},
		Services: map[string]*config.Document{
			"web": {Name: "web", Kind: config.CategoryService, Body: map[string]any{
				"service": "web",
				"rules": map[string]any{
					"restart-after-config-change": map[string]any{
						rules.RuleFieldIf: map[string]any{rules.ConditionChanged: map[string]any{rules.FieldPath: path}},
					},
				},
			}},
		},
	}
	samples := NewArtifactSamples()
	var events []Event
	watches := BuildArtifactWatches(t.Context(), cfg, Deps{
		ArtifactSamples: samples,
		Emit:            func(event Event) { events = append(events, event) },
	})
	name := artifactWatchNamePrefix + path
	var sampler *Watch
	for _, watch := range watches {
		if watch.Name == name {
			sampler = watch
			break
		}
	}
	if sampler == nil || sampler.Cycle == nil || sampler.Check != nil {
		t.Fatalf("artifact path sampler = %+v, want a custom sampling cycle", sampler)
	}

	sampler.RunCycle(context.Background())
	if _, tracked, sampled := samples.FileFingerprint(path); !tracked || !sampled {
		t.Fatalf("artifact path sample = tracked:%t sampled:%t, want true true", tracked, sampled)
	}
	if len(events) != 0 {
		t.Fatalf("artifact path sampler must not emit events, got %+v", events)
	}

	baseline := map[string]string{}
	if changed, err := artifactPathChanged(baseline, path, samples); err != nil || changed {
		t.Fatalf("first artifact sample = changed:%t err:%v, want false nil", changed, err)
	}
	if err := os.WriteFile(path, []byte("updated"), 0o644); err != nil {
		t.Fatal(err)
	}
	sampler.RunCycle(context.Background())
	if changed, err := artifactPathChanged(baseline, path, samples); err != nil || !changed {
		t.Fatalf("updated artifact sample = changed:%t err:%v, want true nil", changed, err)
	}
	if len(events) != 0 {
		t.Fatalf("artifact path sampler must remain silent after a change, got %+v", events)
	}
}
