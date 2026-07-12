package process

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestPIDsByCommFindsSelf(t *testing.T) {
	data, err := os.ReadFile("/proc/self/comm")
	if err != nil {
		t.Skip("no /proc on this host")
	}
	name := strings.TrimSpace(string(data))

	pids, err := PIDsByComm(name)
	if err != nil {
		t.Fatalf("PIDsByComm(%q): %v", name, err)
	}
	self := os.Getpid()
	if slices.Contains(pids, self) {
		return
	}
	t.Fatalf("PIDsByComm(%q) = %v, want it to include self pid %d", name, pids, self)
}

func TestPIDsByCommNoMatch(t *testing.T) {
	if _, err := os.Stat("/proc"); err != nil {
		t.Skip("no /proc on this host")
	}
	pids, err := PIDsByComm("definitely-not-a-running-process-xyz")
	if err != nil {
		t.Fatal(err)
	}
	if len(pids) != 0 {
		t.Fatalf("expected no matches, got %v", pids)
	}
}
