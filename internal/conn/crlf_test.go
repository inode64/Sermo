package conn

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func TestReadCRLFLine(t *testing.T) {
	// Both CRLF and bare-LF terminators are trimmed; the reader advances line by
	// line — the contract every text-protocol probe (redis, imap, smtp, …) relies on.
	br := bufio.NewReader(strings.NewReader("+OK ready\r\nsecond line\nno-newline-eof"))

	if s, err := readCRLFLine(br); err != nil || s != "+OK ready" {
		t.Fatalf("line 1 = %q, %v; want %q", s, err, "+OK ready")
	}
	if s, err := readCRLFLine(br); err != nil || s != "second line" {
		t.Fatalf("line 2 = %q, %v; want %q", s, err, "second line")
	}
	// The final line has no terminator: the trimmed text is returned alongside io.EOF.
	if s, err := readCRLFLine(br); s != "no-newline-eof" || err != io.EOF {
		t.Fatalf("line 3 = %q, %v; want %q + io.EOF", s, err, "no-newline-eof")
	}
}
