package checks

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"sermo/internal/severity"
)

// pathGuardBudget bounds how long these tests wait for a check that must answer
// within milliseconds: a generous multiple, so a slow host cannot fail them.
const pathGuardBudget = 3 * time.Second

// hungFIFO returns a FIFO and a release function. Opening a FIFO blocks in the
// kernel until a writer appears, the shape of a read on a hung mount without
// needing one; the writer that release opens lets every blocked reader return.
func hungFIFO(t *testing.T) (string, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hung")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("FIFOs unsupported: %v", err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			if fh, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = fh.Close()
			}
		})
	}
	t.Cleanup(release)
	return path, release
}

func runWithin(t *testing.T, built []Built) Result {
	t.Helper()
	done := make(chan Result, 1)
	go func() { done <- Run(context.Background(), built, 0)[0] }()
	select {
	case res := <-done:
		return res
	case <-time.After(pathGuardBudget):
		t.Fatalf("the check did not answer within %s", pathGuardBudget)
		return Result{}
	}
}

// unguarded returns the check a path guard wraps.
func unguarded(check Check) Check {
	if guard, ok := check.(pathGuardCheck); ok {
		return guard.Check
	}
	return check
}

// blockedCheck stands in for a path check whose read does not return, with the
// state a real one keeps between cycles.
type blockedCheck struct {
	base
	read  func() // the injected read
	state *int
}

func (c blockedCheck) Run(context.Context) Result {
	start := time.Now()
	c.read()
	*c.state++ // unsynchronized on purpose: a stacked or late run shows under -race
	res := c.result(true, "read", start)
	res.Data = map[string]any{DataKeyValue: *c.state}
	return res
}

// waitPathRunDone returns once key has no run in flight: the goroutine that ran
// it removes the key as its last step, so no goroutine outlives the test.
func waitPathRunDone(t *testing.T, key string) {
	t.Helper()
	deadline := time.Now().Add(pathGuardBudget)
	for {
		if _, err := pathRuns.Do(key, time.Second, func() (Result, error) { return Result{}, nil }); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q is still in flight", key)
		}
		time.Sleep(time.Millisecond)
	}
}

// One hung read must cost its check the timeout, not the cycle: the check reports
// unavailable, frees its slot in the shared budget for other services' checks,
// and a later cycle fails at once instead of stacking another read. Once the read
// returns, its late result is dropped and the next cycle takes over the state.
func TestPathGuardAbandonsABlockedRun(t *testing.T) {
	const timeout = 20 * time.Millisecond
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	key := t.Name()
	t.Cleanup(func() {
		unblock()
		waitPathRunDone(t, key)
	})
	var reads atomic.Int32
	state := 0
	blocked := blockedCheck{
		name: "hung", timeout: timeout, severity: severity.Warning,
		state: &state,
		read: func() {
			reads.Add(1)
			<-release
		},
	}
	limiter := NewLimiter(1)
	built := []Built{{Check: withLimiter(withPathGuard(blocked, key, "/mnt/hung", timeout), limiter, timeout)}}

	first := runWithin(t, built)
	if first.Observation() != ObservationUnavailable || first.Check != "hung" || first.Severity != severity.Warning ||
		!strings.Contains(first.Message, "/mnt/hung") || !strings.Contains(first.Message, "no answer within 20ms") {
		t.Fatalf("first result = %+v, want unavailable naming the path and the timeout", first)
	}
	slot, cancel := context.WithTimeout(context.Background(), pathGuardBudget)
	defer cancel()
	if err := limiter.acquire(slot); err != nil {
		t.Fatalf("the slot of a hung check stayed taken: %v", err)
	}
	limiter.release()

	second := runWithin(t, built)
	if !strings.Contains(second.Message, "still blocked") || reads.Load() != 1 {
		t.Fatalf("second result = %+v after %d reads, want a fast failure without a new read", second, reads.Load())
	}

	unblock()
	waitPathRunDone(t, key)
	next := runWithin(t, built)
	if next.Observation() != ObservationHealthy || next.Data[DataKeyValue] != 2 || reads.Load() != 2 {
		t.Fatalf("next result = %+v after %d reads, want a fresh run over the late run's state", next, reads.Load())
	}
}

// A run that overruns its timeout but honors its context is slow, not hung: it
// keeps reporting its own deadline.
func TestPathGuardLeavesASlowRunItsOwnAnswer(t *testing.T) {
	const timeout = time.Millisecond
	state := 0
	slow := blockedCheck{name: "slow", timeout: timeout, state: &state, read: func() { time.Sleep(10 * timeout) }}
	res := withPathGuard(slow, t.Name(), "/mnt/slow", timeout).Run(context.Background())
	if res.Observation() != ObservationHealthy {
		t.Fatalf("result = %+v, want the run's own answer inside the grace", res)
	}
}

// A panic in a run on the guard's goroutine would otherwise end the daemon.
func TestPathGuardKeepsThePanicBarrier(t *testing.T) {
	state := 0
	panicking := blockedCheck{name: "panics", timeout: time.Second, state: &state, read: func() { panic("corrupt reply") }}
	res := withPathGuard(panicking, t.Name(), "/mnt/x", time.Second).Run(context.Background())
	if !res.Unavailable || !strings.Contains(res.Message, "corrupt reply") {
		t.Fatalf("result = %+v, want the panic reported as unavailable", res)
	}
}

// Callers sharing a run each decorate its result, so the run's map stays its own.
func TestPathGuardGivesEachCallerItsOwnData(t *testing.T) {
	shared := map[string]any{DataKeyValue: 1}
	res := withPathGuard(resultCheck{Result{Check: "c", OK: true, Data: shared}}, t.Name(), "/mnt/x", time.Second).Run(context.Background())
	res.Data[DataKeyPath] = "/mnt/x"
	if _, leaked := shared[DataKeyPath]; leaked {
		t.Fatal("a caller's data reached the shared run's map")
	}
}

type resultCheck struct{ result Result }

func (c resultCheck) Name() string               { return c.result.Check }
func (c resultCheck) Run(context.Context) Result { return c.result }

// Two checks of one path with different configurations must not share a run.
func TestPathRunKeySeparatesConfigurations(t *testing.T) {
	files := map[string]any{CheckKeyPath: "/srv/spool", CheckKeyOf: CountKindFile}
	dirs := map[string]any{CheckKeyPath: "/srv/spool", CheckKeyOf: CountKindDir}
	if pathRunKey("web", "spool", files) == pathRunKey("web", "spool", dirs) {
		t.Fatal("different configurations share a key")
	}
	if pathRunKey("web", "spool", files) == pathRunKey("api", "spool", files) {
		t.Fatal("different services share a key")
	}
	if pathRunKey("web", "spool", files) != pathRunKey("web", "spool", maps.Clone(files)) {
		t.Fatal("a rebuild of the same check must land on the same key, or a reload stacks a blocked run")
	}
}

// Every type that reads the paths its entry names runs under the guard, and only
// those: a sampler that never touches a path keeps its plain call.
func TestPathReadingChecksRunUnderTheGuard(t *testing.T) {
	limit := map[string]any{CheckKeyOp: ">", CheckKeyValue: 0}
	for _, tc := range []struct {
		name  string
		entry map[string]any
		want  bool
	}{
		{"file_exists", map[string]any{CheckKeyType: CheckTypeFileExists, CheckKeyPath: "/srv/a"}, true},
		{"file", map[string]any{CheckKeyType: CheckTypeFile, CheckKeyPath: "/srv/a"}, true},
		{"lockfile", map[string]any{CheckKeyType: CheckTypeLockfile, CheckKeyPath: "/srv/a"}, true},
		{"socket", map[string]any{CheckKeyType: CheckTypeSocket, CheckKeyPath: []any{"/srv/a", "/srv/b"}}, true},
		{"pidfile", map[string]any{CheckKeyType: CheckTypePidfile, CheckKeyPath: "/srv/a"}, true},
		{"binary", map[string]any{CheckKeyType: CheckTypeBinary, CheckKeyPath: "/srv/a"}, true},
		{"count", map[string]any{CheckKeyType: CheckTypeCount, CheckKeyPath: "/srv/a", CheckKeyCount: limit}, true},
		{"log", map[string]any{CheckKeyType: CheckTypeLog, CheckKeyPath: "/srv/a", CheckKeyRegex: "x", CheckKeyCount: limit, CheckKeyWithin: "1m"}, true},
		{"size", map[string]any{CheckKeyType: CheckTypeSize, CheckKeyPath: "/srv/a", CheckKeyMax: "1G"}, true},
		{"cert file", map[string]any{CheckKeyType: CheckTypeCert, CheckKeyPath: "/srv/a"}, true},
		{"config paths", map[string]any{CheckKeyType: CheckTypeConfig, CheckKeyPath: "/srv/a", CheckKeyOnChange: true}, true},
		{"cert endpoint", map[string]any{CheckKeyType: CheckTypeCert, CheckKeyHost: "host-a"}, false},
		{"config command", map[string]any{CheckKeyType: CheckTypeConfig, CheckKeyCommand: []any{"/bin/true"}}, false},
		{"load", map[string]any{CheckKeyType: CheckTypeLoad, fieldLoad1: limit}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			built, issues := BuildWithIssues(map[string]any{"c": tc.entry}, Deps{DefaultTimeout: time.Second})
			if len(issues) != 0 {
				t.Fatalf("issues = %v", issues)
			}
			if _, guarded := built[0].Check.(pathGuardCheck); guarded != tc.want {
				t.Fatalf("built %T, guarded = %v, want %v", built[0].Check, guarded, tc.want)
			}
		})
	}
}

// A real read the kernel blocks, through the check as the daemon builds it.
func TestCertFileAnswersAtItsTimeoutOnAHungRead(t *testing.T) {
	path, release := hungFIFO(t)
	entry := map[string]any{CheckKeyType: CheckTypeCert, CheckKeyPath: path, CheckKeyTimeout: "20ms"}
	built, issues := BuildWithIssues(map[string]any{"hung": entry}, Deps{Service: t.Name(), Limiter: NewLimiter(1)})
	if len(issues) != 0 {
		t.Fatalf("issues = %v", issues)
	}
	res := runWithin(t, built)
	if res.Observation() != ObservationUnavailable || !strings.Contains(res.Message, "no answer within 20ms") || !strings.Contains(res.Message, path) {
		t.Fatalf("result = %+v, want unavailable naming the path and the timeout", res)
	}
	release()
	waitPathRunDone(t, pathRunKey(t.Name(), "hung", entry))
}
