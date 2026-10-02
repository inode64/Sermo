package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/servicemgr"
	"sermo/internal/web"
)

func TestWebBackendEventRefreshesStatusAfterAutomaticRecovery(t *testing.T) {
	for _, tt := range []struct {
		name      string
		kind      string
		eventTime string
		wantFresh bool
	}{
		{name: "action", kind: eventKindAction, eventTime: "2026-10-02T11:00:05Z", wantFresh: true},
		{name: "recovery supersedes action", kind: eventKindRecovered, eventTime: "2026-10-02T11:00:05Z", wantFresh: true},
		{name: "old event", kind: eventKindRecovered, eventTime: "2026-10-02T10:59:59Z"},
		{name: "invalid time", kind: eventKindRecovered, eventTime: "invalid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC)
			current := servicemgr.StatusInactive
			calls := 0
			entry := &webEntry{
				interval: time.Minute, noResidentProcess: true,
				status: func(context.Context) (servicemgr.Status, error) {
					calls++
					return current, nil
				},
			}
			backend := &WebBackend{now: func() time.Time { return now }, snapshots: NewSnapshots()}
			if got := backend.view(context.Background(), "container", entry); got.Status != string(servicemgr.StatusInactive) {
				t.Fatalf("initial status = %q", got.Status)
			}
			current = servicemgr.StatusActive
			now = now.Add(10 * time.Second)
			event := &web.Event{Kind: tt.kind, Time: tt.eventTime}
			observation := backend.observeService("container", entry)
			for range 2 {
				got := backend.viewWithRuntime(context.Background(), "container", entry, observation, event, serviceLockView{})
				if (got.Status == string(servicemgr.StatusActive)) != tt.wantFresh {
					t.Fatalf("status = %q, want refreshed = %v", got.Status, tt.wantFresh)
				}
			}
			wantCalls := 1
			if tt.wantFresh {
				wantCalls++
			}
			if calls != wantCalls {
				t.Fatalf("backend queries = %d, want %d; repeated views must reuse the refreshed cache", calls, wantCalls)
			}
		})
	}
}
