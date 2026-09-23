package checks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/hostfs"
	"sermo/internal/metrics"
	"sermo/internal/units"
)

const (
	// logReadBudgetBytes caps what one cycle reads across the matched files, so a
	// log that explodes cannot stall the worker: the remainder is skipped, the
	// result says so, and the count is a lower bound for that cycle.
	logReadBudgetBytes = 8 * units.BytesPerMiB
	logGlobMeta        = "*?["
	logNewline         = '\n'
)

// logCheck is condition-style: OK means the number of lines appended to the
// matched files within the window that match the regex satisfies the
// predicate. It follows each file by offset like a tail: the first cycle only
// baselines at EOF (existing lines are history, not news), and a file that was
// rotated (new inode) or truncated (shrank) is read again from its start.
type logCheck struct {
	base
	path   string
	regex  *regexp.Regexp
	op     string
	value  float64
	window time.Duration
	budget int64
	clock  func() time.Time
	state  *logState
}

// logState is the per-check tail memory. It lives in the built check, so a
// config reload or a daemon restart re-baselines — exactly as count and size
// growth checks do (see counterWindow).
type logState struct {
	started bool
	files   map[string]*logFileState
	total   int
	window  counterWindow
}

type logFileState struct {
	inode  uint64
	offset int64
}

func newLogState() *logState {
	return &logState{files: map[string]*logFileState{}}
}

// logTally is what one cycle read: matches found, bytes consumed and whether
// the budget cut the read short.
type logTally struct {
	matched   int
	bytesRead int64
	truncated bool
	files     int
}

func (c logCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	paths, err := c.expand()
	if err != nil {
		return c.unavailableResult(fmt.Sprintf("log %s: %v", c.path, err), start)
	}
	if len(paths) == 0 {
		return c.unavailableResult("log: no file matches "+c.path, start)
	}

	tally, err := c.state.advance(ctx, paths, c.regex, c.readBudget())
	if err != nil {
		return c.unavailableResult(fmt.Sprintf("log %s: %v", c.path, err), start)
	}
	growth, span := c.state.window.advance(windowClock(c.clock)(), c.state.total, c.window)
	ok := cfgval.CompareFloat(float64(growth), c.op, c.value)

	msg := fmt.Sprintf("%d line(s) matching %s in %s across %d file(s) (want %s %s)",
		growth, c.regex, span.Round(time.Second), tally.files, c.op, formatThreshold(c.value))
	if tally.truncated {
		msg += fmt.Sprintf("; read budget of %d bytes exceeded, count is a lower bound", c.readBudget())
	}
	res := c.result(ok, msg, start)
	res.Data = map[string]any{
		DataKeyPath:      c.path,
		DataKeyRegex:     c.regex.String(),
		DataKeyCount:     growth,
		DataKeyValue:     growth,
		DataKeyUnit:      metrics.MetricUnitLines,
		DataKeyOp:        c.op,
		DataKeyThreshold: c.value,
		DataKeyWindow:    c.window.String(),
		DataKeyFiles:     tally.files,
		DataKeyBytesRead: tally.bytesRead,
		DataKeyTruncated: tally.truncated,
	}
	return res
}

func (c logCheck) readBudget() int64 {
	if c.budget > 0 {
		return c.budget
	}
	return logReadBudgetBytes
}

// expand resolves the configured path: a glob when it holds a metacharacter,
// otherwise the literal file. The list is sorted so state and results are
// stable across cycles.
func (c logCheck) expand() ([]string, error) {
	if !strings.ContainsAny(c.path, logGlobMeta) {
		return []string{c.path}, nil
	}
	paths, err := filepath.Glob(c.path)
	if err != nil {
		return nil, fmt.Errorf("glob: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}

// advance reads what every file gained since the previous cycle and adds the
// matches to the cumulative total. Files that vanished are forgotten; a file
// seen for the first time after the baseline is read from its start (a rotated
// log's successor is exactly that).
func (s *logState) advance(ctx context.Context, paths []string, re *regexp.Regexp, budget int64) (logTally, error) {
	seen := make(map[string]bool, len(paths))
	tally := logTally{files: len(paths)}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return tally, fmt.Errorf("log read cancelled: %w", err)
		}
		seen[path] = true
		if err := s.advanceFile(path, re, budget, &tally); err != nil {
			return tally, err
		}
	}
	for path := range s.files {
		if !seen[path] {
			delete(s.files, path)
		}
	}
	s.started = true
	return tally, nil
}

func (s *logState) advanceFile(path string, re *regexp.Regexp, budget int64, tally *logTally) error {
	fh, err := hostfs.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = fh.Close() }()
	info, err := fh.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	inode := fileInode(info)
	fs, known := s.files[path]
	switch {
	case !known && !s.started:
		// Baseline: what is already there is history.
		s.files[path] = &logFileState{inode: inode, offset: info.Size()}
		return nil
	case !known, fs.inode != inode, info.Size() < fs.offset:
		// New file, rotated (rename + create) or truncated: read from the start.
		fs = &logFileState{inode: inode}
		s.files[path] = fs
	}
	pending := info.Size() - fs.offset
	if pending <= 0 {
		return nil
	}
	remaining := budget - tally.bytesRead
	if remaining <= 0 {
		tally.truncated = true
		fs.offset = info.Size()
		return nil
	}
	consumed, matched, err := countMatchingLines(fh, fs.offset, min(pending, remaining), re)
	if err != nil {
		return err
	}
	tally.matched += matched
	tally.bytesRead += consumed
	s.total += matched
	if pending > remaining {
		// The rest would not fit this cycle: skip it rather than fall behind
		// forever on a log that outruns the budget.
		tally.truncated = true
		fs.offset = info.Size()
		return nil
	}
	fs.offset += consumed
	return nil
}

// countMatchingLines reads up to limit bytes from offset and counts the whole
// lines matching re. A trailing partial line is left for the next cycle:
// consumed is the number of bytes up to and including the last newline.
func countMatchingLines(r io.ReaderAt, offset, limit int64, re *regexp.Regexp) (consumed int64, matched int, err error) {
	buf := make([]byte, limit)
	n, err := r.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, 0, fmt.Errorf("read: %w", err)
	}
	buf = buf[:n]
	end := bytes.LastIndexByte(buf, logNewline)
	if end < 0 {
		return 0, 0, nil
	}
	buf = buf[:end+1]
	for line := range bytes.SplitSeq(buf, []byte{logNewline}) {
		if len(line) > 0 && re.Match(line) {
			matched++
		}
	}
	return int64(len(buf)), matched, nil
}

// fileInode identifies the file behind a path, so a rotated log (a new file
// under the old name) is told apart from the same file that grew.
func fileInode(info os.FileInfo) uint64 {
	if sys, ok := info.Sys().(*syscall.Stat_t); ok {
		return sys.Ino
	}
	return 0
}
