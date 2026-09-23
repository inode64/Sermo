package checks

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const logTestMatch = "OpenTelemetry: [error] Export failure: cURL error 28: Connection timed out after 10002 milliseconds\n"

// logFixture is a log file under a temp dir plus a movable clock, so a test
// drives cycles without sleeping.
type logFixture struct {
	t     *testing.T
	dir   string
	path  string
	now   time.Time
	check logCheck
}

func newLogFixture(t *testing.T, pattern string, op string, value float64, window time.Duration) *logFixture {
	t.Helper()
	f := &logFixture{t: t, dir: t.TempDir(), now: time.Unix(1_700_000_000, 0)}
	f.path = filepath.Join(f.dir, "worker_err.log")
	f.write("boot line\n")
	f.check = logCheck{
		name:   "l",
		path:   pattern,
		regex:  regexp.MustCompile(`cURL error 28`),
		op:     op,
		value:  value,
		window: window,
		clock:  func() time.Time { return f.now },
		state:  newLogState(),
	}
	if pattern == "" {
		f.check.path = f.path
	}
	return f
}

func (f *logFixture) write(s string) {
	f.t.Helper()
	if err := os.WriteFile(f.path, []byte(s), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *logFixture) append(s string) {
	f.t.Helper()
	fh, err := os.OpenFile(f.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		f.t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fh.WriteString(s); err != nil {
		f.t.Fatal(err)
	}
}

func (f *logFixture) run() Result {
	f.t.Helper()
	return f.check.Run(context.Background())
}

func (f *logFixture) count(res Result) int {
	f.t.Helper()
	n, ok := res.Data[DataKeyCount].(int)
	if !ok {
		f.t.Fatalf("result %+v has no integer count", res)
	}
	return n
}

func TestLogFirstRunBaselinesAtEOF(t *testing.T) {
	f := newLogFixture(t, "", ">", 0, 5*time.Minute)
	f.append(logTestMatch + logTestMatch)
	res := f.run()
	if res.OK || f.count(res) != 0 {
		t.Fatalf("first run = %+v, want 0 matches (existing lines are history, not news) so the condition does not hold", res)
	}
}

func TestLogCountsAppendedMatchesWithinWindow(t *testing.T) {
	f := newLogFixture(t, "", ">", 2, 5*time.Minute)
	f.run()
	f.now = f.now.Add(30 * time.Second)
	f.append(logTestMatch + "plain line\n" + logTestMatch + "another plain line\n" + logTestMatch)
	res := f.run()
	if !res.OK || f.count(res) != 3 {
		t.Fatalf("after 3 matching lines = %+v, want 3 matches and the condition (> 2) to hold", res)
	}
	if !strings.Contains(res.Message, "3 line(s) matching") {
		t.Fatalf("message %q should state the match count", res.Message)
	}
	f.now = f.now.Add(6 * time.Minute)
	res = f.run()
	if res.OK || f.count(res) != 0 {
		t.Fatalf("after the window elapsed = %+v, want 0 matches and the condition released", res)
	}
}

func TestLogPartialLineIsCountedNextCycle(t *testing.T) {
	f := newLogFixture(t, "", ">", 0, 5*time.Minute)
	f.run()
	f.append("OpenTelemetry: [error] cURL error")
	if res := f.run(); f.count(res) != 0 {
		t.Fatalf("half a line = %+v, want 0 (no newline yet)", res)
	}
	f.append(" 28: Connection timed out\n")
	if res := f.run(); f.count(res) != 1 {
		t.Fatalf("completed line = %+v, want 1", res)
	}
}

func TestLogRotationByRenameReadsNewFileFromStart(t *testing.T) {
	f := newLogFixture(t, "", ">", 0, 5*time.Minute)
	f.append(logTestMatch)
	f.run()
	if err := os.Rename(f.path, f.path+"-1"); err != nil {
		t.Fatal(err)
	}
	f.write(logTestMatch + logTestMatch)
	res := f.run()
	if f.count(res) != 2 {
		t.Fatalf("after rename rotation = %+v, want the 2 lines of the new file", res)
	}
}

func TestLogTruncationReadsFromZero(t *testing.T) {
	f := newLogFixture(t, "", ">", 0, 5*time.Minute)
	f.append(logTestMatch + logTestMatch + logTestMatch)
	f.run()
	f.write(logTestMatch)
	res := f.run()
	if f.count(res) != 1 {
		t.Fatalf("after truncation = %+v, want the 1 line written since", res)
	}
}

func TestLogReadBudgetTruncates(t *testing.T) {
	f := newLogFixture(t, "", ">", 0, 5*time.Minute)
	f.run()
	f.check.budget = int64(len(logTestMatch)*2 + 5)
	f.append(strings.Repeat(logTestMatch, 10))
	res := f.run()
	if f.count(res) != 2 {
		t.Fatalf("budgeted read = %+v, want the 2 whole lines that fit", res)
	}
	if truncated, _ := res.Data[DataKeyTruncated].(bool); !truncated {
		t.Fatalf("budgeted read = %+v, want truncated=true", res)
	}
	if res = f.run(); f.count(res) != 2 {
		t.Fatalf("next cycle = %+v, want the skipped remainder not re-read (count stays at 2 in window)", res)
	}
}

func TestLogGlobSumsAcrossFilesAndDropsVanished(t *testing.T) {
	f := newLogFixture(t, "", ">", 0, 5*time.Minute)
	f.check.path = filepath.Join(f.dir, "*_err.log")
	other := filepath.Join(f.dir, "second_err.log")
	if err := os.WriteFile(other, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.run()
	f.append(logTestMatch)
	appendTo(t, other, logTestMatch+logTestMatch)
	res := f.run()
	if f.count(res) != 3 {
		t.Fatalf("two files = %+v, want 3 matches summed", res)
	}
	if files, _ := res.Data[DataKeyFiles].(int); files != 2 {
		t.Fatalf("two files = %+v, want files=2", res)
	}
	if err := os.Remove(other); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(6 * time.Minute)
	res = f.run()
	if files, _ := res.Data[DataKeyFiles].(int); files != 1 || f.count(res) != 0 {
		t.Fatalf("after one file vanished = %+v, want files=1 and 0 matches", res)
	}
	if err := os.WriteFile(other, []byte(logTestMatch), 0o600); err != nil {
		t.Fatal(err)
	}
	if res = f.run(); f.count(res) != 1 {
		t.Fatalf("a file that appears later = %+v, want its lines read from the start", res)
	}
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fh.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestLogMissingFileUnavailable(t *testing.T) {
	f := newLogFixture(t, "", ">", 0, 5*time.Minute)
	f.check.path = filepath.Join(f.dir, "absent.log")
	res := f.run()
	if res.OK || !res.Unavailable {
		t.Fatalf("missing file = %+v, want unavailable", res)
	}
}

func TestBuildLogCheck(t *testing.T) {
	dir := t.TempDir()
	good := map[string]any{
		"type": "log", "path": filepath.Join(dir, "*_err.log"), "regex": "cURL error 28",
		"count": map[string]any{"op": ">", "value": 3}, "within": "5m",
	}
	built, warns := Build(map[string]any{"l": good}, Deps{DefaultTimeout: time.Second})
	if len(warns) > 0 || len(built) != 1 {
		t.Fatalf("build = %d checks, warns %v", len(built), warns)
	}
	lc, ok := built[0].Check.(logCheck)
	if !ok {
		t.Fatalf("built %T, want logCheck", built[0].Check)
	}
	if lc.op != ">" || lc.value != 3 || lc.window != 5*time.Minute || lc.state == nil {
		t.Fatalf("built check = %+v", lc)
	}

	rejects := map[string]map[string]any{
		"relative path":  {"path": "logs/x.log", "regex": "a", "count": map[string]any{"op": ">", "value": 1}, "within": "1m"},
		"missing regex":  {"path": "/var/log/x.log", "count": map[string]any{"op": ">", "value": 1}, "within": "1m"},
		"invalid regex":  {"path": "/var/log/x.log", "regex": "(", "count": map[string]any{"op": ">", "value": 1}, "within": "1m"},
		"missing count":  {"path": "/var/log/x.log", "regex": "a", "within": "1m"},
		"bad op":         {"path": "/var/log/x.log", "regex": "a", "count": map[string]any{"op": "~", "value": 1}, "within": "1m"},
		"missing within": {"path": "/var/log/x.log", "regex": "a", "count": map[string]any{"op": ">", "value": 1}},
	}
	for name, entry := range rejects {
		t.Run(name, func(t *testing.T) {
			entry["type"] = "log"
			_, warns := Build(map[string]any{"l": entry}, Deps{DefaultTimeout: time.Second})
			if len(warns) == 0 {
				t.Fatal("expected a build warning")
			}
		})
	}
}
