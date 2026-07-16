package conn

import (
	"context"
	"errors"
	"testing"
)

func TestProbeWithDeadline(t *testing.T) {
	tests := []struct {
		name  string
		ctx   context.Context
		probe func(context.Context) (Result, error)
		want  string
		wantE error
	}{
		{
			name: "returns probe result",
			ctx:  context.Background(),
			probe: func(context.Context) (Result, error) {
				return Result{Version: "1.0"}, nil
			},
			want: "1.0",
		},
		{
			name: "returns probe error",
			ctx:  context.Background(),
			probe: func(context.Context) (Result, error) {
				return Result{}, errors.New("unavailable")
			},
			wantE: errors.New("unavailable"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := probeWithDeadline(test.ctx, test.probe)
			if got.Version != test.want {
				t.Errorf("version = %q, want %q", got.Version, test.want)
			}
			if test.wantE == nil {
				if err != nil {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err == nil || err.Error() != test.wantE.Error() {
				t.Errorf("error = %v, want %v", err, test.wantE)
			}
		})
	}
}

func TestProbeWithDeadlineReturnsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release := make(chan struct{})
	completed := make(chan struct{})

	result, err := probeWithDeadline(ctx, func(context.Context) (Result, error) {
		<-release
		close(completed)
		return Result{}, nil
	})
	if result.Version != "" || result.Extra != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	close(release)
	<-completed
}
