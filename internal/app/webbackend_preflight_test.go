package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/operation"
)

func TestWebBackendPreflight(t *testing.T) {
	engine := operation.Engine{
		Preflight: func(context.Context) checks.Outcome {
			return checks.Outcome{
				OK: false,
				Results: []checks.Result{
					{Check: "disk", OK: false, Message: "no space"},
					{Check: "warn", OK: false, Optional: true, Message: "stale"},
				},
			}
		},
	}
	b := &WebBackend{
		entries: map[string]*webEntry{
			"web": {engine: engine},
		},
		defaultTimeout: 10 * time.Second,
	}

	res, ok := b.Preflight(context.Background(), "web")
	if !ok {
		t.Fatal("preflight not found")
	}
	if res.OK || len(res.Checks) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if res.Checks[0].Name != "disk" || res.Checks[0].OK {
		t.Fatalf("disk check = %+v", res.Checks[0])
	}
}

func TestWebBackendPreflightUnknown(t *testing.T) {
	b := &WebBackend{entries: map[string]*webEntry{}}
	if _, ok := b.Preflight(context.Background(), "ghost"); ok {
		t.Fatal("unknown service should not be found")
	}
}

func TestWebBackendPreflightNoChecks(t *testing.T) {
	b := &WebBackend{
		entries:        map[string]*webEntry{"web": {engine: operation.Engine{}}},
		defaultTimeout: time.Second,
	}
	res, ok := b.Preflight(context.Background(), "web")
	if !ok || !res.OK || len(res.Checks) != 0 {
		t.Fatalf("empty preflight = %+v", res)
	}
}
