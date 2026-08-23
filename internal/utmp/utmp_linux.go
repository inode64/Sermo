//go:build linux

package utmp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
)

// Linux utmp record layout (struct utmp): a fixed 384-byte record whose first
// uint16 is the entry type; USER_PROCESS (7) marks an interactive login. The
// ut_pid leader and the fixed-width NUL-padded terminal/user fields use the
// offsets below.
const (
	recordSize        = 384
	userProcess       = 7
	pidOffset         = 4
	pidSize           = 4
	lineOffset        = 8
	lineSize          = 32
	userOffset        = 44
	userSize          = 32
	hostOffset        = 76
	hostSize          = 256
	utmpRunPath       = "/run/utmp"
	utmpLegacyRunPath = "/var/run/utmp"
)

var nativeEndian = binary.NativeEndian

// defaultPaths are the usual utmp locations; /run/utmp is canonical, with the
// legacy /var/run/utmp kept as a fallback.
var defaultPaths = []string{utmpRunPath, utmpLegacyRunPath}

// Sessions returns the active login sessions from the default utmp file.
func Sessions() ([]Session, error) {
	return SessionsFrom(defaultPaths)
}

// DefaultPaths returns the usual utmp locations in lookup order.
func DefaultPaths() []string {
	return append([]string(nil), defaultPaths...)
}

// SessionsFrom reads the first readable utmp file in paths (default paths when
// empty) and returns its USER_PROCESS sessions. It errors only when no file is
// found or a present file cannot be read.
func SessionsFrom(paths []string) ([]Session, error) {
	if len(paths) == 0 {
		paths = defaultPaths
	}
	var missing []string
	for _, path := range paths {
		data, err := os.ReadFile(path) //nolint:gosec // G304: fixed utmp/wtmp paths from the Linux session APIs
		if errors.Is(err, os.ErrNotExist) {
			missing = append(missing, path)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read utmp %s: %w", path, err)
		}
		return parse(data), nil
	}
	return nil, fmt.Errorf("read utmp: no utmp file found (%s)", strings.Join(missing, ", "))
}

func parse(data []byte) []Session {
	var out []Session
	for len(data) >= recordSize {
		rec := data[:recordSize]
		data = data[recordSize:]
		if nativeEndian.Uint16(rec[:2]) != userProcess {
			continue
		}
		pidBits := nativeEndian.Uint32(rec[pidOffset : pidOffset+pidSize])
		pid := 0
		if pidBits <= math.MaxInt32 {
			pid = int(pidBits)
		}
		line := cString(rec[lineOffset : lineOffset+lineSize])
		user := cString(rec[userOffset : userOffset+userSize])
		host := cString(rec[hostOffset : hostOffset+hostSize])
		if line == "" || user == "" {
			continue
		}
		out = append(out, Session{PID: pid, User: user, Line: line, Host: host})
	}
	return out
}

func cString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}
