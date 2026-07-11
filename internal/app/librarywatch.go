package app

import (
	"context"
	"sync"
	"time"

	"sermo/internal/appinspect"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
)

const libraryWatchNamePrefix = "library:"

// LibrarySamples stores the latest cadence-limited observation for each
// catalog library path. Workers use it for library changed conditions.
type LibrarySamples struct {
	mu      sync.RWMutex
	entries map[string]librarySample
}

type librarySample struct {
	fingerprint string
	sampled     bool
}

// NewLibrarySamples creates an empty catalog library sample cache.
func NewLibrarySamples() *LibrarySamples {
	return &LibrarySamples{entries: map[string]librarySample{}}
}

// Register marks path as a catalog library path before its first sample.
func (s *LibrarySamples) Register(path string) {
	if s == nil || path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[path]; !ok {
		s.entries[path] = librarySample{}
	}
}

// Store records the latest filesystem fingerprint for path.
func (s *LibrarySamples) Store(path string) {
	if s == nil || path == "" {
		return
	}
	s.mu.Lock()
	s.entries[path] = librarySample{fingerprint: fileFingerprint(path), sampled: true}
	s.mu.Unlock()
}

// Fingerprint returns the cached fingerprint, whether path is tracked, and
// whether the library monitor has sampled it at least once.
func (s *LibrarySamples) Fingerprint(path string) (string, bool, bool) {
	if s == nil {
		return "", false, false
	}
	s.mu.RLock()
	entry, tracked := s.entries[path]
	s.mu.RUnlock()
	return entry.fingerprint, tracked, entry.sampled
}

type libraryCheck struct {
	name    string
	inspect func(context.Context) appinspect.Report
	samples *LibrarySamples
}

func (c libraryCheck) Name() string { return c.name }

func (c libraryCheck) Run(ctx context.Context) checks.Result {
	report := c.inspect(ctx)
	c.samples.Store(report.Binary)
	result := checks.Result{Check: c.name, OK: report.Status == appinspect.StatusOK, Message: report.Status}
	if !result.OK && report.Output != "" {
		result.Data = map[string]any{checks.DataKeyOutput: report.Output}
	}
	return result
}

// libraryWatchInterval resolves a library's explicit interval, falling back to
// engine.libs_interval and then the documented five-minute default.
func libraryWatchInterval(cfg *config.Config, name string) time.Duration {
	interval := EngineDuration(cfg, config.EngineKeyLibsInterval, DefaultEngineLibsInterval)
	resolved, errs := cfg.ResolveCatalog(config.CategoryLibrary, name)
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
	samples := deps.LibrarySamples
	if samples == nil {
		samples = NewLibrarySamples()
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
		samples.Register(report.Binary)
		out = append(out, &Watch{
			Name:      libraryWatchNamePrefix + name,
			CheckType: config.CategoryLibrary,
			Check: libraryCheck{name: name, samples: samples, inspect: func(ctx context.Context) appinspect.Report {
				return appinspect.InspectCategoryOne(ctx, runner, cfg, config.CategoryLibrary, name,
					appinspect.WithUserLookup(deps.UserLookup))
			}},
			FireOnFail: true,
			Interval:   libraryWatchInterval(cfg, name),
			Notifiers:  notifiers,
			Settling:   deps.Settling,
			Now:        deps.Now,
			Emit:       deps.Emit,
			StateStore: deps.WatchState,
		})
	}
	return out
}
