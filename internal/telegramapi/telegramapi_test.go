package telegramapi

import "testing"

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
