package servicemgr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"sermo/internal/hostfs"
)

const openRCMetadataLimit = 64 * 1024

var openRCCustomStatus = regexp.MustCompile(`(?m)^\s*(?:function\s+)?status\s*\(\s*\)`)

type openRCStatusFiles struct {
	read func(string) ([]byte, error)
	dirs func(string) ([]os.DirEntry, error)
	stat func(string) (os.FileInfo, error)
}

// ObserveOpenRCStatus reads OpenRC's state and registered daemon records. The
// caller supplies process observation so the shared process snapshot is reused.
// Custom status functions remain application-specific and are refused.
func ObserveOpenRCStatus(ctx context.Context, unit string, alive func(context.Context, ProcInfo) (bool, error)) (Status, error) {
	files := openRCStatusFiles{
		read: func(path string) ([]byte, error) {
			data, err := hostfs.ReadRegularFile(path, openRCMetadataLimit+1)
			if len(data) > openRCMetadataLimit {
				return nil, fmt.Errorf("OpenRC metadata %s exceeds size limit", path)
			}
			if err != nil {
				return nil, fmt.Errorf("read OpenRC metadata: %w", err)
			}
			return data, nil
		},
		dirs: hostfs.ReadDir, stat: hostfs.Lstat,
	}
	return files.status(ctx, unit, alive)
}

func (f openRCStatusFiles) status(ctx context.Context, unit string, alive func(context.Context, ProcInfo) (bool, error)) (Status, error) {
	if err := ctx.Err(); err != nil {
		return StatusUnknown, fmt.Errorf("observe OpenRC state: %w", err)
	}
	if !ValidOpenRCUnit(unit) {
		return StatusUnknown, invalidOpenRCUnit(unit)
	}
	script, err := f.read(filepath.Join(openRCInitDir, unit))
	if err != nil {
		return StatusUnknown, fmt.Errorf("read OpenRC service %s: %w", unit, err)
	}
	if openRCCustomStatus.Match(script) {
		return StatusUnknown, fmt.Errorf("OpenRC service %s has a custom status function", unit)
	}
	root, err := f.read(filepath.Join(openRCRuntimeDir, "options", unit, "chroot"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return StatusUnknown, fmt.Errorf("read OpenRC chroot: %w", err)
	}
	if value := strings.TrimSpace(string(root)); value != "" && value != "/" {
		return StatusUnknown, fmt.Errorf("OpenRC service %s uses a chroot; retain its status command", unit)
	}
	before, err := f.state(unit)
	if err != nil || before != StatusActive {
		return before, err
	}
	records, err := f.dirs(filepath.Join(openRCDaemonsDir, unit))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return StatusUnknown, fmt.Errorf("read OpenRC daemons: %w", err)
	}
	status := StatusActive
	for _, record := range records {
		if strings.HasPrefix(record.Name(), ".") {
			continue
		}
		if err := ctx.Err(); err != nil {
			return StatusUnknown, fmt.Errorf("observe OpenRC daemon: %w", err)
		}
		data, err := f.read(filepath.Join(openRCDaemonsDir, unit, record.Name()))
		if err != nil {
			return StatusUnknown, fmt.Errorf("read OpenRC daemon record: %w", err)
		}
		info, err := openRCRecordedDaemon(data)
		if err != nil {
			return StatusUnknown, err
		}
		running, err := alive(ctx, info)
		if err != nil {
			return StatusUnknown, err
		}
		if !running {
			status = StatusFailed
		}
	}
	after, err := f.state(unit)
	if err != nil {
		return StatusUnknown, err
	}
	if after != before {
		return StatusUnknown, fmt.Errorf("OpenRC service %s changed state during observation", unit)
	}
	if err := ctx.Err(); err != nil {
		return StatusUnknown, fmt.Errorf("observe OpenRC status: %w", err)
	}
	return status, nil
}

func (f openRCStatusFiles) state(unit string) (Status, error) {
	// An absent runtime root is not evidence that a unit is stopped.
	if _, err := f.stat(openRCRuntimeDir); err != nil {
		return StatusUnknown, fmt.Errorf("read OpenRC runtime: %w", err)
	}
	for _, entry := range []struct {
		dir    string
		status Status
	}{
		{"stopping", StatusUnknown}, {"starting", StatusUnknown}, {"inactive", StatusUnknown}, {"started", StatusActive},
	} {
		if _, err := f.stat(filepath.Join(openRCRuntimeDir, entry.dir, unit)); err == nil {
			if entry.status == StatusUnknown {
				return StatusUnknown, fmt.Errorf("OpenRC service %s is %s", unit, entry.dir)
			}
			return entry.status, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return StatusUnknown, fmt.Errorf("read OpenRC state: %w", err)
		}
	}
	return StatusInactive, nil
}

func openRCRecordedDaemon(data []byte) (ProcInfo, error) {
	values := make(map[string]string)
	for line := range strings.SplitSeq(string(data), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			values[key] = value
		}
	}
	info := ProcInfo{Pidfile: cleanProcPath(values[openRCVarPidfile])}
	if info.Pidfile != "" {
		return info, nil
	}
	var argv []string
	for i := 0; ; i++ {
		value, ok := values["argv_"+strconv.Itoa(i)]
		if !ok {
			break
		}
		argv = append(argv, value)
	}
	if len(argv) == 0 && values[openRCVarExec] != "" {
		argv = []string{values[openRCVarExec]}
	}
	if len(argv) == 0 {
		return ProcInfo{}, errors.New("OpenRC daemon record has no pidfile or command")
	}
	info.Cmd = "^" + regexp.QuoteMeta(strings.Join(argv, " ")) + `($|[[:space:]])`
	return info, nil
}
