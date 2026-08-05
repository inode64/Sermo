//go:build linux

package utmp

import "testing"

func TestTTYPathRejectsUnsafeLines(t *testing.T) {
	tests := []struct {
		line string
		ok   bool
	}{
		{line: "pts/0", ok: true},
		{line: "../pts/0"},
		{line: "/dev/pts/0"},
		{line: ".."},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			path, ok := TTYPath("/dev", tt.line)
			if ok != tt.ok {
				t.Fatalf("TTYPath(%q) = %q, %v; want ok=%v", tt.line, path, ok, tt.ok)
			}
			if tt.ok && path != "/dev/pts/0" {
				t.Fatalf("TTYPath(%q) = %q, want /dev/pts/0", tt.line, path)
			}
		})
	}
}
