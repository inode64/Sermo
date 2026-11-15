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

func TestObservedCollapsesWhitespaceAndBounds(t *testing.T) {
	if got := Observed(" LoadState=loaded\n\tResult=exit-code \n"); got != "LoadState=loaded Result=exit-code" {
		t.Fatalf("Observed() = %q", got)
	}
	long := strings.Repeat("ñ", observedMaxRunes+5)
	got := Observed(long)
	if utf8.RuneCountInString(got) != observedMaxRunes+1 || !strings.HasSuffix(got, observedEllipsis) {
		t.Fatalf("Observed(long) = %d runes, suffix %q", utf8.RuneCountInString(got), got[len(got)-len(observedEllipsis):])
	}
	if Observed("   ") != "" {
		t.Fatal("Observed(blank) must be empty")
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
		if got := cause(in); got != want {
			t.Errorf("cause(%q) = %q, want %q", in, got, want)
		}
	}
	if got := FailureCause("reported on stdout\n", ""); got != "reported on stdout" {
		t.Errorf("FailureCause fell back to %q", got)
	}
	if got := FailureCause("stdout\n", "stderr wins\n"); got != "stderr wins" {
		t.Errorf("FailureCause preferred %q", got)
	}
	if got := FailureCause("", "Error:\n"); got != "Error:" {
		t.Errorf("FailureCause with an empty stdout gave %q", got)
	}
}

// Gentoo's apache2ctl configtest (its init script) prints the syntax error on
// stdout and only announces it on stderr; the reason is the stdout one.
func TestFailureCauseSkipsAStderrThatOnlyIntroducesTheReason(t *testing.T) {
	const stdout = " * Checking apache2 configuration ...AH00526: Syntax error on line 2 of /etc/apache2/vhosts.d/zz.conf:\n" +
		"Invalid command 'Bogus', perhaps misspelled or defined by a module not included in the server configuration\n [ !! ]\n"
	const stderr = "\n * apache2 has detected an error in your setup:\n"
	want := "* Checking apache2 configuration ...AH00526: Syntax error on line 2 of /etc/apache2/vhosts.d/zz.conf: " +
		"Invalid command 'Bogus', perhaps misspelled or defined by a module not included in the server configuration"
	if got := FailureCause(stdout, stderr); got != want {
		t.Errorf("FailureCause = %q, want %q", got, want)
	}
}

// A Python program that died of an exception names it on the traceback's last
// line; the frames before it, and any warning printed first, are not the reason.
func TestCauseOfAPythonTracebackIsItsException(t *testing.T) {
	const traceback = `Traceback (most recent call last):
  File "/usr/bin/ceph", line 163, in <module>
    from ceph_daemon import admin_socket, DaemonWatcher, Termsize
  File "/usr/lib/python3.13/site-packages/ceph_daemon.py", line 21, in <module>
    from prettytable import PrettyTable, HEADER
ModuleNotFoundError: No module named 'prettytable'
`
	const want = "ModuleNotFoundError: No module named 'prettytable'"
	for name, stderr := range map[string]string{
		"traceback":               traceback,
		"after a warning":         "/usr/bin/ceph:7: DeprecationWarning: something old\n" + traceback,
		"without a final newline": strings.TrimSuffix(traceback, "\n"),
	} {
		if got := FailureCause("", stderr); got != want {
			t.Errorf("%s: FailureCause = %q, want %q", name, got, want)
		}
	}
}

// A program that could not load a library it needs is reported by the loader,
// by Python's import machinery or by Perl's module search; any other failure,
// including a traceback that ended in another exception, is not such a death.
func TestMissingLibraryNamesTheLibraryAProgramCouldNotLoad(t *testing.T) {
	const cephTraceback = `Traceback (most recent call last):
  File "/usr/bin/ceph", line 163, in <module>
    from ceph_daemon import admin_socket, DaemonWatcher, Termsize
ModuleNotFoundError: No module named 'prettytable'
`
	tests := []struct {
		name, stdout, stderr, want string
	}{
		{name: "python module", stderr: cephTraceback, want: "ModuleNotFoundError: No module named 'prettytable'"},
		{
			name:   "python import of a missing name",
			stderr: "Traceback (most recent call last):\n  File \"/usr/bin/tool\", line 2, in <module>\nImportError: cannot import name 'X' from 'lib'\n",
			want:   "ImportError: cannot import name 'X' from 'lib'",
		},
		{
			name:   "loader shared object",
			stderr: "/usr/bin/redis-cli: error while loading shared libraries: libssl.so.3: cannot open shared object file: No such file or directory\n",
			want:   "/usr/bin/redis-cli: error while loading shared libraries: libssl.so.3: cannot open shared object file: No such file or directory",
		},
		{
			name:   "loader symbol version",
			stderr: "/usr/bin/tool: /lib64/libc.so.6: version `GLIBC_2.38' not found (required by /usr/bin/tool)\n",
			want:   "/usr/bin/tool: /lib64/libc.so.6: version `GLIBC_2.38' not found (required by /usr/bin/tool)",
		},
		{
			name:   "perl module",
			stderr: "Can't locate DBI.pm in @INC (you may need to install the DBI module) (@INC entries checked: /etc/perl) at /usr/bin/check line 3.\nBEGIN failed--compilation aborted at /usr/bin/check line 3.\n",
			want:   "Can't locate DBI.pm in @INC (you may need to install the DBI module) (@INC entries checked: /etc/perl) at /usr/bin/check line 3.",
		},
		{name: "reported on stdout", stdout: cephTraceback, want: "ModuleNotFoundError: No module named 'prettytable'"},
		{
			name:   "traceback of another exception",
			stderr: "Traceback (most recent call last):\n  File \"/usr/bin/ceph\", line 9, in <module>\nImportError: early\nConnectionRefusedError: [Errno 111] Connection refused\n",
		},
		{name: "service failure", stderr: "admin_socket: exception getting command descriptions: [Errno 2] No such file or directory\n"},
		{name: "perl without the module search", stderr: "Can't locate object method \"new\" via package \"Foo\" at /usr/bin/check line 4.\n"},
		{name: "no output"},
	}
	for _, tt := range tests {
		if got := MissingLibrary(tt.stdout, tt.stderr); got != tt.want {
			t.Errorf("%s: MissingLibrary = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// The exception is the last line shaped like one: notes and the rest of a
// multi-line message printed after it, and the boxes an ExceptionGroup draws
// around its sub-exceptions, are not the reason.
func TestCauseOfAPythonTracebackSkipsNotesAndFrames(t *testing.T) {
	const frames = "Traceback (most recent call last):\n  File \"/usr/bin/tool\", line 3, in <module>\n    main()\n"
	group := `  + Exception Group Traceback (most recent call last):
  |   File "/usr/bin/tool", line 9, in <module>
  |     asyncio.run(main())
  | ExceptionGroup: unhandled errors in a TaskGroup (1 sub-exception)
  +-+---------------- 1 ----------------
    | Traceback (most recent call last):
    |   File "/usr/bin/tool", line 5, in probe
    |     import procfs
    | ModuleNotFoundError: No module named 'procfs'
    +------------------------------------
`
	for name, tc := range map[string]struct{ stderr, want string }{
		"note":               {frames + "ValueError: bad value\nwhile parsing /etc/tool.conf\n", "ValueError: bad value"},
		"multi-line message": {frames + "RuntimeError: first line\nsecond line\n", "RuntimeError: first line"},
		"exception group":    {group, "ModuleNotFoundError: No module named 'procfs'"},
		"bare exception":     {frames + "KeyboardInterrupt\n", "KeyboardInterrupt"},
		"dotted name":        {frames + "salt.exceptions.SaltClientError: no master\n", "salt.exceptions.SaltClientError: no master"},
		"no exception line":  {frames + "killed\n", "killed"},
	} {
		if got := FailureCause("", tc.stderr); got != tc.want {
			t.Errorf("%s: FailureCause = %q, want %q", name, got, tc.want)
		}
	}
	if got := MissingLibrary("", group); got != "ModuleNotFoundError: No module named 'procfs'" {
		t.Errorf("MissingLibrary(exception group) = %q, want the sub-exception", got)
	}
}
