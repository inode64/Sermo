package locks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const lockSuffix = ".lock"

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
	proc := s.Proc
	if proc == nil {
		proc = OSProcessProber{}
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}

	report := Report{Service: service}

	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return report, fmt.Errorf("read locks dir %s: %w", s.Dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), lockSuffix) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, fileName := range names {
		lockName, ok := matchService(fileName, service)
		if !ok {
			continue
		}
		path := filepath.Join(s.Dir, fileName)
		lf, err := readLockFile(path)
		if err != nil {
			report.Warnings = append(report.Warnings, err.Error())
			continue
		}

		state, staleReason := classify(lf, now(), proc)
		report.Locks = append(report.Locks, Lock{
			Service:         orDefault(lf.Service, service),
			Name:            orDefault(lf.Name, lockName),
			Reason:          lf.Reason,
			OwnerPID:        lf.OwnerPID,
			OwnerStartTicks: lf.OwnerStartTicks,
			CreatedAt:       lf.CreatedAt,
			ExpiresAt:       lf.ExpiresAt,
			Path:            path,
			State:           state,
			StaleReason:     staleReason,
		})
	}
	return report, nil
}

// ScanDir returns a warning for every lock file under Dir that cannot be read or
// parsed. A missing directory yields no warnings.
func (s Scanner) ScanDir() ([]string, error) {
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

	var warnings []string
	for _, fileName := range names {
		path := filepath.Join(s.Dir, fileName)
		if _, err := readLockFile(path); err != nil {
			warnings = append(warnings, err.Error())
		}
	}
	return warnings, nil
}

// matchService reports whether fileName is a lock for service, returning the
// derived lock name ("" for the bare <service>.lock). Naming is
// <service>[.<name>].lock (section 20).
func matchService(fileName, service string) (string, bool) {
	base := strings.TrimSuffix(fileName, lockSuffix)
	switch {
	case base == service:
		return "", true
	case strings.HasPrefix(base, service+"."):
		return base[len(service)+1:], true
	default:
		return "", false
	}
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

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
