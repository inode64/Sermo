package app

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/severity"
	"sermo/internal/state"
	"sermo/internal/units"
)

// Hook environment of a db_queries fire.
const (
	sermoEnvDBQueryID      = sermoEnvPrefix + "DB_QUERY_ID"
	sermoEnvDBUser         = sermoEnvPrefix + "DB_USER"
	sermoEnvDBName         = sermoEnvPrefix + "DB_NAME"
	sermoEnvDBHost         = sermoEnvPrefix + "DB_HOST"
	sermoEnvDBEngine       = sermoEnvPrefix + "DB_ENGINE"
	sermoEnvElapsedSeconds = sermoEnvPrefix + "ELAPSED_SECONDS"
	sermoEnvQuery          = sermoEnvPrefix + "QUERY"
)

const (
	// dbQueryEventCheckPrefix keys each statement's own event_notify incident.
	dbQueryEventCheckPrefix = "query:"
	dbQueryPolicyStateSlot  = "db-queries-policy"
	dbQueryChangeLong       = "long"
	dbQueryChangeEnded      = "ended"
)

// dbQueryKillFunc stops one listed statement (checks.KillDBQuery).
type dbQueryKillFunc func(context.Context, checks.DBQueryConfig, checks.DBQueryKill) (checks.DBQuery, error)

// dbQueryState is one tracked statement.
type dbQueryState struct {
	query     checks.DBQuery
	killTried bool // the automatic kill already acted (or reported) on it
	killHeld  bool // the policy's hold-back was already reported
	// prevTicks/prevRead/prevWrite at prevAt are the statement thread's
	// counters at the previous sample, the baseline of its CPU and IO rates.
	// serverThread is whether the statement's thread belongs to the database
	// server (1), not (-1) or not yet checked (0): read once per statement.
	serverThread int8
	prevAt       time.Time
	prevTicks    uint64
	hadCPU       bool
	prevRead     uint64
	prevWrite    uint64
	hadIO        bool
}

// dbQueryProcfs reads one statement's thread (MySQL/MariaDB) or backend
// process (PostgreSQL) counters. /proc/<tid>/stat and io answer for the whole
// thread group, so a thread is read under /proc/<tgid>/task/<tid>.
type dbQueryProcfs interface {
	ThreadCPU(pid, tid int) (uint64, bool)
	ThreadIO(pid, tid int) (read, write uint64, ok bool)
	ProcessRSS(pid int) (uint64, bool)
	// ThreadExe is the executable of the process a thread belongs to.
	ThreadExe(tid int) (string, bool)
}

// osDBQueryProcfs reads the host /proc.
type osDBQueryProcfs struct{ metrics.OSReader }

func (osDBQueryProcfs) ThreadExe(tid int) (string, bool) {
	id, ok := process.OSReader{}.Identity(tid)
	return id.Exe, ok && id.ExeOK
}

// dbServerExes are the executable names of each engine's server process.
var dbServerExes = map[string][]string{
	checks.SQLEngineMySQL:    {"mysqld", "mariadbd"},
	checks.SQLEngineMariaDB:  {"mysqld", "mariadbd"},
	checks.SQLEnginePostgres: {"postgres", "postmaster"},
}

// isDBServerExe reports whether exe is a server executable of engine; a
// deleted or versioned binary ("mysqld-8.0", "postgres (deleted)") counts.
func isDBServerExe(engine, exe string) bool {
	base := filepath.Base(exe)
	for _, name := range dbServerExes[engine] {
		if strings.HasPrefix(base, name) {
			return true
		}
	}
	return false
}

// dbQueryWatcher tracks the statements a database server runs. It fires once
// per statement that outlives min_duration — its own event_notify incident —
// and recovers it when the statement ends. A service watch may also stop
// statements through the service's operation engine (then.kill_query).
type dbQueryWatcher struct {
	name      string
	severity  severity.Level
	cfg       checks.DBQueryConfig
	check     map[string]any
	hook      HookSpec
	notifiers []notify.Notifier
	dryRun    bool
	inPanic   func() bool
	runner    HookRunner
	now       func() time.Time
	emit      func(Event)
	sample    func(context.Context, checks.DBQueryConfig) ([]checks.DBQuery, error)
	publish   func(string, string, checks.Result)
	restore   func() []checks.DBQuery
	// status, set for a service watch, skips cycles while the service is not
	// running: a stopped server has no statements to report.
	status func(context.Context) (servicemgr.Status, error)
	// procfs reads a local statement's counters.
	procfs     dbQueryProcfs
	kill       *checks.DBQueryKillSpec
	killer     func(context.Context, operation.DBQueryTarget) operation.Result
	policy     rules.Policy
	stateStore WatchStateStore

	// state is nil until loadState restored the previous daemon's statements.
	state        map[string]*dbQueryState
	unavailable  bool
	policyState  rules.RemediationState
	policyLoaded bool
}

// buildDBQueriesWatch builds a db_queries watch. kill is only reachable from a
// service watch: the kill runs through the service's operation engine.
func buildDBQueriesWatch(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	cfg, err := checks.ParseDBQueryConfig(checkEntry)
	if err != nil {
		return nil, watchSubjectPrefix + name + ": " + err.Error()
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = deps.DefaultTimeout
	}
	kill, err := parseKillQuery(entry, cfg)
	if err != nil {
		return nil, watchSubjectPrefix + name + ": " + err.Error()
	}
	if kill != nil && deps.DBQueryEngine == nil {
		return nil, watchSubjectPrefix + name + ": then.kill_query requires a service watch: the kill runs through the service operation engine"
	}
	actions, err := resolveWatchActions(entry, deps, watchActionOptions{
		checkType:      checks.CheckTypeDBQueries,
		allowKillQuery: true,
		emptyMessage:   "then requires a hook, notify or kill_query, or omit then for dashboard/event-log alerts",
	})
	if err != nil {
		return nil, watchSubjectPrefix + name + ": " + err.Error()
	}
	policy := rules.ParsePolicy(entry)
	if kill != nil && policy.Cooldown <= 0 {
		// Belt and braces with the validator: a zero policy would allow a kill
		// every cycle.
		return nil, watchSubjectPrefix + name + ": then.kill_query requires a policy with a positive cooldown"
	}
	snapshots := deps.WatchSnapshots
	w := &dbQueryWatcher{
		name:       name,
		severity:   watchSeverity(entry, checkEntry),
		cfg:        cfg,
		check:      checkEntry,
		hook:       actions.hook,
		notifiers:  resolveNotifiers(actions.effectiveNames, deps.Notifiers),
		dryRun:     config.DryRun(entry),
		inPanic:    deps.Panic.Active,
		runner:     OSHookRunner{Runner: deps.ExecxRunner},
		now:        deps.Now,
		emit:       deps.Emit,
		sample:     checks.SampleDBQueries,
		publish:    publishWatchSnapshots(snapshots, deps.watchConfigID),
		restore:    func() []checks.DBQuery { return restoreDBQueries(snapshots, name) },
		procfs:     osDBQueryProcfs{},
		kill:       kill,
		killer:     deps.DBQueryEngine,
		policy:     policy,
		stateStore: deps.WatchState,
	}
	if deps.WatchCheckDeps != nil {
		w.status = deps.WatchCheckDeps.Status
	}
	return newStatefulWatch(name, checks.CheckTypeDBQueries, entry, deps, interval, w.runCycle), ""
}

// parseKillQuery reads then.kill_query through the shared parser the config
// validator also uses; the builder refuses what validation reports.
func parseKillQuery(entry map[string]any, cfg checks.DBQueryConfig) (*checks.DBQueryKillSpec, error) {
	then, err := thenMap(entry)
	if err != nil || then == nil {
		return nil, err
	}
	raw, present := then[config.WatchThenKeyKillQuery]
	if !present {
		return nil, nil //nolint:nilnil // absent optional kill_query has no parse error
	}
	spec, err := checks.ParseDBQueryKill(raw, cfg.MinDuration)
	if err != nil {
		return nil, fmt.Errorf("then.%s: %w", config.WatchThenKeyKillQuery, err)
	}
	return &spec, nil
}

// restoreDBQueries reads the statements the watch's last persisted snapshot
// listed, so a restarted daemon resumes its open incidents.
func restoreDBQueries(snapshots *WatchSnapshots, name string) []checks.DBQuery {
	snaps := snapshots.Get(name, checks.CheckTypeDBQueries)
	out := make([]checks.DBQuery, 0, len(snaps))
	for _, snap := range snaps {
		out = append(out, checks.DBQueriesFromData(snap.Data)...)
	}
	return out
}

func (w *dbQueryWatcher) runCycle(ctx context.Context) {
	w.loadState()
	if w.status != nil {
		if st, err := w.status(ctx); err == nil && st != servicemgr.StatusActive {
			w.publishSkipped(string(st))
			return
		}
	}
	// One defaults_file read per cycle serves both the sample and Local.
	cfg, err := w.cfg.Resolved()
	queries := []checks.DBQuery{}
	if err == nil {
		sampleCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		queries, err = w.sample(sampleCtx, cfg)
		cancel()
	}
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		w.publishUnavailable(err)
		w.reportAvailability(ctx, err)
		return
	}
	w.reportAvailability(ctx, nil)
	slices.SortFunc(queries, func(a, b checks.DBQuery) int { return cmp.Compare(b.ElapsedSeconds, a.ElapsedSeconds) })

	observe := observeOnlyCycle(ctx)
	now := clockOrNow(w.now)()
	local := cfg.Local()
	seen := make(map[string]bool, len(queries))
	for i := range queries {
		key := queries[i].ItemKey()
		seen[key] = true
		st := w.state[key]
		if st != nil && !st.query.SameStatement(queries[i]) {
			// Another statement took the connection under the same key.
			w.recover(ctx, key, st)
			st = nil
		}
		if st == nil {
			st = &dbQueryState{}
			w.state[key] = st
		}
		if local {
			w.measure(st, &queries[i], now)
		}
		// The tracked statement carries whether it was already alerted.
		queries[i].Alerted, queries[i].Announced = st.query.Alerted, st.query.Announced
		queries[i].Long = w.long(queries[i])
		if !queries[i].Alerted && queries[i].Long && !observe {
			queries[i].Alerted = true
			queries[i].Announced = w.fire(ctx, key, queries[i])
		}
		st.query = queries[i]
	}
	if !observe {
		for key, st := range w.state {
			if !seen[key] {
				w.recover(ctx, key, st)
				delete(w.state, key)
			}
		}
		w.autoKill(ctx, now)
	}
	w.publishQueries(queries)
}

// measure fills a local statement's CPU share and IO rates since the previous
// sample from its own thread (MySQL/MariaDB) or backend process (PostgreSQL),
// and a PostgreSQL backend's resident memory. The caller measures only a local
// server: a remote server's thread ids name nothing on this host.
func (w *dbQueryWatcher) measure(st *dbQueryState, q *checks.DBQuery, now time.Time) {
	tid := int(q.OSThreadID)
	if tid <= 0 {
		return
	}
	// /proc/<tid>/task/<tid> is the thread itself, whatever its process;
	// /proc/<tid>/stat would be its whole thread group. A PostgreSQL backend
	// is a single-threaded process, so the same path serves it.
	// A loopback port or a mounted socket can reach a server in another PID
	// namespace (a container), whose thread ids name unrelated host
	// processes: measure only a thread of the database server itself.
	if st.serverThread == 0 {
		st.serverThread = -1
		if exe, ok := w.procfs.ThreadExe(tid); ok && isDBServerExe(q.Engine, exe) {
			st.serverThread = 1
		}
	}
	if st.serverThread < 0 {
		return
	}
	ticks, cpuOK := w.procfs.ThreadCPU(tid, tid)
	if cpuOK && st.hadCPU {
		q.CPU, q.CPUReady = cpuPercent(st.prevTicks, ticks, st.prevAt, now)
	}
	read, write, ioOK := w.procfs.ThreadIO(tid, tid)
	r, rOK := ioBytesPerSec(st.prevRead, read, st.hadIO, ioOK, st.prevAt, now)
	wr, wOK := ioBytesPerSec(st.prevWrite, write, st.hadIO, ioOK, st.prevAt, now)
	q.IORead, q.IOWrite, q.IOReady = r, wr, rOK && wOK
	st.prevAt = now
	st.prevTicks, st.hadCPU = ticks, cpuOK
	st.prevRead, st.prevWrite, st.hadIO = read, write, ioOK
	if q.Engine == checks.SQLEnginePostgres {
		if rss, ok := w.procfs.ProcessRSS(tid); ok {
			q.MemoryBytes, q.MemoryReady = uintToInt64(rss), true
		}
	}
}

// long reports whether a statement counts against min_duration and the
// check's users/databases filters.
func (w *dbQueryWatcher) long(q checks.DBQuery) bool {
	return q.Elapsed() >= w.cfg.MinDuration && w.cfg.Selector.Matches(q)
}

// loadState restores the statements the previous daemon already alerted, so a
// statement still running is not re-announced and one that ended meanwhile is
// recovered on the first live cycle.
func (w *dbQueryWatcher) loadState() {
	if w.state != nil {
		return
	}
	w.state = map[string]*dbQueryState{}
	if w.restore == nil {
		return
	}
	restored := w.restore()
	for i := range restored {
		if restored[i].Alerted {
			w.state[restored[i].ItemKey()] = &dbQueryState{query: restored[i]}
		}
	}
}

func (w *dbQueryWatcher) fire(ctx context.Context, key string, q checks.DBQuery) bool {
	message := w.summaryMessage(q, dbQueryFireMessage(q))
	check := dbQueryEventCheck(key)
	w.emitEvent(Event{Watch: w.name, Kind: eventKindFiring, Severity: w.severity, Check: check, Message: message})
	return w.dispatch(ctx, check, message, w.env(q, dbQueryChangeLong, message))
}

func (w *dbQueryWatcher) recover(ctx context.Context, key string, st *dbQueryState) {
	if !st.query.Alerted {
		return
	}
	message := dbQueryRecoverMessage(st.query)
	check := dbQueryEventCheck(key)
	w.emitEvent(Event{Watch: w.name, Kind: eventKindRecovered, Severity: w.severity, Check: check, Message: message})
	if st.query.Announced {
		env := w.env(st.query, dbQueryChangeEnded, message)
		env[sermoEnvEvent] = eventKindRecovered
		w.dispatch(ctx, check, recoveredMessagePrefix+message, env)
	}
}

// dispatch runs the watch's hook and notifiers for one statement, reporting
// whether they acted live (not dry-run, not panic) — so the recovery reaches
// the same hook or notifiers. A watch without either relies on event_notify,
// fed by the firing/recovered events.
func (w *dbQueryWatcher) dispatch(ctx context.Context, check, message string, env map[string]string) bool {
	if len(w.hook.Command) == 0 && len(w.notifiers) == 0 {
		return false
	}
	live := !w.dryRun && (w.inPanic == nil || !w.inPanic())
	dispatchWatchFire(ctx, watchFireSpec{
		name:        w.name,
		hook:        w.hook,
		runner:      w.runner,
		notifiers:   w.notifiers,
		inPanic:     w.inPanic,
		dryRun:      w.dryRun,
		emit:        func(e Event) { e.Check = check; w.emitEvent(e) },
		dryRunLabel: watchDryRunMessage(w.hook, w.notifiers),
		panicLabel:  "panic mode: hook/notify suppressed",
		severity:    w.severity,
	}, message, env)
	return live
}

// dbQueryEventCheck is a statement's own event_notify incident identity.
func dbQueryEventCheck(key string) string { return dbQueryEventCheckPrefix + key }

func (w *dbQueryWatcher) env(q checks.DBQuery, change, message string) map[string]string {
	return map[string]string{
		sermoEnvWatch:          w.name,
		sermoEnvCheckType:      checks.CheckTypeDBQueries,
		sermoEnvMessage:        message,
		sermoEnvChange:         change,
		sermoEnvDBEngine:       q.Engine,
		sermoEnvDBQueryID:      strconv.FormatInt(q.ID, envFormatBase),
		sermoEnvDBUser:         q.User,
		sermoEnvDBName:         q.Database,
		sermoEnvDBHost:         q.Host,
		sermoEnvElapsedSeconds: envAgeSeconds(q.Elapsed()),
		sermoEnvQuery:          q.Query,
	}
}

func (w *dbQueryWatcher) summaryMessage(q checks.DBQuery, message string) string {
	summary := cfgval.String(w.check[checks.CheckKeySummary])
	if summary == "" {
		return message
	}
	// The typed duration renders humanized, like a process watch's age.
	data := map[string]any{
		checks.DataKeyValue: q.Elapsed(), checks.DataKeyElapsed: q.Elapsed(),
		checks.DataKeyID: q.ID, checks.DataKeyUser: q.User, checks.DataKeyDatabase: q.Database,
		checks.DataKeyHost: q.Host, checks.DataKeyQuery: q.Query,
	}
	return checks.ApplySummary(summary, w.check, checks.Result{Check: w.name, Message: message, Data: data}).Message
}

func dbQueryFireMessage(q checks.DBQuery) string {
	text := q.Query
	if q.Truncated {
		text += " …"
	}
	return fmt.Sprintf("%s running %s (%s): %s", dbQuerySubject(q), units.HumanizeDuration(q.Elapsed()), dbQueryOrigin(q), text)
}

func dbQueryRecoverMessage(q checks.DBQuery) string {
	return fmt.Sprintf("%s (%s) is no longer running (last seen at %s)", dbQuerySubject(q), dbQueryOrigin(q), units.HumanizeDuration(q.Elapsed()))
}

func dbQuerySubject(q checks.DBQuery) string {
	return fmt.Sprintf("%s query %d", q.Engine, q.ID)
}

func dbQueryOrigin(q checks.DBQuery) string {
	origin := "user " + q.User
	if q.Database != "" {
		origin += ", db " + q.Database
	}
	if q.Host != "" {
		origin += ", host " + q.Host
	}
	if q.Command != "" || q.State != "" {
		origin += ", " + q.Command
		if q.State != "" {
			origin += "/" + q.State
		}
	}
	return origin
}

// reportAvailability reports the first failed sample and the first good one
// after it, each once, as the watch's availability incident.
func (w *dbQueryWatcher) reportAvailability(ctx context.Context, err error) {
	if observeOnlyCycle(ctx) {
		return
	}
	switch {
	case err != nil && !w.unavailable:
		w.unavailable = true
		w.emitEvent(Event{Watch: w.name, Kind: eventKindError, Severity: w.severity, Check: watchAvailabilityCheck, Message: checkUnavailablePrefix + err.Error()})
	case err == nil && w.unavailable:
		w.unavailable = false
		w.emitEvent(Event{Watch: w.name, Kind: eventKindRecovered, Check: watchAvailabilityCheck, Message: "check available: " + w.cfg.Engine})
	}
}

func (w *dbQueryWatcher) publishQueries(queries []checks.DBQuery) {
	if w.publish == nil {
		return
	}
	longCount, oldest := 0, int64(0)
	if len(queries) > 0 {
		oldest = queries[0].ElapsedSeconds // sorted longest first
	}
	for i := range queries {
		if queries[i].Long {
			longCount++
		}
	}
	listed := queries
	if len(listed) > w.cfg.MaxRows {
		// The rest are dropped from the list, except the alerted ones: a
		// restarted daemon restores its open incidents from this snapshot.
		listed = slices.Clip(queries[:w.cfg.MaxRows])
		for i := w.cfg.MaxRows; i < len(queries); i++ {
			if queries[i].Alerted {
				listed = append(listed, queries[i])
			}
		}
	}
	message := fmt.Sprintf("%s: %d running statement%s, %d over %s", w.cfg.Engine, len(queries),
		pluralSuffix(len(queries), "statement"), longCount, units.HumanizeDuration(w.cfg.MinDuration))
	result := checks.Result{
		Check:   w.name,
		OK:      longCount == 0,
		Message: message,
		Data: map[string]any{
			checks.DataKeyEngine:        w.cfg.Engine,
			checks.DataKeyCount:         len(queries),
			checks.DataKeyLongCount:     longCount,
			checks.DataKeyOldestSeconds: oldest,
			checks.DataKeyDBQueries:     listed,
		},
	}
	if longCount > 0 {
		result.Severity = w.severity
	}
	w.publish(w.name, checks.CheckTypeDBQueries, result)
}

func (w *dbQueryWatcher) publishUnavailable(err error) {
	if w.publish == nil {
		return
	}
	w.publish(w.name, checks.CheckTypeDBQueries, checks.Result{
		Check: w.name, Unavailable: true, Message: w.cfg.Engine + ": " + err.Error(),
		Data: w.heldData(),
	})
}

func (w *dbQueryWatcher) publishSkipped(status string) {
	if w.publish == nil {
		return
	}
	w.publish(w.name, checks.CheckTypeDBQueries, checks.Result{
		Check: w.name, OK: true, Skipped: true, Message: w.cfg.Engine + ": service " + status,
		Data: w.heldData(),
	})
}

// heldData is a sampleless snapshot's data: no listing, but the statements
// still alerted, so a daemon restarted meanwhile keeps their open incidents.
func (w *dbQueryWatcher) heldData() map[string]any {
	data := map[string]any{checks.DataKeyEngine: w.cfg.Engine}
	var held []checks.DBQuery
	for _, st := range w.state {
		if st.query.Alerted {
			held = append(held, st.query)
		}
	}
	if len(held) > 0 {
		data[checks.DataKeyDBQueries] = held
	}
	return data
}

// autoKill stops at most one eligible statement per cycle through the
// service's operation engine (locks, guards, re-verification, one audit
// event), paced by the watch's own policy — never the service's restart
// budget. Each statement is acted on, or reported as held back, once.
func (w *dbQueryWatcher) autoKill(ctx context.Context, now time.Time) {
	if w.kill == nil {
		return
	}
	var target *dbQueryState
	var key string
	for k, st := range w.state {
		q := &st.query
		// Long already holds min_duration and the check's own filters: the
		// watch never stops a statement it was told to ignore.
		if st.killTried || !q.Long || !q.Killable() || q.Elapsed() < w.kill.After || !w.kill.Selector.Matches(*q) {
			continue
		}
		if target == nil || q.ElapsedSeconds > target.query.ElapsedSeconds || (q.ElapsedSeconds == target.query.ElapsedSeconds && k < key) {
			target, key = st, k
		}
	}
	if target == nil {
		return
	}
	q := &target.query
	check := dbQueryEventCheck(key)
	label := fmt.Sprintf("%s (%s) %s", rules.ActionKillQuery, w.kill.Mode, dbQuerySubject(*q))
	if w.dryRun {
		target.killTried = true
		w.emitEvent(Event{Watch: w.name, Kind: eventKindDryRun, Check: check, Action: string(rules.ActionKillQuery), Message: "would " + label})
		return
	}
	if w.inPanic != nil && w.inPanic() {
		// Retried once panic mode clears; reported once.
		if !target.killHeld {
			target.killHeld = true
			w.emitEvent(Event{Watch: w.name, Kind: eventKindPanicSuppressed, Check: check, Message: "panic mode: " + label + " suppressed"})
		}
		return
	}
	w.loadPolicyState()
	if allowed, reason := w.policy.Allow(&w.policyState, now); !allowed {
		// Retried once the policy allows it; the hold-back is reported once.
		if reason != "" && !target.killHeld {
			target.killHeld = true
			w.emitEvent(Event{Watch: w.name, Kind: eventKindSuppressed, Check: check, Message: label + " held back: " + reason})
		}
		return
	}
	target.killTried = true
	result := w.killer(ctx, operation.DBQueryTarget{
		Watch: watchLocalName(w.name),
		ID:    q.ID, Identity: q.Identity, Mode: w.kill.Mode,
		Require: &checks.DBQueryKillRequirement{After: w.kill.After, Selector: w.kill.Selector, Filter: w.cfg.Selector},
	})
	// Only a kill that stopped something spends the budget: a statement that
	// ended before the re-verification was never killed.
	if result.Status == operation.ResultOK {
		w.policyState.Record(now, w.policy)
		w.persistPolicyState()
	}
	if result.Status == operation.ResultOK {
		w.emitEvent(Event{Watch: w.name, Kind: eventKindKill, Check: check, Severity: w.severity, Message: result.Message})
		return
	}
	w.emitEvent(Event{Watch: w.name, Kind: eventKindKillFailed, Check: check, Message: label + ": " + result.Message})
}

func (w *dbQueryWatcher) loadPolicyState() {
	if w.policyLoaded || w.stateStore == nil {
		return
	}
	w.policyLoaded = true
	rec, found, err := w.stateStore.WatchRuntimeState(w.name, dbQueryPolicyStateSlot)
	if err != nil {
		w.emitEvent(Event{Watch: w.name, Kind: eventKindError, Message: "load kill_query policy state: " + err.Error()})
		return
	}
	if found {
		w.policyState = *remediationFromRecord(rec.Policy)
	}
}

func (w *dbQueryWatcher) persistPolicyState() {
	if w.stateStore == nil {
		return
	}
	rec := state.WatchRuntimeRecord{Policy: remediationToRecord(&w.policyState)}
	if err := w.stateStore.SetWatchRuntimeState(w.name, dbQueryPolicyStateSlot, rec); err != nil {
		w.emitEvent(Event{Watch: w.name, Kind: eventKindError, Message: "persist kill_query policy state: " + err.Error()})
	}
}

// watchLocalName is a service watch's own name ("<service>:<watch>" → "<watch>").
func watchLocalName(name string) string {
	if _, local, ok := strings.Cut(name, serviceWatchNameSeparator); ok {
		return local
	}
	return name
}

func (w *dbQueryWatcher) emitEvent(e Event) { emitSafe(w.emit, e) }

// dbQueryKiller is the service engine's kill_query capability: it resolves the
// named db_queries watch from the service's own configuration (connection and
// credentials never come from the request) and stops the statement after
// re-verifying it. Nil when the service declares no db_queries watch.
func dbQueryKiller(tree map[string]any, kill dbQueryKillFunc) func(context.Context, operation.DBQueryTarget) (string, error) {
	configs := dbQueryConfigs(tree)
	if len(configs) == 0 {
		return nil
	}
	return func(ctx context.Context, target operation.DBQueryTarget) (string, error) {
		cfg, ok := configs[target.Watch]
		if !ok {
			return "", fmt.Errorf("no db_queries watch %q", target.Watch)
		}
		stopped, err := kill(ctx, cfg, target.DBQueryKill)
		if err != nil {
			return "", err
		}
		verb := "cancelled"
		if target.Mode == checks.DBQueryKillModeConnection {
			verb = "closed the connection of"
		}
		return fmt.Sprintf("%s %s after %s (%s): %s", verb, dbQuerySubject(stopped),
			units.HumanizeDuration(stopped.Elapsed()), dbQueryOrigin(stopped), stopped.Query), nil
	}
}

// dbQueryConfigs parses the service's enabled db_queries watches by name.
func dbQueryConfigs(tree map[string]any) map[string]checks.DBQueryConfig {
	section, _ := tree[config.SectionWatches].(map[string]any)
	out := map[string]checks.DBQueryConfig{}
	for name, raw := range section {
		entry, _ := raw.(map[string]any)
		if entry == nil || cfgval.Disabled(entry) {
			continue
		}
		checkEntry, _ := entry[config.WatchKeyCheck].(map[string]any)
		if cfgval.AsString(checkEntry[checks.CheckKeyType]) != checks.CheckTypeDBQueries {
			continue
		}
		if cfg, err := checks.ParseDBQueryConfig(checkEntry); err == nil {
			out[name] = cfg
		}
	}
	return out
}
