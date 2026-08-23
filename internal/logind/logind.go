// Package logind safely identifies and terminates exact systemd login sessions.
package logind

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/godbus/dbus/v5"

	"sermo/internal/process"
)

const (
	login1Destination      = "org.freedesktop.login1"
	login1ManagerPath      = dbus.ObjectPath("/org/freedesktop/login1")
	login1ManagerInterface = "org.freedesktop.login1.Manager"
	login1SessionInterface = "org.freedesktop.login1.Session"
	dbusPropertiesGetAll   = "org.freedesktop.DBus.Properties.GetAll"
	sshdService            = "sshd"
)

// Target is the utmp session leader and process generation displayed to the
// operator. It is evidence to revalidate, never authority to signal the PID.
type Target struct {
	PID        int
	StartTicks uint64
	Terminal   string
}

type session struct {
	ID      string
	TTY     string
	Service string
	Leader  int
	Remote  bool
}

type sessionBus interface {
	SessionByPID(ctx context.Context, pid int) (session, error)
	TerminateSession(ctx context.Context, id string) error
	Close() error
}

// Client closes a session only after checking its process generation and all
// safety-significant login1 properties immediately before termination.
type Client struct {
	connect    func(context.Context) (sessionBus, error)
	startTicks func(int) (uint64, bool)
}

// NewClient returns a native system-bus login1 client.
func NewClient() Client {
	return Client{connect: connectSystemBus, startTicks: process.StartTicks}
}

// CloseRemoteSSHSession asks login1 to terminate one exact remote sshd login.
func (c Client) CloseRemoteSSHSession(ctx context.Context, target Target) error {
	if target.PID <= 0 || target.StartTicks == 0 || target.Terminal == "" {
		return errors.New("invalid managed SSH session identity")
	}
	if err := verifyStartTicks(c.startTicks, target); err != nil {
		return err
	}
	bus, err := c.connect(ctx)
	if err != nil {
		return fmt.Errorf("connect to systemd-logind: %w", err)
	}
	defer func() { _ = bus.Close() }()

	got, err := bus.SessionByPID(ctx, target.PID)
	if err != nil {
		return fmt.Errorf("resolve login session for PID %d: %w", target.PID, err)
	}
	if err := verifySession(got, target); err != nil {
		return err
	}
	if err := verifyStartTicks(c.startTicks, target); err != nil {
		return err
	}
	if err := bus.TerminateSession(ctx, got.ID); err != nil {
		return fmt.Errorf("terminate login session %q: %w", got.ID, err)
	}
	return nil
}

func verifyStartTicks(read func(int) (uint64, bool), target Target) error {
	if read == nil {
		return errors.New("process generation verification is unavailable")
	}
	got, ok := read(target.PID)
	if !ok || got != target.StartTicks {
		return fmt.Errorf("PID %d process generation changed", target.PID)
	}
	return nil
}

func verifySession(got session, target Target) error {
	if got.ID == "" {
		return errors.New("login session has no stable ID")
	}
	if got.Leader != target.PID {
		return fmt.Errorf("login session leader changed from PID %d to %d", target.PID, got.Leader)
	}
	if got.TTY != target.Terminal {
		return fmt.Errorf("login session terminal changed from %q to %q", target.Terminal, got.TTY)
	}
	if !got.Remote {
		return errors.New("login session is not remote")
	}
	if got.Service != sshdService {
		return fmt.Errorf("login session service is %q, not sshd", got.Service)
	}
	return nil
}

type dbusSessionBus struct{ conn *dbus.Conn }

func connectSystemBus(ctx context.Context) (sessionBus, error) { //nolint:ireturn // the private bus interface keeps safety logic unit-testable without D-Bus
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("connect system bus: %w", err)
	}
	return dbusSessionBus{conn: conn}, nil
}

func (b dbusSessionBus) SessionByPID(ctx context.Context, pid int) (session, error) {
	if pid <= 0 || pid > math.MaxInt32 {
		return session{}, fmt.Errorf("invalid login session PID %d", pid)
	}
	dbusPID := uint32(pid)
	manager := b.conn.Object(login1Destination, login1ManagerPath)
	var path dbus.ObjectPath
	if err := manager.CallWithContext(ctx, login1ManagerInterface+".GetSessionByPID", 0, dbusPID).Store(&path); err != nil {
		return session{}, fmt.Errorf("call login1 GetSessionByPID: %w", err)
	}
	var properties map[string]dbus.Variant
	if err := b.conn.Object(login1Destination, path).CallWithContext(ctx, dbusPropertiesGetAll, 0, login1SessionInterface).Store(&properties); err != nil {
		return session{}, fmt.Errorf("read login1 session properties: %w", err)
	}
	return sessionFromProperties(properties)
}

func (b dbusSessionBus) TerminateSession(ctx context.Context, id string) error {
	if err := b.conn.Object(login1Destination, login1ManagerPath).
		CallWithContext(ctx, login1ManagerInterface+".TerminateSession", 0, id).Err; err != nil {
		return fmt.Errorf("call login1 TerminateSession: %w", err)
	}
	return nil
}

func (b dbusSessionBus) Close() error {
	if err := b.conn.Close(); err != nil {
		return fmt.Errorf("close system bus connection: %w", err)
	}
	return nil
}

func sessionFromProperties(properties map[string]dbus.Variant) (session, error) {
	var result session
	var ok bool
	if result.ID, ok = stringProperty(properties, "Id"); !ok {
		return session{}, errors.New("login session Id property is unavailable")
	}
	if result.TTY, ok = stringProperty(properties, "TTY"); !ok {
		return session{}, errors.New("login session TTY property is unavailable")
	}
	if result.Service, ok = stringProperty(properties, "Service"); !ok {
		return session{}, errors.New("login session Service property is unavailable")
	}
	leader, ok := uint32Property(properties, "Leader")
	if !ok || leader > math.MaxInt32 {
		return session{}, errors.New("login session Leader property is unavailable")
	}
	result.Leader = int(leader)
	if result.Remote, ok = boolProperty(properties, "Remote"); !ok {
		return session{}, errors.New("login session Remote property is unavailable")
	}
	return result, nil
}

func stringProperty(properties map[string]dbus.Variant, name string) (string, bool) {
	variant, ok := properties[name]
	if !ok {
		return "", false
	}
	value, ok := variant.Value().(string)
	return value, ok
}

func uint32Property(properties map[string]dbus.Variant, name string) (uint32, bool) {
	variant, ok := properties[name]
	if !ok {
		return 0, false
	}
	value, ok := variant.Value().(uint32)
	return value, ok
}

func boolProperty(properties map[string]dbus.Variant, name string) (bool, bool) {
	variant, ok := properties[name]
	if !ok {
		return false, false
	}
	value, ok := variant.Value().(bool)
	return value, ok
}
