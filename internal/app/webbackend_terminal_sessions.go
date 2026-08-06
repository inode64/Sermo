package app

import (
	"slices"
	"strings"

	"sermo/internal/checks"
	"sermo/internal/web"
)

func terminalSessionsSupported(entry *webEntry) bool {
	if entry == nil {
		return false
	}
	for _, checkType := range entry.checkTypes {
		if checkType == checks.CheckTypeTerminalSessions {
			return true
		}
	}
	return false
}

// terminalSessions reads daemon-published check data only. A dashboard request
// must never run tmux or screen itself because both clients are external,
// user-scoped commands with their own bounded monitor-cycle execution.
func (b *WebBackend) terminalSessions(entry *webEntry, snapshots map[string]CheckSnapshot) []web.TerminalSession {
	if entry == nil {
		return nil
	}
	result := make([]web.TerminalSession, 0)
	for _, name := range entry.checkNames {
		if entry.checkTypes[name] != checks.CheckTypeTerminalSessions {
			continue
		}
		snapshot, ok := snapshots[name]
		if !ok || !b.serviceCheckSnapshotCurrent(entry, name, snapshot) {
			continue
		}
		for _, session := range checks.TerminalSessionsFromData(snapshot.Data) {
			result = append(result, web.TerminalSession{
				Multiplexer: session.Multiplexer,
				Name:        session.Name,
				User:        session.User,
				State:       session.State,
				Windows:     session.Windows,
			})
		}
	}
	slices.SortFunc(result, func(a, b web.TerminalSession) int {
		if byMultiplexer := strings.Compare(a.Multiplexer, b.Multiplexer); byMultiplexer != 0 {
			return byMultiplexer
		}
		if byUser := strings.Compare(a.User, b.User); byUser != 0 {
			return byUser
		}
		return strings.Compare(a.Name, b.Name)
	})
	return result
}
