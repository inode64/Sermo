package checks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
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
	logNewline         = '\n'
	// maxLogCaptureLabels bounds the distinct captured values one message
	// lists; the line count still says how many matched.
	maxLogCaptureLabels = 5
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
	at       time.Time
	matched  int
	captures logCaptures
}

// logCaptures counts the matched lines per label, a label being the values
// the regex's named groups captured ("process=imap signal=6/ABRT"), in the
// order first seen. A regex without named groups captures nothing.
type logCaptures struct {
	order  []string
	counts map[string]int
}

func (c *logCaptures) add(label string, n int) {
	if label == "" || n == 0 {
		return
	}
	if c.counts == nil {
		c.counts = map[string]int{}
	}
	if _, seen := c.counts[label]; !seen {
		c.order = append(c.order, label)
	}
	c.counts[label] += n
}

func (c *logCaptures) merge(other logCaptures) {
	for _, label := range other.order {
		c.add(label, other.counts[label])
	}
}

// String lists the labels with their line counts, the first few only.
func (c *logCaptures) String() string {
	parts := make([]string, 0, min(len(c.order), maxLogCaptureLabels)+1)
	for _, label := range c.order[:min(len(c.order), maxLogCaptureLabels)] {
		parts = append(parts, fmt.Sprintf("%s (%d)", label, c.counts[label]))
	}
	if extra := len(c.order) - maxLogCaptureLabels; extra > 0 {
		parts = append(parts, fmt.Sprintf("%d more", extra))
	}
	return strings.Join(parts, ", ")
}

// captureLabel joins the non-empty named groups of one match, in regex order.
func captureLabel(re *regexp.Regexp, match [][]byte) string {
	var parts []string
	for i, name := range re.SubexpNames() {
		if name != "" && i < len(match) && len(match[i]) > 0 {
			parts = append(parts, name+"="+string(match[i]))
		}
	}
	return strings.Join(parts, " ")
}

func hasNamedGroups(re *regexp.Regexp) bool {
	return slices.ContainsFunc(re.SubexpNames(), func(name string) bool { return name != "" })
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
	captures  logCaptures
}

func (c logCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	paths, _, err := expandPathGlob(c.path)
	if err != nil {
		return c.unavailableResult(fmt.Sprintf("log %s: %v", c.path, err), start)
	}
	if len(paths) == 0 {
		return c.unavailableResult("log: no file matches "+c.path, start)
	}

	tally, err := c.state.advance(ctx, paths, c.regex, c.readBudget())
	// Preserve matches already read even if a later file was unavailable. Their
	// offsets have advanced, so discarding this batch would lose those events.
	count, captures := c.state.countWithin(windowClock(c.clock)(), tally, c.window)
	if err != nil {
		return c.unavailableResult(fmt.Sprintf("log %s: %v", c.path, err), start)
	}
	ok := cfgval.CompareFloat(float64(count), c.op, c.value)

	msg := fmt.Sprintf("%d line(s) matching %s in %s across %d file(s) (want %s %s)",
		count, c.regex, c.window, tally.files, c.op, formatThreshold(c.value))
	if len(captures.order) > 0 {
		msg += "; " + captures.String()
	}
	if tally.truncated {
		msg += fmt.Sprintf("; read budget of %d bytes exceeded, count is a lower bound", c.readBudget())
	}
	res := c.gradeValue(c.result(ok, msg, start), CheckKeyCount, float64(count))
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

// countWithin retains batches by observation time, including the current batch
// even when the previous cycle is older than the whole window, and sums their
// lines and captured labels.
func (s *logState) countWithin(now time.Time, tally logTally, window time.Duration) (int, logCaptures) {
	cutoff := now.Add(-window)
	s.matches = slices.DeleteFunc(s.matches, func(b logMatchBatch) bool { return b.at.Before(cutoff) })
	if tally.matched > 0 {
		s.matches = append(s.matches, logMatchBatch{at: now, matched: tally.matched, captures: tally.captures})
	}
	total := 0
	var captures logCaptures
	for _, batch := range s.matches {
		total += batch.matched
		captures.merge(batch.captures)
	}
	return total, captures
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
	tally.captures.merge(read.captures)
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
	captures  logCaptures
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
	named := hasNamedGroups(re)
	for line := range bytes.SplitSeq(buf, []byte{logNewline}) {
		if err := ctx.Err(); err != nil {
			return logRead{}, fmt.Errorf("log read cancelled: %w", err)
		}
		if len(line) > 0 && re.Match(line) {
			read.matched++
			// Submatches cost more than a match, and only matching lines pay it.
			if named {
				read.captures.add(captureLabel(re, re.FindSubmatch(line)), 1)
			}
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
