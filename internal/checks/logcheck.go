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
// config reload or a daemon restart re-baselines. Matches are dated when read,
// rather than derived from a cumulative counter that needs an older baseline.
type logState struct {
	started bool
	files   map[string]*logFileState
	matches []logMatchBatch
}

type logMatchBatch struct {
	at      time.Time
	matched int
}

type logFileState struct {
	inode  uint64
	offset int64
}

func newLogState() *logState {
	return &logState{files: map[string]*logFileState{}}
}

// logTally is what one cycle read: matches found, bytes read and whether
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
	// Preserve matches already read even if a later file was unavailable. Their
	// offsets have advanced, so discarding this batch would lose those events.
	count := c.state.countWithin(windowClock(c.clock)(), tally.matched, c.window)
	if err != nil {
		return c.unavailableResult(fmt.Sprintf("log %s: %v", c.path, err), start)
	}
	ok := cfgval.CompareFloat(float64(count), c.op, c.value)

	msg := fmt.Sprintf("%d line(s) matching %s in %s across %d file(s) (want %s %s)",
		count, c.regex, c.window, tally.files, c.op, formatThreshold(c.value))
	if tally.truncated {
		msg += fmt.Sprintf("; read budget of %d bytes exceeded, count is a lower bound", c.readBudget())
	}
	res := c.result(ok, msg, start)
	res.Data = map[string]any{
		DataKeyPath:      c.path,
		DataKeyRegex:     c.regex.String(),
		DataKeyCount:     count,
		DataKeyValue:     count,
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

// countWithin retains batches by observation time, including the current batch
// even when the previous cycle is older than the whole window.
func (s *logState) countWithin(now time.Time, matched int, window time.Duration) int {
	s.matches = pruneWindow(s.matches, now.Add(-window), func(b logMatchBatch) time.Time { return b.at })
	if matched > 0 {
		s.matches = append(s.matches, logMatchBatch{at: now, matched: matched})
	}
	total := 0
	for _, batch := range s.matches {
		total += batch.matched
	}
	return total
}

// advance reads what every file gained since the previous cycle and counts its
// matches. Files that vanished are forgotten; a file
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
		if err := s.advanceFile(ctx, path, re, budget, &tally); err != nil {
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

func (s *logState) advanceFile(ctx context.Context, path string, re *regexp.Regexp, budget int64, tally *logTally) error {
	// A path (including a symlink target) can become a FIFO between cycles.
	// Open without waiting for a writer, then validate the opened descriptor.
	fh, err := hostfs.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = fh.Close() }()
	info, err := fh.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("log %s is not a regular file", path)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("log read cancelled: %w", err)
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
	read, err := countMatchingLines(ctx, fh, fs.offset, min(pending, remaining), re)
	if err != nil {
		return err
	}
	tally.matched += read.matched
	tally.bytesRead += read.bytesRead
	if pending > remaining {
		// The rest would not fit this cycle: skip it rather than fall behind
		// forever on a log that outruns the budget.
		tally.truncated = true
		fs.offset = info.Size()
		return nil
	}
	fs.offset += read.consumed
	return nil
}

// logRead separates actual I/O from the whole lines consumed. The budget must
// include a trailing partial line even though the offset cannot advance over it.
type logRead struct {
	bytesRead int64
	consumed  int64
	matched   int
}

// countMatchingLines reads up to limit bytes from offset and counts whole lines.
func countMatchingLines(ctx context.Context, r io.ReaderAt, offset, limit int64, re *regexp.Regexp) (logRead, error) {
	if err := ctx.Err(); err != nil {
		return logRead{}, fmt.Errorf("log read cancelled: %w", err)
	}
	buf := make([]byte, limit)
	n, err := r.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return logRead{}, fmt.Errorf("read: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return logRead{}, fmt.Errorf("log read cancelled: %w", err)
	}
	read := logRead{bytesRead: int64(n)}
	buf = buf[:n]
	end := bytes.LastIndexByte(buf, logNewline)
	if end < 0 {
		return read, nil
	}
	buf = buf[:end+1]
	for line := range bytes.SplitSeq(buf, []byte{logNewline}) {
		if err := ctx.Err(); err != nil {
			return logRead{}, fmt.Errorf("log read cancelled: %w", err)
		}
		if len(line) > 0 && re.Match(line) {
			read.matched++
		}
	}
	read.consumed = int64(len(buf))
	return read, nil
}

// fileInode identifies the file behind a path, so a rotated log (a new file
// under the old name) is told apart from the same file that grew.
func fileInode(info os.FileInfo) uint64 {
	if sys, ok := info.Sys().(*syscall.Stat_t); ok {
		return sys.Ino
	}
	return 0
}
