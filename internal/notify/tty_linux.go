//go:build linux

package notify

import (
	"context"
	"errors"
	"fmt"
	"os"
	osuser "os/user"
	"slices"
	"strconv"
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
	writeTTY  func(context.Context, ttyTarget, []byte) error
	hostname  func() (string, error)
	now       func() time.Time
}

const (
	defaultTTYHost = "localhost"
)

// ttyTarget is one terminal to write. user is the utmp session's user when the
// notifier targets selected users, so the write can confirm the terminal still
// belongs to them; empty for wall and unfiltered tty.
type ttyTarget struct {
	path string
	user string
}

func (t ttyTarget) String() string { return t.path }

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

func (n *ttyNotifier) sendToTargets(ctx context.Context, targets []ttyTarget, msg Message) error {
	host, err := n.hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = defaultTTYHost
	}
	payload := ttyPayload(msg, host, n.now())
	var errs, skipped []error
	delivered := 0
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("check terminal delivery context: %w", err)
		}
		if err := n.writeTTY(ctx, target, payload); err != nil {
			if errors.Is(err, errTTYMessagesDisabled) || errors.Is(err, errTTYOtherOwner) {
				skipped = append(skipped, fmt.Errorf("%s: %w", target, err))
				continue
			}
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
	if delivered == 0 && len(skipped) > 0 {
		// Nobody saw the alert: record it rather than report a delivery.
		return fmt.Errorf("%s notifier reached no terminal: %w", n.Type(), errors.Join(skipped...))
	}
	return nil
}

func (n *ttyNotifier) targetTTYs(sessions []utmp.Session) []ttyTarget {
	var targets []ttyTarget
	seen := map[string]bool{}
	for _, s := range sessions {
		target := ttyTarget{}
		if len(n.users) > 0 {
			if _, ok := n.users[s.User]; !ok {
				continue
			}
			target.user = s.User
		}
		path, ok := utmp.TTYPath(n.devRoot, s.Line)
		if !ok || seen[path] {
			continue
		}
		seen[path] = true
		target.path = path
		targets = append(targets, target)
	}
	slices.SortFunc(targets, func(a, b ttyTarget) int { return strings.Compare(a.path, b.path) })
	return targets
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
		// C0, DEL and the C1 range: UTF-8 terminals (xterm and others) act on
		// C1 codes such as U+009B, the 8-bit CSI, just like an ESC sequence.
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return '?'
		}
		return r
	}, s)
}

// Skip reasons: not delivery failures, unless no terminal took the message.
var (
	// errTTYMessagesDisabled marks a terminal whose user refused messages.
	errTTYMessagesDisabled = errors.New("messages disabled (mesg n)")
	// errTTYOtherOwner marks a stale utmp session whose terminal was reused by
	// another user.
	errTTYOtherOwner = errors.New("terminal belongs to another user (stale session)")
)

// terminalOwnedBy confirms that a terminal owned by uid still belongs to the
// targeted user. A utmp record can outlive its session, and the pts number is
// then reused by someone else, who must not receive a message addressed to
// `users: [root]`. An owner whose name cannot be resolved is not treated as a
// mismatch, so users unknown to the local lookup keep receiving messages.
func terminalOwnedBy(uid uint32, user string, nameOf func(uint32) string) error {
	if user == "" {
		return nil
	}
	if owner := nameOf(uid); owner != "" && owner != user {
		return fmt.Errorf("%w: owned by %s, not %s", errTTYOtherOwner, owner, user)
	}
	return nil
}

// userNameByID resolves a uid to its user name, or "" when it is unknown.
func userNameByID(uid uint32) string {
	u, err := osuser.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return ""
	}
	return u.Username
}

// terminalAcceptsMessages checks the opened terminal's mode. `mesg n` clears
// the group-write bit; write(1) and wall(1) honour it through the kernel's
// permission check, but sermod runs as root and CAP_DAC_OVERRIDE bypasses that
// check, so the bit is tested explicitly.
func terminalAcceptsMessages(mode uint32) error {
	if mode&syscall.S_IFMT != syscall.S_IFCHR {
		return errors.New("not a character device")
	}
	if mode&syscall.S_IWGRP == 0 {
		return errTTYMessagesDisabled
	}
	return nil
}

func writeTTYLinux(ctx context.Context, target ttyTarget, payload []byte) error {
	path := target.path
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
	if err := terminalAcceptsMessages(st.Mode); err != nil {
		return err
	}
	if err := terminalOwnedBy(st.Uid, target.user, userNameByID); err != nil {
		return err
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
