package checks

import (
	"context"
	"errors"
	"testing"
	"time"

	"sermo/internal/metrics"
)

func TestTCPConnectionsCheck(t *testing.T) {
	tests := []struct {
		name        string
		count       int
		err         error
		wantOK      bool
		unavailable bool
	}{
		{name: "threshold reached", count: 8, wantOK: true},
		{name: "below threshold", count: 7},
		{name: "procfs error", err: errors.New("permission denied"), unavailable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := tcpConnectionsCheck{
				name: "clients", timeout: time.Second,
				port:  21,
				preds: []levelPred{{field: DataKeyCount, op: ">=", value: 8}},
				count: func(int) (int, error) { return tt.count, tt.err },
			}
			res := check.Run(context.Background())
			if res.OK != tt.wantOK {
				t.Fatalf("OK = %v, want %v (%s)", res.OK, tt.wantOK, res.Message)
			}
			if res.Unavailable != tt.unavailable {
				t.Fatalf("Unavailable = %v, want %v", res.Unavailable, tt.unavailable)
			}
			if tt.err == nil {
				if got := res.Data[DataKeyCount]; got != tt.count {
					t.Fatalf("count = %#v, want %d", got, tt.count)
				}
				if got := res.Data[DataKeyUnit]; got != metrics.MetricUnitConnections {
					t.Fatalf("unit = %#v, want %q", got, metrics.MetricUnitConnections)
				}
			}
		})
	}
}

func TestBuildTCPConnectionsCheck(t *testing.T) {
	built, warnings := Build(map[string]any{
		"clients": map[string]any{"type": CheckTypeTCPConnections, "port": "21", "count": map[string]any{"op": ">", "value": 5}},
	}, Deps{DefaultTimeout: time.Second})
	if len(warnings) != 0 || len(built) != 1 {
		t.Fatalf("built=%d warnings=%v, want one check", len(built), warnings)
	}
	if !built[0].Check.(tcpConnectionsCheck).condition {
		t.Fatal("tcp_connections must default to condition reporting")
	}
}
