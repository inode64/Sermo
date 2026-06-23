package checks

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
)

type slowSmartRunner struct{}

func (slowSmartRunner) Run(ctx context.Context, _ string, _ ...string) (execx.Result, error) {
	<-ctx.Done()
	return execx.Result{ExitCode: -1}, fmt.Errorf("run smartctl: %w", ctx.Err())
}

func TestSmartCheckTimeoutMessage(t *testing.T) {
	check := smartCheck{
		base:   base{name: "sm", timeout: time.Millisecond},
		runner: slowSmartRunner{},
		device: "/dev/sda",
	}
	res := check.Run(context.Background())
	if res.OK {
		t.Fatal("expected smart check timeout failure")
	}
	if !strings.Contains(res.Message, "timeout after 1ms") {
		t.Fatalf("message = %q, want timeout after duration", res.Message)
	}
}
