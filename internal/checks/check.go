// Package checks runs a service's monitoring/preflight checks.
// Each Run invocation is single-shot; checks that track
// change over time keep their state in the built check instance. The runner
// executes a set concurrently and returns one Result per check.
//
// Service checks/preflight support tcp, ports, http, command, service,
// file_exists, file, lockfile, binary, process, metric (via the daemon's stateful collector),
// libraries, count, and host-resource probes (storage, load, fds, conntrack,
// firewall_rules, zombies, oom, cert). The multi-target watch types
// (net, icmp, swap and file) are also usable in single-metric/single-shot form.
// See buildCheck and TypeInfo for the shared dispatch/validation vocabulary.
package checks

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"sermo/internal/conn"
)

// Reporting modes a check can declare with `reports:`. They describe what the
// result *means*, which is what decides availability, SLA and how the dashboard
// labels the check — not how the probe runs.
const (
	// ReportsHealth is the default: OK means the target is available, so the
	// check reads ok/fail and counts toward availability.
	ReportsHealth = "health"
	// ReportsState marks a state sensor: OK means the sensed state is present
	// (a backup is running), and neither side is good or bad. It has no verdict,
	// so it never counts toward health or SLA and reads active/inactive.
	ReportsState = "state"
	// ReportsCondition is alert-style: OK means the condition fired, so
	// availability is inverted. It is what most non-health types get by default;
	// declaring it makes that explicit, or applies it to a type whose default is
	// health.
	ReportsCondition = "condition"
	// ReportsValue marks a measurement: the check publishes a number and passes
	// no judgement on it. Like a state sensor it has no verdict and no SLA; the
	// dashboard shows the reading and rules compare it themselves.
	ReportsValue = "value"
)

// reportingModes is the immutable package-owned catalog of values `reports:`
// accepts. Consumers receive a copy through ReportingModes so they cannot alter
// validation or registry invariants at runtime.
var reportingModes = [...]string{ReportsHealth, ReportsState, ReportsCondition, ReportsValue}

// ReportingModes returns the values `reports:` accepts, in display order.
func ReportingModes() []string { return slices.Clone(reportingModes[:]) }

// conditionByDefault reports whether a check of this type is alert-style unless
// `reports:` says otherwise. The type only supplies the default: the same type
// can be a health assertion in one service and a sensor in another, so the
// check has the last word.
func conditionByDefault(typ string) bool {
	if info, ok := TypeInfoFor(typ); ok {
		return info.DefaultReports == ReportsCondition
	}
	return !IsHealthType(typ)
}

// ResolveCondition decides whether a check is alert-style: an explicit
// `reports:` wins, otherwise the check type's default applies.
func ResolveCondition(typ, reports string) bool {
	switch {
	case reports == ReportsCondition:
		return true
	case reports == ReportsHealth || VerdictlessMode(reports):
		return false
	default:
		return conditionByDefault(typ)
	}
}

// IsReportingMode reports whether s names a supported `reports:` value.
func IsReportingMode(s string) bool { return slices.Contains(reportingModes[:], s) }

// verdictlessModes are the reporting modes that assert nothing: they observe a
// state or a number without judging it, so they never count toward health and
// record no availability.
var verdictlessModes = []string{ReportsState, ReportsValue}

// VerdictlessMode reports whether a declared `reports:` value passes no
// judgement, so the check has no availability to report. The configured
// counterpart of Result.Verdictless, for callers that hold the declaration
// rather than a result.
func VerdictlessMode(reports string) bool { return slices.Contains(verdictlessModes, reports) }

// Result is the observable outcome of one check.
type Result struct {
	Service string `json:"service,omitempty"`
	Check   string `json:"check"`
	OK      bool   `json:"ok"`
	// Condition inverts availability: OK means the condition fired, not that
	// the target is well. Derived from the check type.
	Condition bool `json:"-"`
	// Reports is the declared reporting mode; empty means ReportsHealth.
	Reports string `json:"-"`
	// Severity is how grave a failure of this check is; empty means
	// SeverityError. It never changes the verdict — Observation() owns that — only
	// how loudly the verdict is reported, so a guard can never read a warning as
	// healthy.
	Severity string `json:"-"`
	Optional bool   `json:"optional,omitempty"`
	Skipped  bool   `json:"skipped,omitempty"` // gated off this cycle (requires/skip_when_changed)
	// Unavailable marks a failed observation that a guard must treat as unsafe
	// rather than as a false condition.
	Unavailable bool           `json:"-"`
	Message     string         `json:"message,omitempty"`
	Latency     time.Duration  `json:"latency_ns,omitempty"`
	Data        map[string]any `json:"data,omitempty"`
}

// ObservationState is the canonical interpretation of one check result. Raw OK
// remains part of Result because rules consume the comparison itself, but every
// health/event/UI consumer uses this state so condition polarity, unavailable
// samples, skipped gates and verdictless sensors cannot be reinterpreted at each
// layer.
type ObservationState string

const (
	// ObservationHealthy is an available sample whose health assertion passes.
	ObservationHealthy ObservationState = "healthy"
	// ObservationFailing is an available sample whose health assertion fails.
	ObservationFailing ObservationState = "failing"
	// ObservationUnavailable means the check could not produce a trustworthy sample.
	ObservationUnavailable ObservationState = "unavailable"
	// ObservationSkipped means a gate deliberately suppressed this cycle's check.
	ObservationSkipped ObservationState = "skipped"
	// ObservationNeutral is a verdictless state/value sample.
	ObservationNeutral ObservationState = "neutral"
)

// Valid reports whether state is one of the states Sermo can persist and
// publish. The empty state is deliberately invalid.
func (s ObservationState) Valid() bool {
	switch s {
	case ObservationHealthy, ObservationFailing, ObservationUnavailable, ObservationSkipped, ObservationNeutral:
		return true
	default:
		return false
	}
}

// Healthy reports whether state leaves target availability healthy. Skipped and
// neutral observations make no negative assertion, while unavailable fails
// closed just like an explicit failing observation.
func (s ObservationState) Healthy() bool {
	return s == ObservationHealthy || s == ObservationSkipped || s == ObservationNeutral
}

// AffectsHealth reports whether state is a real availability verdict. It keeps
// skipped and verdictless observations out of health edges and action windows.
func (s ObservationState) AffectsHealth() bool {
	return s == ObservationHealthy || s == ObservationFailing || s == ObservationUnavailable
}

// Observation interprets the raw check comparison once. Ordering matters:
// unavailable and skipped are transport/scheduler facts that override reporting
// polarity, and verdictless checks deliberately carry no health verdict.
func (r Result) Observation() ObservationState {
	switch {
	case r.Unavailable:
		return ObservationUnavailable
	case r.Skipped:
		return ObservationSkipped
	case r.Verdictless():
		return ObservationNeutral
	case r.Condition && r.OK:
		return ObservationFailing
	case r.Condition:
		return ObservationHealthy
	case r.OK:
		return ObservationHealthy
	default:
		return ObservationFailing
	}
}

// Warning reports whether this result is a failure its subject declared an
// advisory: a bad or unobservable sample worth showing and not worth waking
// anyone. Severity grades a failure — it never invents one and never cancels
// one, so a healthy, skipped or verdictless result is never a warning.
func (r Result) Warning() bool {
	return !r.Observation().Healthy() && IsWarning(r.Severity)
}

// CountsTowardHealth reports whether this result may move aggregate health or
// the SLA. It is the single gate those consumers use: Observation says whether
// there is a verdict at all, severity says whether that verdict is grave enough
// to hold against the target. Availability guards deliberately do not consult
// it — they keep reading Observation, so a warning can never read as healthy.
func (r Result) CountsTowardHealth() bool {
	return r.Observation().AffectsHealth() && !r.Warning()
}

// Verdictless reports whether this result passes no judgement, so it never
// counts toward health and records no availability: a state sensor and a
// measurement both observe without asserting. Rules still read OK directly, so
// a guard keyed on `active: {check: …}` is unaffected.
func (r Result) Verdictless() bool { return VerdictlessMode(r.Reports) }

// Healthy reports whether this result means the target is available. Most
// checks are health-style (OK means healthy); condition checks are alert-style
// (OK means the condition fired), so their availability is inverted. A state
// sensor asserts nothing about availability and is never unhealthy.
func (r Result) Healthy() bool {
	return r.Observation().Healthy()
}

// Check is a single-shot probe.
type Check interface {
	Name() string
	Run(ctx context.Context) Result
}

// resultMetadataProvider exposes the fields common to every built check without
// widening Check for test doubles and specialized callers outside this package.
type resultMetadataProvider interface {
	resultMetadata() Result
}

// Execute runs one check behind the package's panic barrier. All callers that
// execute a built check directly use this entry point so a malformed host
// response or checker defect becomes an unavailable observation instead of
// escaping into the daemon scheduler.
func Execute(ctx context.Context, check Check) (result Result) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = checkResultMetadata(check)
			result.Unavailable = true
			result.Message = fmt.Sprintf("check panicked: %v", recovered)
		}
	}()
	return check.Run(ctx)
}

func checkResultMetadata(check Check) Result {
	if check == nil {
		return Result{Check: "unknown"}
	}
	if provider, ok := check.(resultMetadataProvider); ok {
		if result, ok := safeResultMetadata(provider); ok {
			if result.Check == "" {
				result.Check = safeCheckName(check)
			}
			return result
		}
	}
	return Result{Check: safeCheckName(check)}
}

func safeResultMetadata(provider resultMetadataProvider) (result Result, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return provider.resultMetadata(), true
}

func safeCheckName(check Check) (name string) {
	name = "unknown"
	defer func() { _ = recover() }()
	if checkName := check.Name(); checkName != "" {
		name = checkName
	}
	return name
}

// IsHealthType reports whether OK==true means the check is healthy. Host watches
// invert these checks and fire on failure; condition-style checks fire on OK.
func IsHealthType(typ string) bool {
	if info, ok := TypeInfoFor(typ); ok {
		return info.DefaultReports == ReportsHealth
	}
	_, ok := conn.Lookup(typ)
	return ok
}

// Built pairs a check with whether its failure is optional (a warning) or
// required (blocks the action), per policy.
type Built struct {
	Check    Check
	Optional bool
}

// Run executes checks concurrently and returns their results in input order.
// maxParallel bounds concurrency; 0 means unbounded (the sermoctl one-shot
// path; the daemon's global semaphore is a separate concern).
func Run(ctx context.Context, built []Built, maxParallel int) []Result {
	results := make([]Result, len(built))
	if len(built) == 0 {
		return results
	}
	if maxParallel <= 0 {
		runUnbounded(ctx, built, results)
		return results
	}
	runBounded(ctx, built, results, min(maxParallel, len(built)))
	return results
}

func runUnbounded(ctx context.Context, built []Built, results []Result) {
	var wg sync.WaitGroup
	wg.Add(len(built))
	for i := range built {
		go func() {
			defer wg.Done()
			results[i] = executeBuilt(ctx, built[i])
		}()
	}
	wg.Wait()
}

func runBounded(ctx context.Context, built []Built, results []Result, workers int) {
	jobs := make(chan int)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for i := range jobs {
				if err := ctx.Err(); err != nil {
					results[i] = checkNotStartedResult(built[i], err)
					return
				}
				results[i] = executeBuilt(ctx, built[i])
			}
		}()
	}
	for i := range built {
		select {
		case jobs <- i:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			for pending := i; pending < len(built); pending++ {
				results[pending] = checkNotStartedResult(built[pending], ctx.Err())
			}
			return
		}
	}
	close(jobs)
	wg.Wait()
}

func checkNotStartedResult(built Built, err error) Result {
	result := checkResultMetadata(built.Check)
	result.Optional = built.Optional
	result.Unavailable = true
	result.Message = fmt.Sprintf("check not started: %v", err)
	return result
}

func executeBuilt(ctx context.Context, built Built) Result {
	res := Execute(ctx, built.Check)
	// A check may mark its own result optional (a warning, e.g. an output
	// pattern match graded `warning`); keep that, and the static flag also makes
	// a check optional.
	res.Optional = res.Optional || built.Optional
	return res
}

// base carries the fields every check shares and applies the per-check timeout.
type base struct {
	name      string
	service   string
	timeout   time.Duration
	condition bool
	reports   string
	severity  string
}

func (b base) Name() string { return b.name }

func (b base) resultMetadata() Result {
	return Result{
		Service:   b.service,
		Check:     b.name,
		Condition: b.condition,
		Reports:   b.reports,
		Severity:  b.severity,
	}
}

// withTimeout derives the check's deadline from the caller's context.
func (b base) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if b.timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, b.timeout)
}

// checkRun owns the common lifecycle of a potentially blocking check run.
// Local, non-blocking samplers deliberately skip it to avoid a context
// allocation in the daemon's hot path.
type checkRun struct {
	start  time.Time
	cancel context.CancelFunc
}

// begin starts latency accounting and derives the check's bounded context.
func (b base) begin(ctx context.Context) (context.Context, checkRun) {
	start := time.Now()
	ctx, cancel := b.withTimeout(ctx)
	return ctx, checkRun{start: start, cancel: cancel}
}

func (r checkRun) close() { r.cancel() }

func (b base) result(ok bool, message string, start time.Time) Result {
	return Result{
		Service:   b.service,
		Check:     b.name,
		OK:        ok,
		Condition: b.condition,
		Reports:   b.reports,
		Severity:  b.severity,
		Message:   message,
		Latency:   time.Since(start),
	}
}

// unavailableResult builds a failed observation that guards must not interpret
// as an inactive condition.
func (b base) unavailableResult(message string, start time.Time) Result {
	res := b.result(false, message, start)
	res.Unavailable = true
	return res
}

// levelCountResult builds the Result shared by the count-vs-max level checks
// (fds, pids, conntrack): the used_pct/free predicate fields — with free clamped
// so a count momentarily above the max can't underflow the unsigned subtraction
// — the "label cur/max unit (pct)" message, and the Data map. countField names
// the primary metric in values/Data ("allocated", "count"). The kernel maximum
// (each sample's Max, the DataKeyMax field) is the `limit` parameter: the
// lowercase local is `limit`, not `max`, only to avoid shadowing the Go `max`
// builtin — keep it that way. When it is 0 the maximum is unknown, so used_pct/
// free are omitted and a predicate on them cannot hold (the level check is an AND).
// samplerOr returns the check's injected sampler, falling back to the package
// default when no test seam was wired. Every Run that reads the host opens with
// this resolution; the three shapes here cover the samplers whose only inputs
// are the check's own configuration. Samplers with richer signatures (cert,
// firewall, icmp, size) resolve their default at the call site.
func samplerOr[T any](s, def func() (T, error)) func() (T, error) {
	if s == nil {
		return def
	}
	return s
}

// okSamplerOr is samplerOr for the samplers that report availability with a
// bool instead of an error (oom, zombies).
func okSamplerOr[T any](s, def func() (T, bool)) func() (T, bool) {
	if s == nil {
		return def
	}
	return s
}

// keyedSamplerOr is samplerOr for the samplers selected by a single name: a
// device, interface, pressure resource or address family.
func keyedSamplerOr[T any](s, def func(string) (T, error)) func(string) (T, error) {
	if s == nil {
		return def
	}
	return s
}

// runLevelCountCheck samples one count/max observation and evaluates the level
// predicates against it — the shared Run body of the count-vs-limit level
// checks (fds, pids, conntrack).
func runLevelCountCheck(b base, preds []levelPred, sample func() (count, limit uint64, err error), label, unit, countField string) Result {
	start := time.Now()
	count, limit, err := sample()
	if err != nil {
		return b.unavailableResult(label+": "+err.Error(), start)
	}
	return levelCountResult(b, preds, label, unit, countField, count, limit, start)
}

// runSampledLevelCount is runLevelCountCheck for the kernel-resource checks
// whose sampler returns a struct: read projects the sampled struct onto the
// count/limit pair the level predicates evaluate. It is the whole Run body of
// fds, pids and conntrack, which differ only in sampler, projection and labels.
func runSampledLevelCount[S any](b base, preds []levelPred, sample func() (S, error),
	read func(S) (count, limit uint64), label, unit, countField string,
) Result {
	return runLevelCountCheck(b, preds, func() (uint64, uint64, error) {
		s, err := sample()
		count, limit := read(s)
		return count, limit, err
	}, label, unit, countField)
}

// runThresholdCheck samples one gauge and compares it against the configured
// threshold — the shared Run body of the single-value level checks (zombies,
// oom). unavailableMsg is the failure message when the sampler reports no
// reading; message renders the sampled value for the result.
func runThresholdCheck(b base, op string, value float64, sample func() (uint64, bool), unavailableMsg string, message func(uint64) string, dataKey string) Result {
	start := time.Now()
	v, ok := sample()
	if !ok {
		return b.unavailableResult(unavailableMsg, start)
	}
	met := compareFloat(float64(v), op, value)
	res := b.result(met, message(v), start)
	res.Data = map[string]any{dataKey: v, DataKeyValue: v}
	return res
}

// levelCountFields writes one count-vs-limit dimension's utilisation and free
// headroom into values under the given field names, and reports the utilisation.
// It owns the two rules the count-vs-limit checks share: an unknown limit (0)
// omits both fields so a predicate on them cannot hold, and free is clamped so a
// count momentarily above the limit cannot underflow the unsigned subtraction.
// A multi-dimension check (inotify) calls it once per dimension.
// unlimitedCountMax is the kernel's "no limit" sentinel for a count ceiling:
// fs.file-max reads LONG_MAX on a host that lifts the cap entirely.
const unlimitedCountMax = uint64(math.MaxInt64)

// countLimitUsable reports whether a sampled ceiling can carry a percentage.
//
// Zero means the kernel reported no ceiling at all; LONG_MAX or above means it
// reported an unreachable one. Neither is a denominator. Against a ceiling
// nothing can reach every utilisation rounds to zero, so a gauge built on it
// could only ever say "plenty of headroom" and a threshold written against it
// could never hold — a watch that looks green because it cannot speak. Where
// there is no usable ceiling the count itself is the reading.
func countLimitUsable(limit uint64) bool {
	return limit > 0 && limit < unlimitedCountMax
}

func levelCountFields(values map[string]float64, pctField, freeField string, count, limit uint64) float64 {
	if !countLimitUsable(limit) {
		return 0
	}
	usedPct := float64(count) / float64(limit) * percentScale
	values[pctField] = usedPct
	values[freeField] = float64(limit - min(count, limit))
	return usedPct
}

func levelCountResult(b base, preds []levelPred, label, unit, countField string, count, limit uint64, start time.Time) Result {
	values := map[string]float64{countField: float64(count)}
	usedPct := levelCountFields(values, fieldUsedPct, fieldFree, count, limit)
	res := b.result(levelPredsHold(preds, values), levelCountMessage(label, unit, count, limit, usedPct), start)
	// Without a usable ceiling the percentage, the headroom and the ceiling
	// itself are absent rather than zero. Reporting 0% would read as an idle
	// resource, gauge it against a number nothing can reach, and record a series
	// of flat zeros. The count is what the check has to say, so it is what the
	// scalar carries too.
	res.Data = map[string]any{countField: count}
	fallback := float64(count)
	if countLimitUsable(limit) {
		res.Data[DataKeyMax] = limit
		res.Data[fieldUsedPct] = usedPct
		res.Data[fieldFree] = limit - min(count, limit)
		fallback = usedPct
	}
	res.Data[DataKeyValue] = firstPredValue(preds, values, fallback)
	return res
}

// levelCountMessage words one count-vs-limit sample, naming why a percentage is
// missing when it is: an unreported ceiling and an unreachable one are different
// facts about the host, and "0%" states neither.
func levelCountMessage(label, unit string, count, limit uint64, usedPct float64) string {
	switch {
	case limit == 0:
		return fmt.Sprintf("%s %d %s (limit unknown)", label, count, unit)
	case limit >= unlimitedCountMax:
		return fmt.Sprintf("%s %d %s (no kernel limit)", label, count, unit)
	default:
		return fmt.Sprintf("%s %d/%d %s (%.1f%%)", label, count, limit, unit, usedPct)
	}
}
