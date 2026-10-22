package app

import (
	"reflect"
	"testing"

	"sermo/internal/operation"
	"sermo/internal/web"
)

func TestWebActionResultFrom(t *testing.T) {
	tests := []struct {
		name string
		in   operation.Result
		want web.ActionResult
	}{
		{
			name: "successful message",
			in:   operation.Result{Status: operation.ResultOK, Message: "started"},
			want: web.ActionResult{OK: true, Message: "started", Status: string(operation.ResultOK)},
		},
		{
			name: "status fallback",
			in:   operation.Result{Status: operation.ResultFailed},
			want: web.ActionResult{OK: false, Message: string(operation.ResultFailed), Status: string(operation.ResultFailed)},
		},
		{
			// The client tells a refused action from a failed one by status;
			// the rest of the operation metadata stays server-side.
			name: "blocked status without operation metadata",
			in: operation.Result{
				Service: "web",
				Action:  "restart",
				Status:  operation.ResultBlocked,
				Message: "guard blocked",
			},
			want: web.ActionResult{OK: false, Message: "guard blocked", Status: string(operation.ResultBlocked)},
		},
		{
			name: "orphan processes",
			in:   operation.Result{Status: operation.ResultOrphanProcesses, Message: "1 residual process(es) remain after stop"},
			want: web.ActionResult{OK: false, Message: "1 residual process(es) remain after stop", Status: string(operation.ResultOrphanProcesses)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := webActionResultFrom(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("webActionResultFrom() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
