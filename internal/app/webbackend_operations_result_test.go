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
			want: web.ActionResult{OK: true, Message: "started"},
		},
		{
			name: "status fallback",
			in:   operation.Result{Status: operation.ResultFailed},
			want: web.ActionResult{OK: false, Message: string(operation.ResultFailed)},
		},
		{
			name: "operation metadata is not exposed",
			in: operation.Result{
				Service: "web",
				Action:  "restart",
				Status:  operation.ResultBlocked,
				Message: "guard blocked",
			},
			want: web.ActionResult{OK: false, Message: "guard blocked"},
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
