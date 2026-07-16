package locks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const lockSuffix = ".lock"

// lockNameSep joins a service and a named-lock name into one filename. It is a
// backslash because validateIdentifier rejects '\' in both service and lock
// names, so the encoding is unambiguous (unlike a '.', which is legal in a
// service name and so could collide a named lock with a bare service lock).
const lockNameSep = "\\"

// Report is the result of scanning one service's named runtime locks.
type Report struct {
	Service  string
	Locks    []Lock
	Warnings []string // malformed lock files, reported but not fatal
}

// Scanner reads named runtime locks from a single locks directory
// (<paths.runtime>/locks).
type Scanner struct {
	Dir  string
	Proc ProcessProber
	Now  func() time.Time
}

// NewScanner returns a Scanner over dir using the real host for process probing
// and the wall clock.
func NewScanner(dir string) Scanner {
	return Scanner{Dir: dir, Proc: OSProcessProber{}, Now: time.Now}
}

// Scan returns every named runtime lock belonging to service, each classified
// as active, expired or stale. A missing directory yields an empty report (a
// host may simply have no locks); an unreadable directory is an error.
func (s Scanner) Scan(service string) (Report, error) {
	reports, err := s.ScanServices([]string{service})
	if err != nil {
		return Report{Service: service}, err
	}
	return reports[service], nil
}

// ScanServices returns named runtime locks for each requested service, reading
// the locks directory once. Reports are keyed by service and always include every
// requested service, even when it has no locks.
func (s Scanner) ScanServices(services []string) (map[string]Report, error) {
	proc, now := s.dependencies()
	reports := reportsForServices(services)

	names, err := s.lockFileNames()
	if err != nil {
		return reports, err
	}

	for _, fileName := range names {
		matches := lockServiceMatches(fileName, services)
		if len(matches) == 0 {
			continue
		}
		path := filepath.Join(s.Dir, fileName)
		lf, readErr := readLockFile(path)
		if readErr != nil {
			for _, match := range matches {
				report := reports[match.service]
				report.Warnings = append(report.Warnings, readErr.Error())
				reports[match.service] = report
			}
			continue
		}
		state, staleReason := classify(lf, now(), proc)

		for _, match := range matches {
			report := reports[match.service]
			report.Locks = append(report.Locks, scannedLock(lf, path, match, state, staleReason))
			reports[match.service] = report
		}
	}
	return reports, nil
}

func (s Scanner) dependencies() (ProcessProber, func() time.Time) {
	return procNowDefaults(s.Proc, s.Now)
}

func reportsForServices(services []string) map[string]Report {
	reports := make(map[string]Report, len(services))
	for _, service := range services {
		reports[service] = Report{Service: service}
	}
	return reports
}

func scannedLock(lf lockFile, path string, match lockServiceMatch, state State, staleReason string) Lock {
	return Lock{
		Service:         orDefault(lf.Service, match.service),
		Name:            orDefault(lf.Name, match.lockName),
		Reason:          lf.Reason,
		OwnerPID:        lf.OwnerPID,
		OwnerStartTicks: lf.OwnerStartTicks,
		CreatedAt:       lf.CreatedAt,
		ExpiresAt:       lf.ExpiresAt,
		Path:            path,
		State:           state,
		StaleReason:     staleReason,
	}
}

// ScanDir returns a warning for every lock file under Dir that cannot be read or
// parsed. A missing directory yields no warnings.
func (s Scanner) ScanDir() ([]string, error) {
	names, err := s.lockFileNames()
	if err != nil {
		return nil, err
	}

	var warnings []string
	for _, fileName := range names {
		path := filepath.Join(s.Dir, fileName)
		if _, err := readLockFile(path); err != nil {
			warnings = append(warnings, err.Error())
		}
	}
	return warnings, nil
}

func (s Scanner) lockFileNames() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read locks dir %s: %w", s.Dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), lockSuffix) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// matchService reports whether fileName is a lock for service, returning the
// derived lock name ("" for the bare <service>.lock). Naming is
// <service>[.<name>].lock.
func matchService(fileName, service string) (string, bool) {
	base := strings.TrimSuffix(fileName, lockSuffix)
	if base == service {
		return "", true
	}
	// Named locks are <service><sep><name>; neither segment can contain the
	// separator, so the first (only) split is unambiguous.
	if s, n, ok := strings.Cut(base, lockNameSep); ok && s == service && n != "" {
		return n, true
	}
	return "", false
}

type lockServiceMatch struct {
	service  string
	lockName string
}

func lockServiceMatches(fileName string, services []string) []lockServiceMatch {
	var matches []lockServiceMatch
	for _, service := range services {
		lockName, ok := matchService(fileName, service)
		if !ok {
			continue
		}
		matches = append(matches, lockServiceMatch{service: service, lockName: lockName})
	}
	return matches
}

func readLockFile(path string) (lockFile, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return lockFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	var lf lockFile
	if err := json.Unmarshal(data, &lf); err != nil {
		return lockFile{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return lf, nil
}

// isMissingLock reports a lock file that is not present. Use errors.Is rather
// than os.IsNotExist: readLockFile wraps I/O errors and os.IsNotExist does not
// see through fmt.Errorf wrapping.
func isMissingLock(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

// isRetryableLockRead reports a read/parse failure that may clear on retry.
// Another contender may have created the path (O_EXCL) but not finished writing.
func isRetryableLockRead(err error) bool {
	if isMissingLock(err) {
		return true
	}
	_, ok := errors.AsType[*json.SyntaxError](err)
	return ok
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
