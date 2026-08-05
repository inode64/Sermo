package procnet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCountPortState(t *testing.T) {
	const table = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:0015 0100007F:AF20 01 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
   1: 0100007F:0015 0100007F:AF21 01 00000000:00000000 00:00000000 00000000     0        0 12346 1 0000000000000000 100 0 0 10 0
   2: 0100007F:0015 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12347 1 0000000000000000 100 0 0 10 0
   3: 0100007F:0050 0100007F:AF22 01 00000000:00000000 00:00000000 00000000     0        0 12348 1 0000000000000000 100 0 0 10 0
`

	count, err := countPortState(strings.NewReader(table), 21, StateEstablished)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2 established sockets on port 21", count)
	}
}

func TestCountTCPConnectionsRejectsPartialSocketTables(t *testing.T) {
	dir := t.TempDir()
	tcp := filepath.Join(dir, "tcp")
	if err := os.WriteFile(tcp, []byte("  sl  local_address rem_address   st\n   0: 0100007F:0015 0100007F:AF20 01\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missingTCP6 := filepath.Join(dir, "tcp6")

	if _, err := countTCPConnections(21, []string{tcp, missingTCP6}); err == nil {
		t.Fatal("partial TCP tables must be unavailable")
	} else if !strings.Contains(err.Error(), missingTCP6) {
		t.Fatalf("error = %q, want missing TCP6 path", err)
	}
}

func TestScanPortStateStops(t *testing.T) {
	const table = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:0015 0100007F:AF20 01 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
   1: 0100007F:0015 0100007F:AF21 01 00000000:00000000 00:00000000 00000000     0        0 12346 1 0000000000000000 100 0 0 10 0
`
	seen := 0
	err := ScanPortState(strings.NewReader(table), 21, map[string]bool{StateEstablished: true}, func(localAddress string) bool {
		seen++
		return false
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("visited %d matching rows, want 1 after early stop", seen)
	}
}
