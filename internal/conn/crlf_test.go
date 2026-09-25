package conn

import (
	"bufio"
	"errors"
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
	if s, err := readCRLFLine(br); s != "no-newline-eof" || !errors.Is(err, io.EOF) {
		t.Fatalf("line 3 = %q, %v; want %q + io.EOF", s, err, "no-newline-eof")
	}
}

func TestReadCRLFLineLenientPreservesBufferedLines(t *testing.T) {
	br := bufio.NewReader(strings.NewReader("first\r\nsecond\nlast"))
	for _, want := range []string{"first", "second", "last"} {
		line, err := readCRLFLineLenient(br)
		if err != nil || line != want {
			t.Fatalf("line = %q, %v; want %q", line, err, want)
		}
	}
	if _, err := readCRLFLineLenient(br); !errors.Is(err, io.EOF) {
		t.Fatalf("empty read = %v, want EOF", err)
	}
}
