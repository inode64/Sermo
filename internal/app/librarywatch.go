package app

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"sermo/internal/appinspect"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/rules"
)

const (
	libraryWatchNamePrefix  = "library:"
	artifactWatchNamePrefix = "artifact:"
	artifactWatchCheckType  = "artifact"
)

// ArtifactSamples stores cadence-limited catalog app and file observations.
// Workers use it for app/library/config changed conditions instead of probing on
// every service cycle.
type ArtifactSamples struct {
	mu          sync.RWMutex
	files       map[string]artifactFileSample
	appVersions map[string]artifactAppSample
}

type artifactFileSample struct {
	fingerprint string
	sampled     bool
}

type artifactAppSample struct {
	version string
	sampled bool
	err     error
}

// NewArtifactSamples creates an empty artifact sample cache.
func NewArtifactSamples() *ArtifactSamples {
	return &ArtifactSamples{files: map[string]artifactFileSample{}, appVersions: map[string]artifactAppSample{}}
}

// RegisterFile marks a file artifact before its first sample.
func (s *ArtifactSamples) RegisterFile(path string) {
	if s == nil || path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.files[path]; !ok {
		s.files[path] = artifactFileSample{}
	}
}

// StoreFile records the latest filesystem fingerprint for path.
func (s *ArtifactSamples) StoreFile(path string) {
	if s == nil || path == "" {
		return
	}
	s.mu.Lock()
	s.files[path] = artifactFileSample{fingerprint: fileFingerprint(path), sampled: true}
	s.mu.Unlock()
}

// FileFingerprint returns the cached fingerprint, whether path is tracked, and
// whether its artifact monitor has sampled it at least once.
func (s *ArtifactSamples) FileFingerprint(path string) (string, bool, bool) {
	if s == nil {
		return "", false, false
	}
	s.mu.RLock()
	entry, tracked := s.files[path]
	s.mu.RUnlock()
	return entry.fingerprint, tracked, entry.sampled
}

// RegisterApp marks an app before its first sample and reports whether it was
// newly registered.
func (s *ArtifactSamples) RegisterApp(name string) bool {
	if s == nil || name == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.appVersions[name]; !ok {
		s.appVersions[name] = artifactAppSample{}
		return true
	}
	return false
}

// StoreAppVersion records one app version observation. A failed probe remains a
// sampled observation so workers can return its error rather than re-running it.
func (s *ArtifactSamples) StoreAppVersion(name, version string, err error) {
	if s == nil || name == "" {
		return
	}
	s.mu.Lock()
	s.appVersions[name] = artifactAppSample{version: version, sampled: true, err: err}
	s.mu.Unlock()
}

// AppVersion returns the latest sampled app version and its probe outcome.
func (s *ArtifactSamples) AppVersion(name string) (string, bool, error) {
	if s == nil {
		return "", false, nil
	}
	s.mu.RLock()
	entry, tracked := s.appVersions[name]
	s.mu.RUnlock()
	if !tracked || !entry.sampled {
		return "", false, nil
	}
	return entry.version, true, entry.err
}

type libraryCheck struct {
	name    string
	inspect func(context.Context) appinspect.Report
	samples *ArtifactSamples
}

func (c libraryCheck) Name() string { return c.name }

func (c libraryCheck) Run(ctx context.Context) checks.Result {
	report := c.inspect(ctx)
	c.samples.StoreFile(report.Binary)
	result := checks.Result{Check: c.name, OK: report.Status == appinspect.StatusOK, Message: report.Status}
	if !result.OK && report.Output != "" {
		result.Data = map[string]any{checks.DataKeyOutput: report.Output}
	}
	return result
}

type artifactFileCheck struct {
	name    string
	path    string
	samples *ArtifactSamples
}

func (c artifactFileCheck) Name() string { return c.name }

func (c artifactFileCheck) Run(context.Context) checks.Result {
	c.samples.StoreFile(c.path)
	return checks.Result{Check: c.name, OK: true, Message: "sampled"}
}

// artifactWatchInterval resolves a catalog app or library's explicit interval,
// falling back to engine.artifact_interval and then the documented five-minute default.
func artifactWatchInterval(cfg *config.Config, category, name string) time.Duration {
	interval := EngineDuration(cfg, config.EngineKeyArtifactInterval, DefaultEngineArtifactInterval)
	resolved, errs := cfg.ResolveCatalog(category, name)
	if len(errs) == 0 {
		if override := cfgval.Duration(resolved.Tree[config.EntryKeyInterval]); override > 0 {
			interval = override
		}
	}
	return interval
}

// BuildLibraryWatches builds one monitor for every installed catalog library.
// Library events are regular watches named "library:<name>" so they remain
// distinct from application events without expanding the persisted event schema.
func BuildLibraryWatches(cfg *config.Config, deps Deps) []*Watch {
	if cfg == nil {
		return nil
	}
	samples := deps.ArtifactSamples
	if samples == nil {
		samples = NewArtifactSamples()
	}
	runner := deps.ExecxRunner
	reports := appinspect.List(context.Background(), runner, cfg, config.CategoryLibrary, false,
		appinspect.WithUserLookup(deps.UserLookup))
	if len(reports) == 0 {
		return nil
	}
	notifiers := resolveNotifiers(deps.GlobalNotify, deps.Notifiers)
	out := make([]*Watch, 0, len(reports))
	for _, report := range reports {
		name := report.Name
		samples.RegisterFile(report.Binary)
		out = append(out, &Watch{
			Name:      libraryWatchNamePrefix + name,
			CheckType: config.CategoryLibrary,
			Check: libraryCheck{name: name, samples: samples, inspect: func(ctx context.Context) appinspect.Report {
				return appinspect.InspectCategoryOne(ctx, runner, cfg, config.CategoryLibrary, name,
					appinspect.WithUserLookup(deps.UserLookup))
			}},
			FireOnFail: true,
			Interval:   artifactWatchInterval(cfg, config.CategoryLibrary, name),
			Notifiers:  notifiers,
			Settling:   deps.Settling,
			Now:        deps.Now,
			Emit:       deps.Emit,
			StateStore: deps.WatchState,
		})
	}
	return out
}

// BuildArtifactWatches builds all cadence-limited catalog and service artifact
// monitors. Service workers consume their samples rather than re-probing a
// changed artifact on every service cycle.
func BuildArtifactWatches(cfg *config.Config, deps Deps) []*Watch {
	if cfg == nil {
		return nil
	}
	samples := deps.ArtifactSamples
	if samples == nil {
		samples = NewArtifactSamples()
		deps.ArtifactSamples = samples
	}
	out := BuildLibraryWatches(cfg, deps)
	out = append(out, BuildAppWatches(cfg, deps)...)
	out = append(out, buildArtifactAppWatches(cfg, deps, samples)...)
	return append(out, buildArtifactPathWatches(cfg, deps, samples)...)
}

// buildArtifactAppWatches samples changed-app dependencies which do not have a
// regular app watch because they are currently not installed. The cycle is
// intentionally silent: its only purpose is to refresh the shared sample, so a
// failed app probe is cached at the artifact cadence rather than retried by each
// service rule.
func buildArtifactAppWatches(cfg *config.Config, deps Deps, samples *ArtifactSamples) []*Watch {
	apps := map[string]struct{}{}
	for _, name := range cfg.SortedServiceNames() {
		resolved, errs := cfg.Resolve(name)
		if len(errs) > 0 || resolved.Tree == nil {
			continue
		}
		for _, app := range changedRuleApps(resolved.Tree) {
			apps[app] = struct{}{}
		}
	}
	if len(apps) == 0 {
		return nil
	}

	runner := deps.ExecxRunner
	out := make([]*Watch, 0, len(apps))
	for _, name := range slices.Sorted(maps.Keys(apps)) {
		if !samples.RegisterApp(name) {
			continue // The regular installed-app watch already samples it.
		}
		appName := name
		out = append(out, &Watch{
			Name:      artifactWatchNamePrefix + appName,
			CheckType: artifactWatchCheckType,
			Cycle: func(ctx context.Context) {
				report := appinspect.InspectOne(ctx, runner, cfg, appName,
					appinspect.WithUserLookup(deps.UserLookup))
				storeAppSample(samples, appName, report)
			},
			Interval:   artifactWatchInterval(cfg, config.CategoryApp, appName),
			Settling:   deps.Settling,
			Now:        deps.Now,
			Emit:       deps.Emit,
			StateStore: deps.WatchState,
		})
	}
	return out
}

func buildArtifactPathWatches(cfg *config.Config, deps Deps, samples *ArtifactSamples) []*Watch {
	paths := map[string]time.Duration{}
	for _, name := range cfg.SortedServiceNames() {
		resolved, errs := cfg.Resolve(name)
		if len(errs) > 0 || resolved.Tree == nil {
			continue
		}
		interval := serviceArtifactInterval(cfg, resolved.Tree)
		for _, path := range changedRulePaths(resolved.Tree) {
			if prior, found := paths[path]; !found || interval < prior {
				paths[path] = interval
			}
		}
	}
	if len(paths) == 0 {
		return nil
	}
	out := make([]*Watch, 0, len(paths))
	for _, path := range slices.Sorted(maps.Keys(paths)) {
		if _, tracked, _ := samples.FileFingerprint(path); tracked {
			continue // installed library watcher already owns this file sample.
		}
		samples.RegisterFile(path)
		name := artifactWatchNamePrefix + path
		out = append(out, &Watch{
			Name:       name,
			CheckType:  artifactWatchCheckType,
			Check:      artifactFileCheck{name: name, path: path, samples: samples},
			Interval:   paths[path],
			Settling:   deps.Settling,
			Now:        deps.Now,
			Emit:       deps.Emit,
			StateStore: deps.WatchState,
		})
	}
	return out
}

func changedRulePaths(tree map[string]any) []string {
	return changedRuleValues(tree, rules.FieldPath)
}

func changedRuleApps(tree map[string]any) []string {
	return changedRuleValues(tree, rules.FieldApp)
}

func changedRuleValues(tree map[string]any, field string) []string {
	raw, _ := tree[rules.SectionRules].(map[string]any)
	if len(raw) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	for _, entry := range raw {
		rule, _ := entry.(map[string]any)
		collectChangedRuleValues(rule[rules.RuleFieldIf], field, seen)
	}
	return slices.Sorted(maps.Keys(seen))
}

func collectChangedRuleValues(node any, field string, seen map[string]struct{}) {
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	if changed, ok := m[rules.ConditionChanged].(map[string]any); ok {
		if value := cfgval.String(changed[field]); value != "" {
			seen[value] = struct{}{}
		}
	}
	for _, key := range []string{rules.ConditionAnd, rules.ConditionOr} {
		if children, ok := m[key].([]any); ok {
			for _, child := range children {
				collectChangedRuleValues(child, field, seen)
			}
		}
	}
	collectChangedRuleValues(m[rules.ConditionNot], field, seen)
}

func serviceArtifactInterval(cfg *config.Config, tree map[string]any) time.Duration {
	if interval := cfgval.Duration(tree[config.EntryKeyInterval]); interval > 0 {
		return interval
	}
	return EngineDuration(cfg, config.EngineKeyArtifactInterval, DefaultEngineArtifactInterval)
}
