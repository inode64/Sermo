package locks

import (
	"os"
	"testing"
)

func TestOSProberSelfIsAliveWithStartTicks(t *testing.T) {
	p := OSProcessProber{}
	pid := os.Getpid()
	if !p.Alive(pid) {
		t.Fatalf("Alive(self=%d) = false, want true", pid)
	}
	if ticks, ok := p.StartTicks(pid); !ok || ticks == 0 {
		t.Fatalf("StartTicks(self) = (%d, %v), want a non-zero value", ticks, ok)
	}
}

func TestOSProberRejectsInvalidPID(t *testing.T) {
	p := OSProcessProber{}
	if p.Alive(0) || p.Alive(-1) {
		t.Fatalf("Alive should reject non-positive PIDs")
	}
}
