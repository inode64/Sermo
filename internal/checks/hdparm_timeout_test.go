package checks

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
)

type slowHdparmRunner struct{}

func (slowHdparmRunner) Run(ctx context.Context, _ string, _ ...string) (execx.Result, error) {
	<-ctx.Done()
	return execx.Result{ExitCode: -1}, fmt.Errorf("run hdparm: %w", ctx.Err())
}

func TestHdparmCheckTimeoutMessage(t *testing.T) {
	check := hdparmCheck{
		base:   base{name: "hd", timeout: time.Millisecond},
		runner: slowHdparmRunner{},
		device: "/dev/sda",
		preds:  []levelPred{{field: "cached", op: "<", value: 100}},
	}
	res := check.Run(context.Background())
	if res.OK {
		t.Fatal("expected hdparm check timeout failure")
	}
	if !strings.Contains(res.Message, "timeout after 1ms") {
		t.Fatalf("message = %q, want timeout after duration", res.Message)
	}
}
