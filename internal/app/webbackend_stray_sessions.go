package app

import (
	"context"
	"slices"
	"strconv"

	"sermo/internal/checks"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/web"
)

// appendStraySessions uses the worker's published discovery, never a second
// host scan in the HTTP path. Even a missing terminal client cannot hide it.
func (b *WebBackend) appendStraySessions(inventory *web.SessionInventory, service string, entry *webEntry) {
	if entry.disabled {
		return
	}
	observation := b.observeService(service, entry)
	for sample := range observation.currentSnapshotsOfType(entry, checks.CheckTypeStrays) {
		for _, proc := range process.Strays(checks.DecodeDataSlice[process.Process](sample.Data[checks.DataKeyProcesses])) {
			reason := entry.engine.ReapBlockReason(proc)
			if proc.StartTicks == 0 {
				reason = "process generation unavailable"
			}
			user := proc.User
			if user == "" {
				user = strconv.FormatUint(uint64(proc.UID), 10)
			}
			inventory.Strays = append(inventory.Strays, web.StraySession{
				Service: service, PID: proc.PID, PPID: proc.PPID, StartTicks: proc.StartTicks,
				User: user, Exe: proc.Exe, ExePrevious: proc.ExePrev,
				Identity: process.SignalIdentity(proc), CanClose: reason == "", CloseReason: reason,
			})
		}
		break // multiple strays checks share the same service discovery
	}
}

// ReapProcess closes only the selected stray through the service operation
// engine. Neither request fields nor an old inventory grant kill authority.
func (b *WebBackend) ReapProcess(ctx context.Context, service string, target web.StraySession) web.ActionResult {
	entry := b.entries[service]
	if entry == nil {
		return b.operateError(service, reapActionLabel, unknownServiceMessage+service)
	}
	if entry.disabled {
		return b.operateError(service, reapActionLabel, "service is disabled in configuration")
	}
	return webActionResultFrom(entry.engine.ReapProcess(ctx, operation.ReapTarget{
		PID: target.PID, StartTicks: target.StartTicks, Identity: target.Identity,
	}))
}

// attachStrayTerminals links diagnostic rows to a unique live terminal session.
// It grants no reap authority and never changes a process row's action target.
// Closing a whole session is offered only on that session's own row.
func (b *WebBackend) attachStrayTerminals(inventory *web.SessionInventory, snapshot sessionProcessSnapshot) {
	owners := make(map[int][]int)
	for i, session := range inventory.Terminal {
		pids := snapshot.treePIDs(session.PIDs)
		pids = append(pids, b.terminalServerPIDs(session, snapshot)...)
		for _, pid := range pids {
			if !slices.Contains(owners[pid], i) {
				owners[pid] = append(owners[pid], i)
			}
		}
	}
	for i := range inventory.Strays {
		row := &inventory.Strays[i]
		sessions := owners[row.PID]
		if len(sessions) != 1 {
			continue
		}
		session := inventory.Terminal[sessions[0]]
		id, ok := snapshot.byPID[row.PID]
		if !ok || id.StartTicks != row.StartTicks || session.Service != row.Service {
			continue
		}
		row.Terminal = &session
	}
}

func (b *WebBackend) terminalServerPIDs(session web.TerminalSession, snapshot sessionProcessSnapshot) []int {
	entry := b.entries[session.Service]
	if entry == nil {
		return nil
	}
	var pids []int
	for _, source := range entry.terminalSessions {
		if source.check != session.Check {
			continue
		}
		for _, pid := range session.PIDs {
			pane, ok := snapshot.byPID[pid]
			if !ok {
				continue
			}
			parent, ok := snapshot.byPID[pane.PPID]
			if ok && parent.ExeOK && parent.Exe == source.binary && parent.UID == pane.UID {
				pids = append(pids, parent.PID)
			}
		}
	}
	return pids
}
