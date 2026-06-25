package buildinfo

import (
	"runtime"
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	s := String()
	if !strings.HasPrefix(s, "sermo ") {
		t.Errorf("String() = %q, want it to start with %q", s, "sermo ")
	}
	if !strings.Contains(s, runtime.Version()) {
		t.Errorf("String() = %q, want it to contain Go version %q", s, runtime.Version())
	}
	if !strings.Contains(s, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Errorf("String() = %q, want it to contain %s/%s", s, runtime.GOOS, runtime.GOARCH)
	}
}

func TestStringVersionOverride(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "9.9.9"
	if got := String(); !strings.Contains(got, "sermo 9.9.9") {
		t.Errorf("String() = %q, want it to contain %q", got, "sermo 9.9.9")
	}
}

func TestShortMatchesResolvedVersion(t *testing.T) {
	version, revision, _ := resolve()
	want := version
	if revision != "" {
		want += " (" + revision + ")"
	}
	if got := Short(); got != want {
		t.Errorf("Short() = %q, want %q", got, want)
	}
}

func TestStringMatchesResolvedBanner(t *testing.T) {
	version, revision, date := resolve()
	line := "sermo " + version
	var meta []string
	if revision != "" {
		meta = append(meta, revision)
	}
	if date != "" {
		meta = append(meta, date)
	}
	if len(meta) > 0 {
		line += " (" + strings.Join(meta, ", ") + ")"
	}
	want := line + "\n  " + runtime.Version() + ", " + runtime.GOOS + "/" + runtime.GOARCH
	if got := String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
