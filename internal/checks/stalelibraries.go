package checks

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/process"
	"sermo/internal/severity"
	"sermo/internal/strutil"
)

const (
	// CheckKeyStableFor is the minimum unchanged observation window before a
	// stale_libraries result may drive an explicitly configured operation rule.
	CheckKeyStableFor       = "stable_for"
	staleLibrariesInterval  = time.Minute
	staleLibrariesStableFor = 5 * time.Minute
	staleLibrariesTimeout   = 30 * time.Second
)

// StaleLibrariesFunc samples mappings attributed to one service. Host checks
// use ScanLibraries directly and can only report, never authorize an action.
type StaleLibrariesFunc func(context.Context) (process.LibraryReport, error)

type staleLibrariesCheck struct {
	base
	sample    StaleLibrariesFunc
	stableFor time.Duration
	clock     func() time.Time
	mu        sync.Mutex
	last      time.Time
	since     time.Time
	previous  map[process.StaleLibrary]struct{}
	cached    Result
}

// A cancelled queue entry or recovered panic must retain the same fail-closed
// operation policy as an incomplete scan, before Run can produce a result.
func (c *staleLibrariesCheck) resultMetadata() Result {
	res := c.base.resultMetadata()
	res.ActionBlocked = true
	return res
}

func buildStaleLibrariesCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	if b.reports == "" {
		b.reports = ReportsState
	}
	stableFor := staleLibrariesStableFor
	if raw, present := entry[CheckKeyStableFor]; present {
		stableFor = cfgval.Duration(raw)
		if stableFor <= 0 {
			return nil, "stale_libraries stable_for must be a positive duration"
		}
	}
	if b.timeout <= 0 {
		b.timeout = staleLibrariesTimeout
	}
	if !b.severity.Valid() {
		b.severity = severity.Warning
	}
	if deps.Service != "" && deps.StaleLibraries == nil {
		return nil, "stale_libraries needs service process discovery"
	}
	return &staleLibrariesCheck{base: b, sample: deps.StaleLibraries, stableFor: stableFor, clock: time.Now}, ""
}

func (c *staleLibrariesCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	if !c.mu.TryLock() {
		res := c.unavailableResult("library inventory already running", run.start)
		res.ActionBlocked = true
		return res
	}
	defer c.mu.Unlock()
	now := c.clock()
	if err := ctx.Err(); err != nil {
		res := c.libraryResult(process.LibraryReport{}, err, now, run.start)
		c.last, c.cached = now, res
		return res
	}
	// The internal floor also protects ad-hoc rule references from repeatedly
	// scanning procfs. The normal per-check interval may make this less frequent.
	if !c.last.IsZero() && now.Sub(c.last) < staleLibrariesInterval {
		return c.cached
	}
	var report process.LibraryReport
	var err error
	if c.sample != nil {
		report, err = c.sample(ctx)
	} else {
		report, err = process.ScanLibraries(ctx, nil)
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	res := c.libraryResult(report, err, now, run.start)
	c.last, c.cached = now, res
	return res
}

// libraryEvidence reduces findings to what stable_for must see unchanged: the
// replaced files, and the generation of the main process whose finding can
// authorize a restart. A forked worker inherits its parent's mappings and comes
// and goes with load, so neither its PID nor its presence says anything about an
// update; only a file no other process reports changes the evidence.
func libraryEvidence(libraries []process.StaleLibrary) map[process.StaleLibrary]struct{} {
	evidence := map[process.StaleLibrary]struct{}{}
	for _, lib := range libraries {
		evidence[process.StaleLibrary{Path: lib.Path, Mapped: lib.Mapped, Installed: lib.Installed,
			Modified: lib.Modified, Changed: lib.Changed, Missing: lib.Missing, ReplacementPath: lib.ReplacementPath}] = struct{}{}
		if lib.RestartEligible {
			evidence[lib] = struct{}{}
		}
	}
	return evidence
}

func (c *staleLibrariesCheck) libraryResult(report process.LibraryReport, err error, now, start time.Time) Result {
	if c.sample != nil && report.Processes == 0 && err == nil {
		err = errors.New("no running service processes available for library inspection")
	}
	message := fmt.Sprintf("no stale libraries in %d process(es)", report.Processes)
	paths := make([]string, 0, len(report.Libraries))
	eligible := false
	missing := false
	for _, lib := range report.Libraries {
		paths = append(paths, lib.Path)
		eligible = eligible || lib.RestartEligible
		missing = missing || lib.Missing
	}
	paths = strutil.SortedUnique(paths)
	if len(paths) > 0 {
		message = fmt.Sprintf("%d stale library mapping(s): %s", len(report.Libraries), strings.Join(paths, ", "))
	}
	evidence := libraryEvidence(report.Libraries)
	if err != nil || !maps.Equal(evidence, c.previous) || c.since.IsZero() {
		c.since = now
		c.previous = evidence
	}
	res := c.result(len(report.Libraries) == 0, message, start)
	res.ActionBlocked = c.sample == nil
	if len(report.Libraries) > 0 {
		reason := "restart the service to load the installed libraries"
		switch {
		case c.sample == nil:
			reason = "host inventory only; select the owning service"
		case !eligible:
			reason = "no uniquely identified main process affected; close affected sessions or review ownership"
		case missing:
			reason = "installed library missing; review package update before restarting"
		case now.Sub(c.since) < c.stableFor:
			reason = "waiting for unchanged library identities for " + c.stableFor.String()
		default:
			res.ActionBlocked = false
		}
		res.ActionBlocked = res.ActionBlocked || !eligible || missing || now.Sub(c.since) < c.stableFor
		res.Message += "; " + reason
	}
	if err != nil {
		// An incomplete inventory is evidence only of what it found. One that
		// found no stale mapping states no failure, so an alert stays unknown
		// instead of reading a worker that exited mid-scan as replaced
		// libraries; the result still blocks every action.
		res.Unavailable, res.ActionBlocked = true, true
		res.Message += "; incomplete inventory: " + err.Error()
	}
	res.Data = map[string]any{DataKeyType: CheckTypeStaleLibraries, DataKeyValue: float64(len(report.Libraries)),
		DataKeyPath: strings.Join(paths, ","), DataKeyPIDs: joinedPIDs(report.Libraries, func(lib process.StaleLibrary) int { return lib.PID }),
		DataKeyLibraries: report.Libraries, DataKeyActionBlocked: res.ActionBlocked,
		DataKeyDependencies: report.Dependencies, DataKeyComplete: err == nil && report.Processes > 0}
	return res
}
