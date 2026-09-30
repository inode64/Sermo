package output

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBounded(t *testing.T) {
	if got := Bounded("", ""); got != "" {
		t.Fatalf("empty streams must yield empty output, got %q", got)
	}
	got := Bounded("hello\n", "boom\n")
	if !strings.Contains(got, streamLabelStdout+streamLabelSeparator+"hello") ||
		!strings.Contains(got, streamLabelStderr+streamLabelSeparator+"boom") {
		t.Fatalf("combined output must label both streams: %q", got)
	}

	var b strings.Builder
	for range boundedMaxLines + 20 {
		b.WriteString("line\n")
	}
	b.WriteString("LASTLINE")
	out := Bounded(b.String(), "")
	if !strings.HasPrefix(out, "… (truncated)") {
		t.Fatalf("over-cap output must be marked truncated: %q", out[:20])
	}
	if !strings.HasSuffix(out, "LASTLINE") {
		t.Fatalf("truncation must keep the tail, got suffix %q", out[len(out)-12:])
	}
	if strings.Count(out, "\n") > boundedMaxLines+1 {
		t.Fatalf("truncated output exceeded line cap: %d lines", strings.Count(out, "\n"))
	}
}

func TestBoundTailBoundaries(t *testing.T) {
	forty := strings.Repeat("x\n", 39) + "x"
	if strings.HasPrefix(boundTail(forty), "… (truncated)") {
		t.Errorf("exactly %d lines must not be truncated", boundedMaxLines)
	}
	fortyOne := strings.Repeat("x\n", 40) + "x"
	if !strings.HasPrefix(boundTail(fortyOne), "… (truncated)") {
		t.Errorf("%d lines must be truncated", boundedMaxLines+1)
	}
	exact := strings.Repeat("a", boundedMaxBytes)
	if strings.HasPrefix(boundTail(exact), "… (truncated)") {
		t.Errorf("a %d-byte single line must not be truncated", boundedMaxBytes)
	}
}

func TestBoundTailKeepsWholeCharactersOnALongLine(t *testing.T) {
	// A single line longer than the cap with multi-byte characters: the byte
	// cut lands inside one and there is no line break to realign on.
	long := strings.Repeat("€", boundedMaxBytes) // 3 bytes each; 4096 is not a multiple of 3
	got := boundTail(long)
	if !utf8.ValidString(got) {
		t.Fatalf("tail starts inside a character: %q…", got[:20])
	}
	if !strings.HasPrefix(got, truncatedOutputPrefix+"€") || len(got) > len(truncatedOutputPrefix)+boundedMaxBytes {
		t.Fatalf("tail = %d bytes, want whole characters within the cap", len(got))
	}
}

func TestFirstNonEmptyLineUsesTrim(t *testing.T) {
	if got := FirstNonEmptyLine("\n\n  \nreal line\n\n  \n"); got != "real line" {
		t.Fatalf("FirstNonEmptyLine after trim gave %q", got)
	}
	if got := FirstNonEmptyLine("\n\n\n"); got != "" {
		t.Fatalf("FirstNonEmptyLine all blank gave %q", got)
	}
}

func TestCauseJoinsALineThatOnlyIntroducesTheReason(t *testing.T) {
	tests := map[string]string{
		"AH00526: Syntax error on line 3 of /etc/a.conf:\n\nInvalid command 'Foo'\nmore\n": "AH00526: Syntax error on line 3 of /etc/a.conf: Invalid command 'Foo'",
		"\n  php: error while loading shared libraries: libicu.so.74\nsecond\n":            "php: error while loading shared libraries: libicu.so.74",
		"Error:\n":         "Error:",
		"":                 "",
		"\n \n":            "",
		"done in 3s: ok\n": "done in 3s: ok",
	}
	for in, want := range tests {
		if got := Cause(in); got != want {
			t.Errorf("Cause(%q) = %q, want %q", in, got, want)
		}
	}
	if got := FailureCause("reported on stdout\n", ""); got != "reported on stdout" {
		t.Errorf("FailureCause fell back to %q", got)
	}
	if got := FailureCause("stdout\n", "stderr wins\n"); got != "stderr wins" {
		t.Errorf("FailureCause preferred %q", got)
	}
}
