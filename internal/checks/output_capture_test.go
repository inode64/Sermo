package checks

import (
	"context"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
)

func TestCommandCheckCapturesOutputOnFailure(t *testing.T) {
	c := commandCheck{
		base:       base{name: "c", timeout: time.Second},
		runner:     fakeRunner{execx.Result{ExitCode: 1, Stdout: "starting\n", Stderr: "Traceback\nImportError: x\n"}},
		argv:       []string{"x"},
		expectExit: []int{0},
	}
	r := c.Run(context.Background())
	if r.OK {
		t.Fatal("exit 1 (want 0) should fail")
	}
	out, _ := r.Data["output"].(string)
	if !strings.Contains(out, "ImportError: x") || !strings.Contains(out, "starting") {
		t.Fatalf("failure must capture full stdout/stderr in Data[output], got %q", out)
	}

	ok := commandCheck{
		base:       base{name: "c", timeout: time.Second},
		runner:     fakeRunner{execx.Result{ExitCode: 0, Stdout: "fine\n"}},
		argv:       []string{"x"},
		expectExit: []int{0},
	}
	if r := ok.Run(context.Background()); r.Data["output"] != nil {
		t.Fatalf("success must not attach output, got %v", r.Data["output"])
	}
}
