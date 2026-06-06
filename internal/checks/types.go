package checks

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/servicemgr"
)

// tcpCheck dials a TCP host:port (section 12).
type tcpCheck struct {
	base
	host string
	port int
}

func (c tcpCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	addr := net.JoinHostPort(c.host, strconv.Itoa(c.port))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return c.result(false, fmt.Sprintf("dial %s: %v", addr, err), start)
	}
	_ = conn.Close()
	return c.result(true, "connected to "+addr, start)
}

// httpCheck issues an HTTP request and compares the status code (section 12).
// expect accepts a single code, a class (2xx) or a list (post-MVP).
type httpCheck struct {
	base
	client *http.Client
	url    string
	method string
	expect statusMatcher
}

func (c httpCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, c.method, c.url, nil)
	if err != nil {
		return c.result(false, fmt.Sprintf("build request: %v", err), start)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return c.result(false, fmt.Sprintf("%s %s: %v", c.method, c.url, err), start)
	}
	defer resp.Body.Close()

	ok := c.expect.matches(resp.StatusCode)
	return c.result(ok, fmt.Sprintf("status %d (want %s)", resp.StatusCode, c.expect), start)
}

// statusMatcher matches an HTTP status against exact codes and/or classes (the
// leading digit of an Nxx pattern).
type statusMatcher struct {
	codes   []int
	classes []int
}

func (m statusMatcher) matches(code int) bool {
	for _, c := range m.codes {
		if c == code {
			return true
		}
	}
	for _, cl := range m.classes {
		if code/100 == cl {
			return true
		}
	}
	return false
}

func (m statusMatcher) String() string {
	parts := make([]string, 0, len(m.codes)+len(m.classes))
	for _, c := range m.codes {
		parts = append(parts, strconv.Itoa(c))
	}
	for _, cl := range m.classes {
		parts = append(parts, strconv.Itoa(cl)+"xx")
	}
	return strings.Join(parts, ",")
}

// commandCheck runs a command and compares its exit code (section 12). The
// command is always an argv array, never a shell string (section 30/34).
type commandCheck struct {
	base
	runner     execx.Runner
	argv       []string
	expectExit int
}

func (c commandCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	res, _ := c.runner.Run(ctx, c.argv[0], c.argv[1:]...)
	ok := res.ExitCode == c.expectExit
	msg := fmt.Sprintf("exit %d (want %d)", res.ExitCode, c.expectExit)
	if !ok {
		if stderr := firstLine(res.Stderr); stderr != "" {
			msg += ": " + stderr
		}
	}
	return c.result(ok, msg, start)
}

// serviceCheck compares the service's backend status to an expected value
// (section 12). The status function is injected so the check stays single-shot.
type serviceCheck struct {
	base
	expect string
	status func(context.Context) (servicemgr.Status, error)
}

func (c serviceCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	status, err := c.status(ctx)
	if err != nil {
		return c.result(false, fmt.Sprintf("status: %v", err), start)
	}
	ok := string(status) == c.expect
	return c.result(ok, fmt.Sprintf("status %s (want %s)", status, c.expect), start)
}

// fileExistsCheck passes when a path exists (section 12). It must point at a
// foreign flag/lock file, never Sermo's own runtime locks (enforced in §30).
type fileExistsCheck struct {
	base
	path string
}

func (c fileExistsCheck) Run(_ context.Context) Result {
	start := time.Now()
	if _, err := os.Stat(c.path); err != nil {
		if os.IsNotExist(err) {
			return c.result(false, c.path+" does not exist", start)
		}
		return c.result(false, fmt.Sprintf("stat %s: %v", c.path, err), start)
	}
	return c.result(true, c.path+" exists", start)
}

// binaryCheck passes when a path exists and is an executable file (section 19).
type binaryCheck struct {
	base
	path string
}

func (c binaryCheck) Run(_ context.Context) Result {
	start := time.Now()
	info, err := os.Stat(c.path)
	if err != nil {
		return c.result(false, c.path+" not found", start)
	}
	if info.IsDir() {
		return c.result(false, c.path+" is a directory", start)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return c.result(false, c.path+" is not executable", start)
	}
	return c.result(true, c.path+" is executable", start)
}

// metricCheck reads a sampled metric and compares it to a threshold (section
// 12/14). Its OK is the comparison result (the threshold being met), so
// `active: {check: ...}` is true when the threshold is breached.
type metricCheck struct {
	base
	scope  string
	metric string
	op     string
	value  string
	source MetricReader
}

func (c metricCheck) Run(_ context.Context) Result {
	start := time.Now()
	if c.source == nil {
		return c.result(false, "metric source unavailable", start)
	}
	reading, ok := c.source(c.scope, c.metric)
	if !ok {
		return c.result(false, fmt.Sprintf("metric %s/%s unavailable", c.scope, c.metric), start)
	}
	met, err := metrics.Compare(reading, c.op, c.value)
	if err != nil {
		return c.result(false, err.Error(), start)
	}
	if !reading.Ready {
		return c.result(false, fmt.Sprintf("%s/%s not ready", c.scope, c.metric), start)
	}
	return c.result(met, fmt.Sprintf("%s/%s %s %s = %t", c.scope, c.metric, c.op, c.value, met), start)
}

// processCheck passes when the observed state of processes matching its
// exe/user selector equals the expected state (section 12). Matching uses the
// exact resolved-exe and real-UID rules of section 21.
type processCheck struct {
	base
	exe     string
	user    string
	expect  string
	observe func(exe, user string) string
}

func (c processCheck) Run(_ context.Context) Result {
	start := time.Now()
	if c.observe == nil {
		return c.result(false, "process discovery unavailable", start)
	}
	state := c.observe(c.exe, c.user)
	ok := state == c.expect
	return c.result(ok, fmt.Sprintf("state %s (want %s)", state, c.expect), start)
}

// librariesCheck runs ldd on a binary and fails if any shared library does not
// resolve (section 19). It is typically an optional preflight entry.
type librariesCheck struct {
	base
	runner execx.Runner
	binary string
}

func (c librariesCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	res, _ := c.runner.Run(ctx, "ldd", c.binary)
	out := res.Stdout + res.Stderr
	if strings.Contains(out, "not found") {
		return c.result(false, c.binary+": missing shared libraries", start)
	}
	if strings.Contains(out, "not a dynamic executable") {
		return c.result(true, c.binary+": static binary, no shared libraries", start)
	}
	if res.ExitCode != 0 {
		msg := firstLine(res.Stderr)
		if msg == "" {
			msg = fmt.Sprintf("ldd exit %d", res.ExitCode)
		}
		return c.result(false, "ldd "+c.binary+": "+msg, start)
	}
	return c.result(true, c.binary+": all shared libraries resolve", start)
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
