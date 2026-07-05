package output

import (
	"strings"
	"testing"
)

func TestBounded(t *testing.T) {
	if got := Bounded("", ""); got != "" {
		t.Fatalf("empty streams must yield empty output, got %q", got)
	}
	got := Bounded("hello\n", "boom\n")
	if !strings.Contains(got, "stdout:\nhello") || !strings.Contains(got, "stderr:\nboom") {
		t.Fatalf("combined output must label both streams: %q", got)
	}

	var b strings.Builder
	for i := 0; i < boundedMaxLines+20; i++ {
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

func TestTrim(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"only ws", "   \t\n  ", ""},
		{"single line", " hello world \n", "hello world"},
		{"leading blank lines", "\n\n\nline1\nline2", "line1\nline2"},
		{"trailing blank lines", "line1\nline2\n\n\n", "line1\nline2"},
		{"both ends + internal blank", "\n\n  \nfirst\n\nmiddle\n\nlast\n\n  ", "first\n\nmiddle\n\nlast"},
		{"all blank lines", "\n\n\t\n  \n", ""},
		{"mixed whitespace lines", "  \r\n\t\nreal\n   \n  ", "real"},
		{"version banner typical", "\n\nPostgreSQL 15.3\n\n", "PostgreSQL 15.3"},
		{"sql with trailing", "col1\nval1\n\n", "col1\nval1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Trim(tt.in); got != tt.want {
				t.Fatalf("Trim(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
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
