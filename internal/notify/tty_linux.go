//go:build linux

package notify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/strutil"
	"sermo/internal/utmp"
)

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

const (
	defaultTTYHost = "localhost"
)

func buildTTY(name string, entry map[string]any) (Notifier, error) {
	return newTTY(name, TypeTTY, strutil.Set(cfgval.StringList(entry[KeyUsers]))), nil
}

func buildTargetedTTY(name string, users map[string]struct{}) (Notifier, error) {
	return newTTY(name, TypeTTY, users), nil
}

func buildWall(name string, _ map[string]any) (Notifier, error) {
	return newTTY(name, TypeWall, nil), nil
}

func newTTY(name, typ string, users map[string]struct{}) *ttyNotifier {
	return &ttyNotifier{
		name:      name,
		typ:       typ,
		users:     users,
		utmpPaths: utmp.DefaultPaths(),
		devRoot:   utmp.DevRoot,
		writeTTY:  writeTTYLinux,
		hostname:  os.Hostname,
		now:       time.Now,
	}
}

func (n *ttyNotifier) Name() string { return n.name }

func (n *ttyNotifier) Type() string { return n.typ }

func (n *ttyNotifier) Send(ctx context.Context, msg Message) error {
	sessions, err := utmp.SessionsFrom(n.utmpPaths)
	if err != nil {
		return fmt.Errorf("load terminal sessions: %w", err)
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
		host = defaultTTYHost
	}
	payload := ttyPayload(msg, host, n.now())
	var errs []error
	delivered := 0
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("check terminal delivery context: %w", err)
		}
		if err := n.writeTTY(ctx, target, payload); err != nil {
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

func (n *ttyNotifier) targetTTYs(sessions []utmp.Session) []string {
	var paths []string
	for _, s := range sessions {
		if len(n.users) > 0 {
			if _, ok := n.users[s.User]; !ok {
				continue
			}
		}
		path, ok := utmp.TTYPath(n.devRoot, s.Line)
		if !ok {
			continue
		}
		paths = append(paths, path)
	}
	return strutil.SortedUnique(paths)
}

func ttyPayload(msg Message, host string, at time.Time) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, notifyLF+"Message from Sermo on %s at %s"+notifyLF, terminalSafe(host), at.Format(time.RFC1123))
	if msg.Subject != "" {
		b.WriteString(notifyLF)
		b.WriteString(terminalSafe(msg.Subject))
		b.WriteString(notifyLF)
	}
	if msg.Body != "" {
		b.WriteString(notifyLF)
		b.WriteString(terminalSafe(msg.Body))
		if !strings.HasSuffix(msg.Body, notifyLF) {
			b.WriteString(notifyLF)
		}
	}
	return []byte(strings.ReplaceAll(b.String(), notifyLF, notifyCRLF))
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
		return fmt.Errorf("check terminal write context: %w", err)
	}
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NOCTTY|syscall.O_NONBLOCK|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open terminal %s: %w", path, err)
	}
	defer func() { _ = syscall.Close(fd) }()

	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return fmt.Errorf("inspect terminal %s: %w", path, err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFCHR {
		return errors.New("not a character device")
	}
	for len(payload) > 0 {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("check terminal write context: %w", err)
		}
		n, err := syscall.Write(fd, payload)
		if err != nil {
			return fmt.Errorf("write terminal %s: %w", path, err)
		}
		if n == 0 {
			return errors.New("short write to terminal")
		}
		payload = payload[n:]
	}
	return nil
}
