//go:build linux

package notify

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"sermo/internal/cfgval"
)

const (
	linuxUtmpRecordSize = 384
	linuxUserProcess    = 7
	utmpLineOffset      = 8
	utmpLineSize        = 32
	utmpUserOffset      = 44
	utmpUserSize        = 32
)

var nativeEndian = binary.NativeEndian

type ttySession struct {
	User string
	Line string
}

type ttyNotifier struct {
	name      string
	typ       string
	users     map[string]struct{}
	utmpPaths []string
	devRoot   string
	writeTTY  func(context.Context, string, []byte) error
	hostname  func() (string, error)
	now       func() time.Time
}

func buildTTY(name string, entry map[string]any) (Notifier, error) {
	return &ttyNotifier{
		name:      name,
		typ:       "tty",
		users:     stringSet(cfgval.StringList(entry["users"])),
		utmpPaths: []string{"/run/utmp", "/var/run/utmp"},
		devRoot:   "/dev",
		writeTTY:  writeTTYLinux,
		hostname:  os.Hostname,
		now:       time.Now,
	}, nil
}

func buildWall(name string, entry map[string]any) (Notifier, error) {
	return &ttyNotifier{
		name:      name,
		typ:       "wall",
		utmpPaths: []string{"/run/utmp", "/var/run/utmp"},
		devRoot:   "/dev",
		writeTTY:  writeTTYLinux,
		hostname:  os.Hostname,
		now:       time.Now,
	}, nil
}

func (n *ttyNotifier) Name() string { return n.name }

func (n *ttyNotifier) Type() string {
	if n.typ == "" {
		return "tty"
	}
	return n.typ
}

func (n *ttyNotifier) Send(ctx context.Context, msg Message) error {
	sessions, err := readUtmpSessions(n.utmpPaths)
	if err != nil {
		return err
	}
	targets := n.targetTTYs(sessions)
	if len(targets) == 0 {
		return fmt.Errorf("%s notifier found no active terminal sessions", n.Type())
	}
	return n.sendToTargets(ctx, targets, msg)
}

func (n *ttyNotifier) sendToTargets(ctx context.Context, targets []string, msg Message) error {
	host, err := n.hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "localhost"
	}
	now := time.Now
	if n.now != nil {
		now = n.now
	}
	payload := ttyPayload(msg, host, now())
	writeTTY := n.writeTTY
	if writeTTY == nil {
		writeTTY = writeTTYLinux
	}
	var errs []error
	delivered := 0
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeTTY(ctx, target, payload); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", target, err))
			continue
		}
		delivered++
	}
	if len(errs) > 0 {
		err := errors.Join(errs...)
		if delivered > 0 {
			return fmt.Errorf("%s notifier delivered to %d terminal(s), failed on %d: %w", n.Type(), delivered, len(errs), err)
		}
		return err
	}
	return nil
}

func (n *ttyNotifier) targetTTYs(sessions []ttySession) []string {
	devRoot := n.devRoot
	if devRoot == "" {
		devRoot = "/dev"
	}
	seen := map[string]struct{}{}
	var out []string
	for _, s := range sessions {
		if len(n.users) > 0 {
			if _, ok := n.users[s.User]; !ok {
				continue
			}
		}
		path, ok := ttyPath(devRoot, s.Line)
		if !ok {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	slices.Sort(out)
	return out
}

func readUtmpSessions(paths []string) ([]ttySession, error) {
	if len(paths) == 0 {
		paths = []string{"/run/utmp", "/var/run/utmp"}
	}
	var missing []string
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			missing = append(missing, path)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read utmp %s: %w", path, err)
		}
		return parseUtmp(data), nil
	}
	return nil, fmt.Errorf("read utmp: no utmp file found (%s)", strings.Join(missing, ", "))
}

func parseUtmp(data []byte) []ttySession {
	var out []ttySession
	for len(data) >= linuxUtmpRecordSize {
		rec := data[:linuxUtmpRecordSize]
		data = data[linuxUtmpRecordSize:]
		if nativeEndian.Uint16(rec[:2]) != linuxUserProcess {
			continue
		}
		line := cString(rec[utmpLineOffset : utmpLineOffset+utmpLineSize])
		user := cString(rec[utmpUserOffset : utmpUserOffset+utmpUserSize])
		if line == "" || user == "" {
			continue
		}
		out = append(out, ttySession{User: user, Line: line})
	}
	return out
}

func cString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}

func ttyPath(devRoot, line string) (string, bool) {
	if strings.ContainsRune(line, 0) || filepath.IsAbs(line) {
		return "", false
	}
	clean := filepath.Clean(line)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	root, err := filepath.Abs(devRoot)
	if err != nil {
		return "", false
	}
	path := filepath.Join(root, clean)
	if path != root && strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return path, true
	}
	return "", false
}

func ttyPayload(msg Message, host string, at time.Time) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "\nMessage from Sermo on %s at %s\n", terminalSafe(host), at.Format(time.RFC1123))
	if msg.Subject != "" {
		b.WriteString("\n")
		b.WriteString(terminalSafe(msg.Subject))
		b.WriteString("\n")
	}
	if msg.Body != "" {
		b.WriteString("\n")
		b.WriteString(terminalSafe(msg.Body))
		if !strings.HasSuffix(msg.Body, "\n") {
			b.WriteString("\n")
		}
	}
	return []byte(strings.ReplaceAll(b.String(), "\n", "\r\n"))
}

func terminalSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return r
		}
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

func writeTTYLinux(ctx context.Context, path string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NOCTTY|syscall.O_NONBLOCK|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = syscall.Close(fd) }()

	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFCHR {
		return errors.New("not a character device")
	}
	for len(payload) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := syscall.Write(fd, payload)
		if err != nil {
			return err
		}
		if n == 0 {
			return errors.New("short write to terminal")
		}
		payload = payload[n:]
	}
	return nil
}

func stringSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}
