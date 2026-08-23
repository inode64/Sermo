package logind

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

type fakeSessionBus struct {
	session    session
	lookupErr  error
	terminate  string
	termErr    error
	closeCalls int
}

func (b *fakeSessionBus) SessionByPID(context.Context, int) (session, error) {
	return b.session, b.lookupErr
}
func (b *fakeSessionBus) TerminateSession(_ context.Context, id string) error {
	b.terminate = id
	return b.termErr
}
func (b *fakeSessionBus) Close() error { b.closeCalls++; return nil }

func testClient(bus *fakeSessionBus, ticks ...uint64) Client {
	index := 0
	return Client{
		connect: func(context.Context) (sessionBus, error) { return bus, nil },
		startTicks: func(int) (uint64, bool) {
			value := ticks[min(index, len(ticks)-1)]
			index++
			return value, true
		},
	}
}

func TestCloseRemoteSSHSessionTerminatesExactLogin(t *testing.T) {
	bus := &fakeSessionBus{session: session{ID: "c42", TTY: "pts/11", Service: sshdService, Leader: 96, Remote: true}}
	err := testClient(bus, 1234, 1234).CloseRemoteSSHSession(t.Context(), Target{PID: 96, StartTicks: 1234, Terminal: "pts/11"})
	if err != nil {
		t.Fatal(err)
	}
	if bus.terminate != "c42" || bus.closeCalls != 1 {
		t.Fatalf("terminated=%q close calls=%d", bus.terminate, bus.closeCalls)
	}
}

func TestCloseRemoteSSHSessionRejectsChangedIdentity(t *testing.T) {
	base := session{ID: "c42", TTY: "pts/11", Service: sshdService, Leader: 96, Remote: true}
	for _, tt := range []struct {
		name    string
		mutate  func(*session)
		ticks   []uint64
		wantErr string
	}{
		{name: "leader", mutate: func(s *session) { s.Leader = 97 }, ticks: []uint64{1234, 1234}, wantErr: "leader changed"},
		{name: "terminal", mutate: func(s *session) { s.TTY = "pts/12" }, ticks: []uint64{1234, 1234}, wantErr: "terminal changed"},
		{name: "local", mutate: func(s *session) { s.Remote = false }, ticks: []uint64{1234, 1234}, wantErr: "not remote"},
		{name: "service", mutate: func(s *session) { s.Service = "login" }, ticks: []uint64{1234, 1234}, wantErr: "not sshd"},
		{name: "generation before lookup", ticks: []uint64{9999}, wantErr: "generation changed"},
		{name: "generation after lookup", ticks: []uint64{1234, 9999}, wantErr: "generation changed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := base
			if tt.mutate != nil {
				tt.mutate(&got)
			}
			bus := &fakeSessionBus{session: got}
			err := testClient(bus, tt.ticks...).CloseRemoteSSHSession(t.Context(), Target{PID: 96, StartTicks: 1234, Terminal: "pts/11"})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
			if bus.terminate != "" {
				t.Fatalf("terminated %q after rejected identity", bus.terminate)
			}
		})
	}
}

func TestCloseRemoteSSHSessionReportsTerminateFailure(t *testing.T) {
	bus := &fakeSessionBus{
		session: session{ID: "c42", TTY: "pts/11", Service: sshdService, Leader: 96, Remote: true},
		termErr: errors.New("denied"),
	}
	err := testClient(bus, 1234, 1234).CloseRemoteSSHSession(t.Context(), Target{PID: 96, StartTicks: 1234, Terminal: "pts/11"})
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("error = %v", err)
	}
}

func TestSessionFromPropertiesRequiresTypedIdentity(t *testing.T) {
	properties := map[string]dbus.Variant{
		"Id": dbus.MakeVariant("c42"), "TTY": dbus.MakeVariant("pts/11"),
		"Service": dbus.MakeVariant(sshdService), "Leader": dbus.MakeVariant(uint32(96)),
		"Remote": dbus.MakeVariant(true),
	}
	got, err := sessionFromProperties(properties)
	if err != nil || got.ID != "c42" || got.Leader != 96 {
		t.Fatalf("session=%+v error=%v", got, err)
	}
	delete(properties, "Leader")
	if _, err := sessionFromProperties(properties); err == nil {
		t.Fatal("missing Leader property accepted")
	}
}
