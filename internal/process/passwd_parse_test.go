package process

import "testing"

func TestParseUnixDatabaseLine(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantName string
		wantID   uint32
		wantOK   bool
	}{
		{name: "full passwd line", line: "root:x:0:0:root:/root:/bin/bash", wantName: "root", wantOK: true},
		{name: "short passwd line", line: "alice:x:1000", wantName: "alice", wantID: 1000, wantOK: true},
		{name: "full group line", line: "wheel:x:10:alice,bob", wantName: "wheel", wantID: 10, wantOK: true},
		{name: "short group line", line: "grp:x:42", wantName: "grp", wantID: 42, wantOK: true},
		{name: "too few fields", line: "a:b"},
		{name: "empty name", line: ":x:0"},
		{name: "non-numeric ID", line: "u:x:notnum", wantName: "u"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, id, ok := parseUnixDatabaseLine(tc.line)
			if name != tc.wantName || id != tc.wantID || ok != tc.wantOK {
				t.Fatalf("parseUnixDatabaseLine(%q) = (%q,%d,%v), want (%q,%d,%v)", tc.line, name, id, ok, tc.wantName, tc.wantID, tc.wantOK)
			}
		})
	}
}
