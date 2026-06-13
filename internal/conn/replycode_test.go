package conn

import (
	"bufio"
	"strings"
	"testing"
)

// readReplyCode is the shared SMTP/FTP/NNTP reply parser; the multi-line form
// (continuation lines marked by a '-' after the code, a space on the last) is
// the easy thing to get wrong, so pin it directly.
func TestReadReplyCode(t *testing.T) {
	cases := []struct {
		name string
		in   string
		code int
		text string
	}{
		{"single line", "220 mail.example.com ESMTP\r\n", 220, "mail.example.com ESMTP"},
		{"multi line joins parts", "250-mail\r\n250-PIPELINING\r\n250 SIZE 100\r\n", 250, "mail PIPELINING SIZE 100"},
		{"code only, no text", "200\r\n", 200, ""},
		{"continuation with empty text then final", "250-\r\n250 ok\r\n", 250, "ok"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, text, err := readReplyCode(bufio.NewReader(strings.NewReader(c.in)))
			if err != nil || code != c.code || text != c.text {
				t.Fatalf("= (%d, %q, %v), want (%d, %q, nil)", code, text, err, c.code, c.text)
			}
		})
	}

	// Malformed lines are errors, not silent zero codes.
	for _, bad := range []string{"ab\r\n", "xyz hello\r\n", "12\r\n"} {
		if _, _, err := readReplyCode(bufio.NewReader(strings.NewReader(bad))); err == nil {
			t.Errorf("readReplyCode(%q) = nil error, want malformed error", bad)
		}
	}
}
