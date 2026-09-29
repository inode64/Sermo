package procnet

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
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

func TestCountTCPConnectionsSocketTables(t *testing.T) {
	const row = "  sl  local_address rem_address   st\n   0: 0100007F:0015 0100007F:AF20 01\n"
	dir := t.TempDir()
	tcp := filepath.Join(dir, "tcp")
	tcp6 := filepath.Join(dir, "tcp6")
	for _, path := range []string{tcp, tcp6} {
		if err := os.WriteFile(path, []byte(row), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	missing := filepath.Join(dir, "missing")
	unreadable := t.TempDir() // a directory: opens, but reading it fails

	tests := []struct {
		name     string
		tcp      string
		tcp6     string
		want     int
		wantPath string
	}{
		{name: "both families", tcp: tcp, tcp6: tcp6, want: 2},
		{name: "IPv6 stack disabled", tcp: tcp, tcp6: missing, want: 1},
		{name: "missing IPv4 table", tcp: missing, tcp6: tcp6, wantPath: missing},
		{name: "unreadable IPv6 table", tcp: tcp, tcp6: unreadable, wantPath: unreadable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := countTCPConnections(21, tt.tcp, tt.tcp6)
			if tt.wantPath != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantPath) {
					t.Fatalf("err = %v, want an unavailable observation naming %s", err, tt.wantPath)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("count = %d, %v; want %d", got, err, tt.want)
			}
		})
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

func TestScanSocketRowsPropagatesErrors(t *testing.T) {
	failure := errors.New("socket table failed")
	if err := ScanSocketRows(iotest.ErrReader(failure), MinFields, func([]string) (bool, error) {
		t.Fatal("callback after failed read")
		return false, nil
	}); !errors.Is(err, failure) {
		t.Fatalf("reader error = %v", err)
	}
	err := ScanSocketRows(strings.NewReader("0: 0100007F:0015 00000000:0000 01\n"), MinFields, func([]string) (bool, error) {
		return false, failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("callback error = %v", err)
	}
}
