package app

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/web"
)

// dbQueryStaleIntervals is how many watch intervals a published statement list
// stays current; an older one renders as collecting rather than as live rows.
const dbQueryStaleIntervals = 3

// appendDBQueries lists the statements every db_queries watch last published.
// Like tmux/screen it only decodes the daemon's snapshots: an HTTP request
// never opens a database connection.
func (b *WebBackend) appendDBQueries(result *web.SessionInventory) {
	now := b.webNow()
	for _, name := range b.watchOrder {
		w := b.watches[name]
		if w == nil || w.disabled || w.checkType != checks.CheckTypeDBQueries {
			continue
		}
		service, watch := "", name
		if w.serviceScoped {
			service, watch, _ = strings.Cut(name, serviceWatchNameSeparator)
		}
		source := web.SessionSource{Kind: web.SessionKindDatabase, Service: service, Check: watch, State: web.SessionSourceCollecting}
		snaps := b.watchSnapshots.Get(name, checks.CheckTypeDBQueries)
		if len(snaps) == 0 || (w.interval > 0 && now.Sub(snaps[0].At) > dbQueryStaleIntervals*w.interval) {
			result.Sources = append(result.Sources, source)
			continue
		}
		snap := snaps[0]
		_, sampled := snap.Data[checks.DataKeyCount]
		if snap.Skipped || (snap.Unavailable && !sampled) {
			source.State, source.Message = web.SessionSourceUnavailable, snap.Message
			result.Sources = append(result.Sources, source)
			continue
		}
		source.State = web.SessionSourceAvailable
		complete, hasComplete := snap.Data[checks.DataKeyComplete].(bool)
		unknown, _ := cfgval.Int(snap.Data[checks.DataKeyUnknownCount])
		if snap.Unavailable || (hasComplete && !complete) || unknown > 0 {
			// Partial data and a known failing threshold can coexist. Preserve
			// the missing-data reason independently of the check's verdict.
			source.State, source.Message = web.SessionSourcePartial, snap.Message
		}
		result.Sources = append(result.Sources, source)
		listed := checks.DBQueriesFromData(snap.Data)
		for i := range listed {
			q := &listed[i]
			result.Database = append(result.Database, web.DBQuerySession{
				Service: service, Watch: watch, Engine: q.Engine, ID: q.ID, User: q.User, Host: q.Host,
				QueryID: q.QueryID, OSThreadID: q.OSThreadID, At: snap.At.UTC().Format(time.RFC3339),
				Database: q.Database, Command: q.Command, State: q.State, ElapsedSeconds: q.ElapsedSeconds,
				Query: q.Query, Truncated: q.Truncated, Identity: q.Identity, DisplayIdentity: q.DisplayIdentity,
				Long: q.Long, Matched: q.Matched, Alerted: q.Alerted, CanKill: w.serviceScoped && q.Killable(), Stopping: q.Stopping(),
				RSS: q.MemoryBytes, MemoryReady: q.MemoryReady, CPU: q.CPU, CPUThread: q.CPUThread, CPUReady: q.CPUReady,
				IORead: q.IORead, IOWrite: q.IOWrite, IOReady: q.IOReady,
			})
		}
	}
	slices.SortStableFunc(result.Database, func(a, b web.DBQuerySession) int {
		return cmp.Or(cmp.Compare(b.ElapsedSeconds, a.ElapsedSeconds),
			strings.Compare(a.Service, b.Service), strings.Compare(a.Watch, b.Watch))
	})
}

// KillDBQuery cancels one listed statement (or closes its connection) through
// the service's operation engine, which re-verifies it before acting.
func (b *WebBackend) KillDBQuery(ctx context.Context, name string, req web.DBQueryKillRequest) web.ActionResult {
	e := b.entries[name]
	if e == nil {
		return b.operateError(name, string(rules.ActionKillQuery), unknownServiceMessage+name)
	}
	if e.disabled {
		return b.operateError(name, string(rules.ActionKillQuery), serviceSubjectPrefix+name+" is disabled in configuration")
	}
	r := e.engine.KillDBQuery(ctx, operation.DBQueryTarget{
		Watch: req.Watch,
		ID:    req.ID, Identity: req.Identity, Mode: req.Mode,
	})
	return webActionResultFrom(r)
}
