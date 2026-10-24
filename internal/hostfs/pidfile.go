package hostfs

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

const pidfileLineSeparator = "\n"

// ReadPidfile reads the first PID line from a pidfile. Most pidfiles contain
// only that line; PostgreSQL's postmaster.pid keeps the PID on line one and
// cluster metadata below it.
//
// A negative value is the process-group form some daemons write (DCC's
// dccifd, for example) so that a shutdown script can `kill -- -$(cat pidfile)`
// the whole group. A process group id is always its leader's pid, so the
// absolute value names that leader and discovery uses it like any other
// pidfile pid — it still has to be a live process to match, and the tree walk
// then picks up the rest of the group as children. `-1` and `-0` are refused:
// in kill semantics -1 means every process, so it identifies no service.
func ReadPidfile(path string) (int, error) {
	data, err := ReadRegularFile(filepath.Clean(path), MaxPidfileBytes)
	if err != nil {
		return 0, fmt.Errorf("read pidfile %s: %w", path, err)
	}
	text := strings.TrimSpace(string(data))
	line, _, _ := strings.Cut(text, pidfileLineSeparator)
	line = strings.TrimSpace(line)
	pid, err := strconv.Atoi(line)
	if err != nil {
		return 0, fmt.Errorf("invalid pid %q", line)
	}
	if pid < 0 {
		if pid >= -1 {
			return 0, fmt.Errorf("invalid process group %d", pid)
		}
		return -pid, nil
	}
	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid %d", pid)
	}
	return pid, nil
}
