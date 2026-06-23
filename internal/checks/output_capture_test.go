package checks

import (
	"context"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
)

func TestBoundedOutput(t *testing.T) {
	if got := BoundedOutput("", ""); got != "" {
		t.Fatalf("empty streams must yield empty output, got %q", got)
	}
	got := BoundedOutput("hello\n", "boom\n")
	if !strings.Contains(got, "stdout:\nhello") || !strings.Contains(got, "stderr:\nboom") {
		t.Fatalf("combined output must label both streams: %q", got)
	}

	// Keeps the tail and marks truncation when over the line cap.
	var b strings.Builder
	for i := 0; i < boundedOutputMaxLines+20; i++ {
		b.WriteString("line\n")
	}
	b.WriteString("LASTLINE")
	out := BoundedOutput(b.String(), "")
	if !strings.HasPrefix(out, "… (truncated)") {
		t.Fatalf("over-cap output must be marked truncated: %q", out[:20])
	}
	if !strings.HasSuffix(out, "LASTLINE") {
		t.Fatalf("truncation must keep the tail (the error), got suffix %q", out[len(out)-12:])
	}
	if strings.Count(out, "\n") > boundedOutputMaxLines+1 {
		t.Fatalf("truncated output exceeded line cap: %d lines", strings.Count(out, "\n"))
	}
}

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
