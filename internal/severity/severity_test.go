package severity

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for _, level := range levels {
		if got, ok := Parse(string(level)); !ok || got != level {
			t.Errorf("Parse(%q) = %q, %v; want %q, true", level, got, ok, level)
		}
	}
	// "ok" grades an analyze match, not a failure; case matters because a
	// severity is a keyword.
	for _, bad := range []string{"", "ok", "Warning", "urgent", " error"} {
		if got, ok := Parse(bad); ok {
			t.Errorf("Parse(%q) = %q, true; want invalid", bad, got)
		}
	}
}

func TestRankIsAscending(t *testing.T) {
	for i, level := range levels {
		if level.Rank() != i {
			t.Errorf("%s.Rank() = %d, want %d", level, level.Rank(), i)
		}
	}
	if Level("").Rank() >= Debug.Rank() || Level("nope").Rank() >= Debug.Rank() {
		t.Error("an unset or unknown level must rank below debug")
	}
}

func TestSummaryNamesEveryLevel(t *testing.T) {
	names := make([]string, len(levels))
	for i, level := range levels {
		names[i] = string(level)
	}
	want := strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
	if Summary != want {
		t.Fatalf("Summary = %q, want %q", Summary, want)
	}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name               string
		declared, fallback Level
		want               Level
	}{
		{"undeclared is an error", "", "", Error},
		{"declared wins", Warning, Error, Warning},
		{"empty inherits", "", Info, Info},
		{"critical overrules an inherited warning", Critical, Warning, Critical},
		// A value that never passed validation must not silently demote a check.
		{"garbage inherits", "nope", Warning, Warning},
		{"garbage with no fallback is an error", "nope", "", Error},
		{"ok is not a severity", "ok", "", Error},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Resolve(tt.declared, tt.fallback); got != tt.want {
				t.Fatalf("Resolve(%q, %q) = %q, want %q", tt.declared, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestAdvisory(t *testing.T) {
	tests := map[Level]bool{"": false, Debug: true, Info: true, Warning: true, Error: false, Critical: false, "nope": false}
	for level, want := range tests {
		if got := level.Advisory(); got != want {
			t.Errorf("%q.Advisory() = %v, want %v", level, got, want)
		}
	}
}

func TestAtLeast(t *testing.T) {
	tests := []struct {
		level, minimum Level
		want           bool
	}{
		{Debug, Debug, true},
		{Warning, Error, false},
		{Error, Error, true},
		{Critical, Error, true},
		// An unset level is an error; an unset minimum filters nothing.
		{"", Error, true},
		{"", Critical, false},
		{Debug, "", true},
	}
	for _, tt := range tests {
		if got := tt.level.AtLeast(tt.minimum); got != tt.want {
			t.Errorf("%q.AtLeast(%q) = %v, want %v", tt.level, tt.minimum, got, tt.want)
		}
	}
}

func TestMax(t *testing.T) {
	tests := []struct{ a, b, want Level }{
		{Warning, Error, Error},
		{Critical, Info, Critical},
		{"", Warning, Warning},
		{Warning, "", Warning},
		{"nope", Debug, Debug},
		{"", "", ""},
	}
	for _, tt := range tests {
		if got := Max(tt.a, tt.b); got != tt.want {
			t.Errorf("Max(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
	}
}
