package telegramapi

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestURLMatchesPreRefactorSpelling(t *testing.T) {
	// The notifier used to build: base + token + "/sendMessage".
	const token = "123:abc"
	want := "https://api.telegram.org/bot" + token + "/sendMessage"
	if got := MethodURL(token, MethodSendMessage); got != want {
		t.Fatalf("MethodURL = %q, want %q", got, want)
	}
	// The bot client used: base + token + "/" + method.
	wantGet := "https://api.telegram.org/bot" + token + "/" + "getUpdates"
	if got := MethodURL(token, MethodGetUpdates); got != wantGet {
		t.Fatalf("MethodURL = %q, want %q", got, wantGet)
	}
}

func TestEscapeTextForParseMode(t *testing.T) {
	const raw = `[sermo] SERMO_WATCH=disk-root <a & b> *x* 1.5 (ok)! \`
	tests := []struct {
		name      string
		parseMode string
		want      string
	}{
		{name: "plain", parseMode: "", want: raw},
		{name: "MarkdownV2", parseMode: "MarkdownV2", want: `\[sermo\] SERMO\_WATCH\=disk\-root <a & b\> \*x\* 1\.5 \(ok\)\! \\`},
		{name: "Markdown", parseMode: "Markdown", want: `\[sermo] SERMO\_WATCH=disk-root <a & b> \*x\* 1.5 (ok)! \`},
		{name: "HTML", parseMode: "HTML", want: `[sermo] SERMO_WATCH=disk-root &lt;a &amp; b&gt; *x* 1.5 (ok)! \`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EscapeText(tt.parseMode, raw); got != tt.want {
				t.Fatalf("EscapeText(%q) = %q, want %q", tt.parseMode, got, tt.want)
			}
		})
	}
}

func TestTextLengthCountsUTF16Units(t *testing.T) {
	if got := TextLength("añ😀"); got != 4 { // a, ñ (1 unit each), 😀 (surrogate pair)
		t.Fatalf("TextLength = %d, want 4", got)
	}
}

func TestSplitTextKeepsShortTextWhole(t *testing.T) {
	text := strings.Repeat("x", MaxTextLength)
	if parts := SplitText(text, 3); len(parts) != 1 || parts[0] != text {
		t.Fatalf("text at the limit must stay one part, got %d parts", len(parts))
	}
}

func TestSplitTextBreaksAtLinesWithinLimit(t *testing.T) {
	line := strings.Repeat("é", 99) // 99 units, 198 bytes
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = line
	}
	text := strings.Join(lines, "\n")
	parts := SplitText(text, 10)
	if len(parts) < 2 {
		t.Fatalf("parts = %d, want a split", len(parts))
	}
	if got := strings.Join(parts, "\n"); got != text {
		t.Fatal("line-split parts must rejoin into the original text")
	}
	for i, p := range parts {
		if TextLength(p) > MaxTextLength || !utf8.ValidString(p) {
			t.Fatalf("part %d: %d units, valid=%v", i, TextLength(p), utf8.ValidString(p))
		}
		if strings.HasPrefix(p, "\n") || strings.HasSuffix(p, "\n") {
			t.Fatalf("part %d must break at a line boundary: %q…", i, p[:10])
		}
	}
}

func TestSplitTextCutsLongLineAtRuneBoundary(t *testing.T) {
	text := strings.Repeat("😀", MaxTextLength) // 2 units each, no line breaks
	parts := SplitText(text, 10)
	for i, p := range parts {
		if TextLength(p) > MaxTextLength || !utf8.ValidString(p) {
			t.Fatalf("part %d: %d units, valid=%v", i, TextLength(p), utf8.ValidString(p))
		}
	}
	if strings.Join(parts, "") != text {
		t.Fatal("rune-split parts must rejoin into the original text")
	}
}

func TestSplitTextTruncatesPastMaxParts(t *testing.T) {
	text := strings.Repeat("line of output\n", 1000)
	parts := SplitText(text, 2)
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	last := parts[1]
	if !strings.HasSuffix(last, TruncatedMarker) || TextLength(last) > MaxTextLength {
		t.Fatalf("last part must end with the marker within the limit (%d units)", TextLength(last))
	}
	if got := TruncateText(text); !strings.HasSuffix(got, TruncatedMarker) || TextLength(got) > MaxTextLength {
		t.Fatalf("TruncateText = %d units", TextLength(got))
	}
}
