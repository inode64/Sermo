package app

import "testing"

func TestParseOSReleasePrettyName(t *testing.T) {
	const data = "NAME=Gentoo\n" +
		"PRETTY_NAME=\"Gentoo Linux\"\n" +
		"ID=gentoo\n"
	if got := parseOSReleasePrettyName([]byte(data)); got != "Gentoo Linux" {
		t.Fatalf("got %q, want %q", got, "Gentoo Linux")
	}
	// Single quotes are stripped too.
	if got := parseOSReleasePrettyName([]byte("PRETTY_NAME='Debian GNU/Linux 12'\n")); got != "Debian GNU/Linux 12" {
		t.Fatalf("got %q", got)
	}
	// Absent / empty -> "".
	if got := parseOSReleasePrettyName([]byte("NAME=x\nID=y\n")); got != "" {
		t.Fatalf("missing PRETTY_NAME should be empty, got %q", got)
	}
	if got := parseOSReleasePrettyName([]byte(`PRETTY_NAME=""`)); got != "" {
		t.Fatalf("empty PRETTY_NAME should be empty, got %q", got)
	}
}
