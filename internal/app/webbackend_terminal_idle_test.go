package app

import (
	"testing"
	"time"

	"sermo/internal/process"
	"sermo/internal/web"
)

func TestTerminalIdleUnavailableAndActivity(t *testing.T) {
	reads := 0
	accessedAt := cachedTerminalAccess(func(uint64) (time.Time, bool) { reads++; return time.Time{}, false })
	snapshot := sessionProcessSnapshot{byPID: map[int]process.Identity{1: {TTY: 7, TTYOK: true}, 2: {TTY: 7, TTYOK: true}}}
	row := web.TerminalSession{}
	attachTerminalSessionIdle(snapshot, &row, []int{1, 2}, time.Unix(100, 0), accessedAt)
	if row.HasIdle || reads != 1 {
		t.Fatalf("unavailable terminal: %+v, reads %d", row, reads)
	}
	row.ActivityUnix = 90
	attachTerminalSessionIdle(snapshot, &row, []int{1, 2}, time.Unix(100, 0), accessedAt)
	if !row.HasIdle || row.IdleSeconds != 10 || reads != 1 {
		t.Fatalf("multiplexer activity: %+v, reads %d", row, reads)
	}
}
