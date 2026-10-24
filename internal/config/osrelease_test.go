package config

import "testing"

func TestParseOSReleaseValue(t *testing.T) {
	const data = "NAME=Gentoo\n" +
		"PRETTY_NAME=\"Gentoo Linux\"\n" +
		"ID=gentoo\n"
	if got := ParseOSReleaseValue([]byte(data), OSReleaseKeyPrettyName); got != "Gentoo Linux" {
		t.Fatalf("got %q, want %q", got, "Gentoo Linux")
	}
	// Single quotes are stripped too.
	if got := ParseOSReleaseValue([]byte("PRETTY_NAME='Debian GNU/Linux 12'\n"), OSReleaseKeyPrettyName); got != "Debian GNU/Linux 12" {
		t.Fatalf("got %q", got)
	}
	// Absent / empty -> "".
	if got := ParseOSReleaseValue([]byte("NAME=x\nID=y\n"), OSReleaseKeyPrettyName); got != "" {
		t.Fatalf("missing PRETTY_NAME should be empty, got %q", got)
	}
	if got := ParseOSReleaseValue([]byte(`PRETTY_NAME=""`), OSReleaseKeyPrettyName); got != "" {
		t.Fatalf("empty PRETTY_NAME should be empty, got %q", got)
	}
	if got := ParseOSReleaseValue([]byte(data), OSReleaseKeyID); got != "gentoo" {
		t.Fatalf("ID = %q, want gentoo", got)
	}
}
