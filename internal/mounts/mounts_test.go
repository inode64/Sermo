package mounts

import "testing"

func TestUnescapeField(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "/mnt/data", want: "/mnt/data"},
		{name: "space", in: `/mnt/my\040disk`, want: "/mnt/my disk"},
		{name: "all supported escapes", in: `/mnt/tab\011nl\012bs\134x`, want: "/mnt/tab\tnl\nbs\\x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UnescapeField(tt.in); got != tt.want {
				t.Fatalf("UnescapeField(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPathUnder(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		mountPoint string
		want       bool
	}{
		{name: "root contains absolute path", path: "/anything", mountPoint: "/", want: true},
		{name: "root rejects relative path", path: "relative", mountPoint: "/", want: false},
		{name: "exact mountpoint", path: "/mnt", mountPoint: "/mnt", want: true},
		{name: "direct child", path: "/mnt/data", mountPoint: "/mnt", want: true},
		{name: "nested child", path: "/mnt/data/x", mountPoint: "/mnt", want: true},
		{name: "sibling prefix", path: "/mntfoo", mountPoint: "/mnt", want: false},
		{name: "other path", path: "/other", mountPoint: "/mnt", want: false},
		{name: "dot path", path: ".", mountPoint: "/mnt", want: false},
		{name: "dot mountpoint", path: "/mnt", mountPoint: ".", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PathUnder(tt.path, tt.mountPoint); got != tt.want {
				t.Fatalf("PathUnder(%q, %q) = %v, want %v", tt.path, tt.mountPoint, got, tt.want)
			}
		})
	}
}
