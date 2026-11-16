package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"testing"
	"time"
)

func TestObserveCommandState(t *testing.T) {
	pattern := regexp.MustCompile(`(^|[[:space:]/])(emerge|ebuild|qmerge|glus(\.sh)?|update_vm\.sh|revdep-rebuild|apt|apt-get|dpkg|unattended-upgrade(s)?|dnf|yum|rpm|pacman|apk|zypper)([[:space:]]|$)`)
	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{name: "binary", argv: []string{"/usr/bin/apt-get", "upgrade"}, want: StateRunning},
		{name: "python script", argv: []string{"/usr/bin/python3", "/usr/bin/emerge", "world"}, want: StateRunning},
		{name: "shell script", argv: []string{"bash", "/opt/tools/update_vm.sh"}, want: StateRunning},
		{name: "glus", argv: []string{"/usr/bin/glus"}, want: StateRunning},
		{name: "glus shell", argv: []string{"bash", "/opt/tools/glus.sh"}, want: StateRunning},
		{name: "argument", argv: []string{"wrapper", "dnf"}, want: StateRunning},
		{name: "similar name", argv: []string{"/usr/bin/apt-get-helper"}, want: StateAbsent},
		{name: "suffix", argv: []string{"/usr/bin/myemerge"}, want: StateAbsent},
		{name: "empty argv", want: StateAbsent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Discoverer{Reader: fakeReader{ids: map[int]Identity{42: {PID: 42, State: "S", Cmdline: tc.argv}}}}
			if got := d.ObserveAnyState(t.Context(), nil, "", pattern).State; got != tc.want {
				t.Fatalf("state = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestObserveCommandUncertaintyAndFilters(t *testing.T) {
	pattern := regexp.MustCompile(`(^|/)emerge($| )`)
	for _, tc := range []struct {
		name string
		id   Identity
		exes []string
		user string
		want string
	}{
		{name: "unreadable", id: Identity{CmdlineUnreadable: true}, want: StateUnknown},
		{name: "unknown user", id: Identity{Cmdline: []string{"emerge"}}, user: "missing", want: StateUnknown},
		{name: "wrong user", id: Identity{UID: 100, CmdlineUnreadable: true}, user: "root", want: StateAbsent},
		{name: "wrong exe", id: Identity{Exe: "/bin/bash", ExeOK: true, Cmdline: []string{"emerge"}}, exes: []string{testExe}, want: StateAbsent},
		{name: "combined", id: Identity{Exe: testExe, ExeOK: true, Cmdline: []string{"emerge"}}, exes: []string{testExe}, user: "root", want: StateRunning},
		{name: "replaced exe with cmd only", id: Identity{ExePrev: testExe, Cmdline: []string{"emerge"}}, want: StateRunning},
		{name: "zombie empty argv", id: Identity{State: ProcStateZombie}, want: StateAbsent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Discoverer{Reader: fakeReader{ids: map[int]Identity{42: tc.id}}, ResolveUser: fakeUsers(map[string]uint32{"root": 0})}
			if got := d.ObserveAnyState(t.Context(), tc.exes, tc.user, pattern).State; got != tc.want {
				t.Fatalf("state = %s, want %s", got, tc.want)
			}
		})
	}
	d := Discoverer{Reader: snapshotErrReader{err: errors.New("partial table")}}
	if got := d.ObserveAnyState(t.Context(), nil, "", pattern).State; got != StateUnknown {
		t.Fatalf("partial table = %s", got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := d.ObserveAnyState(ctx, nil, "", pattern).State; got != StateUnknown {
		t.Fatalf("cancelled = %s", got)
	}
}

func TestReadCmdlineDistinguishesEmptyFromUnreadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cmdline")
	for _, data := range []string{"", "python3\x00/usr/bin/emerge\x00world\x00"} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		argv, unreadable := readCmdlineAt(path)
		if unreadable || (len(argv) == 0) != (data == "") {
			t.Fatalf("read %q = %v, unreadable %v", data, argv, unreadable)
		}
	}
	for _, unreadablePath := range []string{dir, filepath.Join(dir, "gone")} {
		if _, unreadable := readCmdlineAt(unreadablePath); !unreadable {
			t.Fatalf("%s read as an empty argv", unreadablePath)
		}
	}
}

type blockedCommandReader struct {
	closed chan struct{}
	calls  atomic.Int32
}

func (r *blockedCommandReader) PIDs() ([]int, error) {
	r.calls.Add(1)
	<-r.closed
	return nil, nil
}

func (*blockedCommandReader) Identity(int) (Identity, bool) { return Identity{}, false }

func TestCommandSnapshotTimeoutSharesBlockedScan(t *testing.T) {
	r := &blockedCommandReader{closed: make(chan struct{})}
	defer close(r.closed)
	d := Discoverer{Reader: NewCachingReader(r, time.Minute)}
	for range 2 {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		state := d.ObserveAnyState(ctx, nil, "", regexp.MustCompile("emerge")).State
		cancel()
		if state != StateUnknown {
			t.Fatalf("blocked scan = %s, want unknown", state)
		}
	}
	if got := r.calls.Load(); got != 1 {
		t.Fatalf("scans = %d, want one shared in-flight scan", got)
	}
}

type invalidatedCommandReader struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (r *invalidatedCommandReader) PIDs() ([]int, error) {
	if r.calls.Add(1) == 1 {
		close(r.started)
		<-r.release
		return nil, nil
	}
	return []int{42}, nil
}

func (*invalidatedCommandReader) Identity(pid int) (Identity, bool) {
	return Identity{PID: pid, Cmdline: []string{"emerge"}}, true
}

func TestCommandSnapshotInvalidationRejectsInFlightAbsence(t *testing.T) {
	r := &invalidatedCommandReader{started: make(chan struct{}), release: make(chan struct{})}
	cached := NewCachingReader(r, time.Hour)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	monitor := make(chan error, 1)
	go func() {
		_, err := cached.SnapshotContext(ctx)
		monitor <- err
	}()
	select {
	case <-r.started:
	case <-ctx.Done():
		t.Fatal("monitor scan never started")
	}
	invalidated := make(chan struct{})
	go func() { cached.Invalidate(); close(invalidated) }()
	select {
	case <-invalidated:
	case <-ctx.Done():
		close(r.release)
		t.Fatal("invalidation waited for a blocked scan")
	}
	operation := make(chan string, 1)
	go func() {
		d := Discoverer{Reader: cached}
		operation <- d.ObserveAnyState(ctx, nil, "", regexp.MustCompile("emerge")).State
	}()
	close(r.release)
	if state := <-operation; state != StateRunning {
		t.Fatalf("operation used stale absence: %s", state)
	}
	if err := <-monitor; err != nil {
		t.Fatalf("monitor snapshot: %v", err)
	}
	if r.calls.Load() != 2 {
		t.Fatalf("scans = %d, want one fresh scan after invalidation", r.calls.Load())
	}
}

// A guard on a package manager has to say which process held it. The reading
// names the oldest live match (a map snapshot must not make it flicker), the
// name the predicate matched instead of the interpreter, and none of the
// arguments, which may carry credentials into events and notifications.
func TestObserveCommandNamesOldestLiveMatch(t *testing.T) {
	pattern := regexp.MustCompile(`(^|[[:space:]/])(emerge|glus(\.sh)?)([[:space:]]|$)`)
	ids := map[int]Identity{
		1310048: {PID: 1310048, PPID: 1, StartTicks: 900, StartTicksOK: true, State: "S",
			Cmdline: []string{"/usr/bin/python3.13", "/usr/lib/python-exec/python3.13/emerge", "-vp", "--password=secret"}},
		77: {PID: 77, PPID: 1, StartTicks: 1200, StartTicksOK: true, State: "S", Cmdline: []string{"bash", "/opt/glus.sh"}},
		55: {PID: 55, PPID: 1, StartTicksOK: false, State: "S", Cmdline: []string{"emerge", "world"}},
		12: {PID: 12, PPID: 1, StartTicks: 100, StartTicksOK: true, State: ProcStateZombie, Cmdline: []string{"emerge"}},
	}
	d := Discoverer{Reader: fakeReader{ids: ids}}
	want := StateObservation{State: StateRunning, PID: 1310048, PPID: 1, Program: "emerge"}
	for range 20 {
		if got := d.ObserveAnyState(t.Context(), nil, "", pattern); got != want {
			t.Fatalf("observation = %+v, want %+v", got, want)
		}
	}
}

func TestObserveExecutableNamesProgram(t *testing.T) {
	ids := map[int]Identity{
		300: {PID: 300, PPID: 1, StartTicks: 5, StartTicksOK: true, Exe: testExe, ExeOK: true, State: "S", Cmdline: []string{testExe, "-psecret"}},
	}
	got := Discoverer{Reader: fakeReader{ids: ids}}.ObserveAnyState(t.Context(), []string{testExe}, "", nil)
	if want := (StateObservation{State: StateRunning, PID: 300, PPID: 1, Program: testExe}); got != want {
		t.Fatalf("observation = %+v, want %+v", got, want)
	}
}

// Only the predicate's own match is named, as a plain name: never an argument
// beside it or around it, and never text a notifier could read as markup.
func TestMatchedName(t *testing.T) {
	packages := `(^|[[:space:]/])(emerge|apt-get|dnf|glus(\.sh)?)([[:space:]]|$)`
	for _, tc := range []struct {
		name, pattern string
		argv          []string
		want          string
	}{
		{name: "script path", pattern: packages, argv: []string{"/usr/bin/python3.13", "/usr/lib/python-exec/python3.13/emerge", "-vp", "rspamd"}, want: "emerge"},
		{name: "after a separator", pattern: packages, argv: []string{"wrapper", "dnf", "upgrade"}, want: "dnf"},
		{name: "first word", pattern: packages, argv: []string{"apt-get", "-y", "upgrade"}, want: "apt-get"},
		{name: "shell script", pattern: packages, argv: []string{"bash", "/opt/tools/glus.sh"}, want: "glus.sh"},
		{name: "secret in the same word", pattern: `emerge`, argv: []string{"sh", "-c", "export TOKEN=abc;emerge -uDN @world"}, want: "emerge"},
		{name: "credentials in a url", pattern: packages, argv: []string{"curl", "-O", "https://user:tok@mirror/emerge"}, want: "emerge"},
		{name: "match spanning arguments", pattern: `mysqldump .*`, argv: []string{"mysqldump", "--password=secret", "db"}, want: ""},
		{name: "markup", pattern: `\S*emerge`, argv: []string{"<!channel>emerge"}, want: ""},
		{name: "no match", pattern: `zypper`, argv: []string{"emerge"}, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchedName(regexp.MustCompile(tc.pattern), Identity{Cmdline: tc.argv}); got != tc.want {
				t.Fatalf("MatchedName = %q, want %q", got, tc.want)
			}
		})
	}
	if got := MatchedName(nil, Identity{Cmdline: []string{"emerge"}}); got != "" {
		t.Fatalf("nil pattern = %q", got)
	}
	if got := MatchedName(regexp.MustCompile("emerge"), Identity{CmdlineUnreadable: true}); got != "" {
		t.Fatalf("unreadable argv = %q", got)
	}
}
