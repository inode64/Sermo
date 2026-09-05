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
		{name: "root contains itself", path: "/", mountPoint: "/", want: true},
		{name: "root contains absolute path", path: "/anything", mountPoint: "/", want: true},
		{name: "root rejects relative path", path: "relative", mountPoint: "/", want: false},
		{name: "exact mountpoint", path: "/mnt", mountPoint: "/mnt", want: true},
		{name: "direct child", path: "/mnt/data", mountPoint: "/mnt", want: true},
		{name: "nested child", path: "/mnt/data/x", mountPoint: "/mnt", want: true},
		{name: "cleans path", path: "/mnt/other/../data", mountPoint: "/mnt", want: true},
		{name: "cleans mountpoint", path: "/mnt/data", mountPoint: "/mnt/.", want: true},
		{name: "cleans repeated separators", path: "/mnt//data", mountPoint: "/mnt/", want: true},
		{name: "sibling prefix", path: "/mntfoo", mountPoint: "/mnt", want: false},
		{name: "other path", path: "/other", mountPoint: "/mnt", want: false},
		{name: "dot path", path: ".", mountPoint: "/mnt", want: false},
		{name: "dot mountpoint", path: "/mnt", mountPoint: ".", want: false},
		{name: "relative pair", path: "mnt/data", mountPoint: "mnt", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PathUnder(tt.path, tt.mountPoint); got != tt.want {
				t.Fatalf("PathUnder(%q, %q) = %v, want %v", tt.path, tt.mountPoint, got, tt.want)
			}
		})
	}
}

func TestPathStrictlyUnder(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		mountPoint string
		want       bool
	}{
		{name: "root excludes itself", path: "/", mountPoint: "/", want: false},
		{name: "root contains child", path: "/anything", mountPoint: "/", want: true},
		{name: "exact mountpoint", path: "/mnt", mountPoint: "/mnt", want: false},
		{name: "trailing slash is still the mountpoint", path: "/mnt/", mountPoint: "/mnt", want: false},
		{name: "clean equality", path: "/mnt/data/..", mountPoint: "/mnt", want: false},
		{name: "direct child", path: "/mnt/data", mountPoint: "/mnt", want: true},
		{name: "nested child", path: "/mnt/data/x", mountPoint: "/mnt", want: true},
		{name: "sibling prefix", path: "/mntfoo", mountPoint: "/mnt", want: false},
		{name: "relative pair", path: "mnt/data", mountPoint: "mnt", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PathStrictlyUnder(tt.path, tt.mountPoint); got != tt.want {
				t.Fatalf("PathStrictlyUnder(%q, %q) = %v, want %v", tt.path, tt.mountPoint, got, tt.want)
			}
		})
	}
}
