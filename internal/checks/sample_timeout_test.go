package checks

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTallyEntriesTimeoutMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-ctx.Done()

	_, err := TallyEntries(ctx, t.TempDir(), "any", false, time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout after 1ms") {
		t.Fatalf("error = %q, want timeout after duration", err.Error())
	}
}

func TestSamplePathSizeTimeoutMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-ctx.Done()

	_, err := SamplePathSize(ctx, t.TempDir(), time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout after 1ms") {
		t.Fatalf("error = %q, want timeout after duration", err.Error())
	}
}

func TestSampleHdparmTimeoutMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-ctx.Done()

	_, err := SampleHdparm(ctx, slowHdparmRunner{}, "/dev/sda", true, false, time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout after 1ms") {
		t.Fatalf("error = %q, want timeout after duration", err.Error())
	}
}

func TestSampleSmartTimeoutMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-ctx.Done()

	_, err := SampleSmart(ctx, slowSmartRunner{}, "/dev/sda", time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout after 1ms") {
		t.Fatalf("error = %q, want timeout after duration", err.Error())
	}
}
