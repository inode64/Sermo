package cli

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"sermo/internal/config"
	"sermo/internal/hostfs"
	"sermo/internal/process"
	"sermo/internal/strutil"
)

func (a App) runServiceReload(ctx context.Context, opts options) int {
	if opts.service() == "" {
		return a.commandUsageError(commandReload, "reload requires a service name; use `sermoctl daemon reload` to reload sermod config")
	}
	return a.runAction(ctx, opts, commandReload)
}

// defaultReloadPidfileFallbacks are the absolute pidfiles `daemon reload` checks
// after the configured runtime dir. Keep this list restricted to current
// supported paths; old package locations are intentionally not searched.
func defaultReloadPidfileFallbacks() []string {
	return []string{filepath.Join(config.DefaultRuntime, config.DaemonPIDFilename)}
}

func daemonReloadPidfileCandidates(primary string, fallbacks []string) []string {
	return strutil.Unique(append([]string{primary}, fallbacks...))
}

// runReload asks the running sermod to reload its configuration (SIGHUP
// equivalent). It prefers a pidfile written by the daemon under the configured
// runtime dir. If no pidfile is found it falls back to a native /proc scan for
// a running sermod process. Only the sermod installed alongside this sermoctl
// and running as the caller's real UID is signalled. This works whether or not
// the web UI is enabled.
func (a App) runReload(ctx context.Context, opts options) int {
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}

	runtimeDir := cfg.Global.RuntimeDir()

	fallbacks := a.pidfileFallbacks
	if fallbacks == nil {
		fallbacks = defaultReloadPidfileFallbacks()
	}
	candidates := daemonReloadPidfileCandidates(filepath.Join(runtimeDir, config.DaemonPIDFilename), fallbacks)

	target, err := a.findDaemon(candidates)
	if err != nil {
		a.recordAccess(cfg, accessCommandDaemonReload, "", accessStatusError, err.Error())
		return a.fail(opts, err.Error())
	}
	pid := target.PID

	signal := a.signalDaemon
	if signal == nil {
		signal = process.OSSignaler{}.SignalProcess
	}
	// The pidfd signaler revalidates target's start time and exe, so PID reuse
	// between the identity check and delivery cannot redirect SIGHUP.
	if err := signal(ctx, target, syscall.SIGHUP); err != nil {
		a.recordAccess(cfg, accessCommandDaemonReload, "", accessStatusError, err.Error())
		return a.fail(opts, fmt.Sprintf("failed to signal pid %d: %v", pid, err))
	}

	a.recordAccess(cfg, accessCommandDaemonReload, "", accessStatusOK, fmt.Sprintf("pid %d", pid))
	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyOK: true, cliJSONKeyPID: pid})
	} else {
		fmt.Fprintf(a.Stdout, "reload signal (HUP) sent to sermod pid %d\n", pid)
	}
	return exitSuccess
}

// errDaemonNotFound reports that no pidfile or /proc entry names a sermod.
var errDaemonNotFound = errors.New("could not find running sermod pid (no pidfile naming a live sermod and no running sermod process)")

// errDaemonIdentity reports that the identity a reloaded sermod must have could
// not be derived, which refuses every candidate.
var errDaemonIdentity = errors.New("cannot identify the sermod to reload")

// errNotDaemon marks a PID whose executable is not named sermod at all: a
// recycled pidfile PID or a process renamed through prctl. It is skipped
// silently, unlike a sermod the expected identity rejects.
var errNotDaemon = errors.New("not a sermod process")

// daemonSbinDir is the sibling directory packages install sermod into when
// sermoctl lives in bin (bindir/sbindir under one exec prefix).
const daemonSbinDir = "sbin"

// rootUID is the superuser, which may reload a sermod running as any account.
const rootUID = 0

// daemonExpectation is the identity `daemon reload` signals: one of the sermod
// executables installed alongside this sermoctl, running as the caller's real
// UID, or as any account when the caller is root (a sermod run under a service
// account is still the installed binary). The executable basename alone proves
// nothing: any user can start a program called sermod.
type daemonExpectation struct {
	exes []string
	uid  uint32
}

// localDaemonExpectation derives the expected sermod from this sermoctl's own
// process identity, failing closed when it cannot be read. A sermoctl whose
// binary was replaced on disk still resolves through its previous path.
func localDaemonExpectation() (daemonExpectation, error) {
	self, ok := process.OSReader{}.Identity(os.Getpid())
	exe := identityExecutable(self)
	if !ok || exe == "" {
		return daemonExpectation{}, errors.New("cannot read this sermoctl's own executable")
	}
	exes := daemonExecutableCandidates(exe, filepath.EvalSymlinks)
	if len(exes) == 0 {
		return daemonExpectation{}, fmt.Errorf("no %s installed alongside %s", daemonProcessName, exe)
	}
	return daemonExpectation{exes: exes, uid: self.UID}, nil
}

// identityExecutable is the executable an identity runs: its resolved path, or
// for a binary replaced on disk the path it was started from.
func identityExecutable(id process.Identity) string {
	if id.ExeOK {
		return id.Exe
	}
	return id.ExePrev
}

// daemonExecutableCandidates lists the resolved sermod executables installed
// alongside the sermoctl at self: the sibling binary (a development build or a
// merged /usr) and ../sbin/sermod (bindir and sbindir under one prefix).
// Missing candidates are dropped; symlinks resolve so that a merged
// /usr/sbin -> /usr/bin names one file once.
func daemonExecutableCandidates(self string, resolve func(string) (string, error)) []string {
	bin := filepath.Dir(self)
	var out []string
	for _, candidate := range []string{
		filepath.Join(bin, daemonProcessName),
		filepath.Join(filepath.Dir(bin), daemonSbinDir, daemonProcessName),
	} {
		if resolved, err := resolve(candidate); err == nil {
			out = append(out, resolved)
		}
	}
	return strutil.Unique(out)
}

// findDaemon resolves the running sermod: pidfile candidates first, then a
// native /proc scan by program name. Every candidate PID must currently be the
// expected sermod. A pidfile left behind by a SIGKILLed or OOM-killed daemon can
// name a recycled PID, and SIGHUP terminates most programs, so an unverified
// pidfile PID is skipped instead of signalled. The expected identity is
// resolved once the first sermod-named process turns up, and failing to resolve
// it refuses every candidate. When only rejected sermod processes remain, the
// error names why each was refused.
func (a App) findDaemon(pidfiles []string) (process.Process, error) {
	identify := a.daemonIdentity
	if identify == nil {
		identify = process.OSReader{}.Identity
	}
	expect := a.expectedDaemon
	if expect == nil {
		expect = localDaemonExpectation
	}
	expected := sync.OnceValues(expect)
	var rejected []string
	for pid := range a.daemonCandidatePIDs(pidfiles) {
		target, err := daemonTarget(identify, expected, pid)
		switch {
		case err == nil:
			return target, nil
		case errors.Is(err, errDaemonIdentity):
			return process.Process{}, err
		case !errors.Is(err, errNotDaemon):
			rejected = append(rejected, err.Error())
		}
	}
	if len(rejected) > 0 {
		return process.Process{}, fmt.Errorf("could not find a trusted running sermod: %s", strings.Join(strutil.Unique(rejected), "; "))
	}
	return process.Process{}, errDaemonNotFound
}

// daemonCandidatePIDs yields the PIDs findDaemon checks, in order: the PID each
// readable pidfile names, then a running sermod found by program name. The
// fallback is a native /proc scan (process.PIDsByComm), not a pidof/pgrep
// shell-out: it reads the world-readable /proc/<pid>/comm, so it locates a
// root-owned daemon without external binaries, and it runs only when no
// pidfile PID was the daemon.
func (a App) daemonCandidatePIDs(pidfiles []string) iter.Seq[int] {
	return func(yield func(int) bool) {
		for _, p := range pidfiles {
			data, err := hostfs.ReadFile(p)
			if err != nil {
				continue
			}
			if n, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && n > 0 && !yield(n) {
				return
			}
		}
		find := a.FindPID
		if find == nil {
			find = process.PIDsByComm
		}
		pids, err := find(daemonProcessName)
		if err != nil {
			return
		}
		for _, pid := range pids {
			if pid > 0 && !yield(pid) {
				return
			}
		}
	}
}

// daemonTarget returns the signal target for pid when it is the expected
// sermod: a known start time, an executable (or, for a daemon whose binary a
// package upgrade replaced, its previous path) among the expected ones, and the
// caller's real UID. comm is writable by any process through prctl and the
// basename by anyone who can copy a file, so neither is proof. A replaced binary
// is still identified so the operator gets the signaler's refusal (a deleted
// exe cannot authorize a reload) instead of "no sermod running".
func daemonTarget(identify func(int) (process.Identity, bool), expect func() (daemonExpectation, error), pid int) (process.Process, error) {
	id, ok := identify(pid)
	if !ok || !id.StartTicksOK {
		return process.Process{}, errNotDaemon
	}
	exe := identityExecutable(id)
	if exe == "" || filepath.Base(exe) != daemonProcessName {
		return process.Process{}, errNotDaemon
	}
	expected, err := expect()
	if err != nil {
		return process.Process{}, fmt.Errorf("%w: %w", errDaemonIdentity, err)
	}
	if !slices.Contains(expected.exes, exe) {
		return process.Process{}, fmt.Errorf("pid %d runs %s, not the %s installed alongside this sermoctl (%s)",
			pid, exe, daemonProcessName, strings.Join(expected.exes, ", "))
	}
	if expected.uid != rootUID && id.UID != expected.uid {
		return process.Process{}, fmt.Errorf("pid %d runs as uid %d, not the caller's uid %d", pid, id.UID, expected.uid)
	}
	return process.Process{PID: pid, StartTicks: id.StartTicks, Exe: id.Exe, ExeOK: id.ExeOK, UID: id.UID}, nil
}
