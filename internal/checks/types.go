package checks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"sermo/internal/conn"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/servicemgr"
)

// tcpCheck dials a TCP host:port (section 12), optionally egressing through one
// or more interfaces (ifaces); ifaceAll requires every one to succeed.
type tcpCheck struct {
	base
	host     string
	ifaces   []string
	ifaceAll bool
	port     int
}

func (c tcpCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	addr := net.JoinHostPort(c.host, strconv.Itoa(c.port))
	chosen, perIface, err := tryInterfaces(c.ifaces, c.ifaceAll, func(iface string) error {
		nc, e := conn.BindDialer(iface).DialContext(ctx, "tcp", addr)
		if e == nil {
			_ = nc.Close()
		}
		return e
	})
	if err != nil {
		r := c.result(false, fmt.Sprintf("dial %s: %v", addr, err), start)
		r.Data = ifaceData(perIface)
		return r
	}
	r := c.result(true, "connected to "+addr+ifaceSuffix(chosen), start)
	r.Data = ifaceData(perIface)
	return r
}

// httpCheck issues an HTTP request and asserts the response: the status code
// (expect), optionally that the body contains a substring (expectBody) and that
// the JSON response matches key/value pairs at dotted paths (expectJSON). The
// request may carry custom headers and a raw or JSON body (section 12).
type httpCheck struct {
	base
	client       *http.Client
	url          string
	method       string
	headers      map[string]string
	body         []byte
	contentType  string // set when the body is JSON, unless headers override it
	expect       statusMatcher
	expectBody   string // substring the body must contain
	bodyOp       string // when set, compare the (trimmed) body via compareValue
	bodyValue    string
	expectJSON   []jsonAssertion
	latencyOp    string // when set, compare the response latency in ms
	latencyValue string

	// Certificate inspection (https only). certHost is non-empty when any cert_*
	// option is configured; certClient is then an InsecureSkipVerify client so
	// the leaf can be read even when expired or otherwise invalid.
	certHost   string
	certClient *http.Client
	certOpts   certOptions
	certEval   certEvaluator
}

// jsonAssertion is one response-JSON check: the value at a dotted path compared to
// value with op (== by default; also != > >= < <= contains).
type jsonAssertion struct {
	path  string
	op    string
	value string
}

// maxHTTPBody bounds how much of the response is read for body/JSON assertions.
const maxHTTPBody = 1 << 20

func (c *httpCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	client := c.client
	if c.certHost != "" {
		client = c.certClient
	}

	var body io.Reader
	if len(c.body) > 0 {
		body = bytes.NewReader(c.body)
	}
	req, err := http.NewRequestWithContext(ctx, c.method, c.url, body)
	if err != nil {
		return c.result(false, fmt.Sprintf("build request: %v", err), start)
	}
	if c.contentType != "" {
		req.Header.Set("Content-Type", c.contentType)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return c.result(false, fmt.Sprintf("%s %s: %v", c.method, c.url, err), start)
	}
	defer resp.Body.Close()

	if !c.expect.matches(resp.StatusCode) {
		return c.result(false, fmt.Sprintf("status %d (want %s)", resp.StatusCode, c.expect), start)
	}
	if c.latencyOp != "" {
		ms := strconv.FormatInt(elapsed.Milliseconds(), 10)
		ok, err := compareValue(ms, c.latencyOp, c.latencyValue)
		if err != nil {
			return c.result(false, fmt.Sprintf("latency: %v", err), start)
		}
		if !ok {
			return c.result(false, fmt.Sprintf("status %d; latency %sms not %s %s", resp.StatusCode, ms, c.latencyOp, c.latencyValue), start)
		}
	}
	if c.expectBody == "" && c.bodyOp == "" && len(c.expectJSON) == 0 {
		return c.success(resp, elapsed, fmt.Sprintf("status %d", resp.StatusCode), start)
	}

	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody))
	if c.expectBody != "" && !strings.Contains(string(data), c.expectBody) {
		return c.result(false, fmt.Sprintf("status %d; body does not contain %q", resp.StatusCode, c.expectBody), start)
	}
	if c.bodyOp != "" {
		ok, err := compareValue(strings.TrimSpace(string(data)), c.bodyOp, c.bodyValue)
		if err != nil {
			return c.result(false, fmt.Sprintf("status %d; body: %v", resp.StatusCode, err), start)
		}
		if !ok {
			return c.result(false, fmt.Sprintf("status %d; body %s %q not satisfied", resp.StatusCode, c.bodyOp, c.bodyValue), start)
		}
	}
	if len(c.expectJSON) > 0 {
		var doc any
		if err := json.Unmarshal(data, &doc); err != nil {
			return c.result(false, fmt.Sprintf("status %d; response is not JSON", resp.StatusCode), start)
		}
		for _, a := range c.expectJSON {
			got, ok := jsonPath(doc, a.path)
			if !ok {
				return c.result(false, fmt.Sprintf("status %d; json %q missing", resp.StatusCode, a.path), start)
			}
			if !jsonAssert(got, a.op, a.value) {
				return c.result(false, fmt.Sprintf("status %d; json %q %s %q (got %q)", resp.StatusCode, a.path, a.op, a.value, jsonValueString(got)), start)
			}
		}
	}
	return c.success(resp, elapsed, fmt.Sprintf("status %d", resp.StatusCode), start)
}

// success builds the result for a request whose HTTP assertions all passed,
// folding in certificate inspection when configured (https only). A certificate
// problem turns the otherwise-passing check into a failure, keeping the http
// check's pass/fail semantics (OK==true means healthy).
func (c *httpCheck) success(resp *http.Response, elapsed time.Duration, statusMsg string, start time.Time) Result {
	if c.certHost == "" || resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		res := c.result(true, statusMsg, start)
		res.Data = map[string]any{"status": resp.StatusCode, "latency_ms": elapsed.Milliseconds(), "protocol": resp.Proto}
		return res
	}
	leaf := resp.TLS.PeerCertificates[0]
	s := certSampleFromCert(leaf)
	if c.certOpts.verify {
		s.VerifyError = verifyCertChain(leaf, resp.TLS.PeerCertificates[1:], c.certHost)
	}
	problems, daysLeft, hasExpiry := c.certEval.evaluate(s, c.certOpts, time.Now())
	ok := len(problems) == 0
	msg := statusMsg
	if !ok {
		msg = c.certHost + ": " + strings.Join(problems, "; ")
	}
	res := c.result(ok, msg, start)
	data := certData(c.certHost, c.certHost, "", s, daysLeft, hasExpiry)
	data["status"], data["latency_ms"], data["protocol"] = resp.StatusCode, elapsed.Milliseconds(), resp.Proto
	res.Data = data
	return res
}

// jsonAssert compares a decoded JSON value against want under op. Numeric
// comparisons require both sides to parse as numbers; ==/!=/contains compare the
// stringified value.
func jsonAssert(got any, op, want string) bool {
	gotStr := jsonValueString(got)
	switch op {
	case "", "==":
		return gotStr == want
	case "!=":
		return gotStr != want
	case "contains":
		return strings.Contains(gotStr, want)
	case "=~":
		ok, _ := compareValue(gotStr, "=~", want)
		return ok
	case ">", ">=", "<", "<=":
		gf, err1 := strconv.ParseFloat(gotStr, 64)
		wf, err2 := strconv.ParseFloat(want, 64)
		if err1 != nil || err2 != nil {
			return false
		}
		switch op {
		case ">":
			return gf > wf
		case ">=":
			return gf >= wf
		case "<":
			return gf < wf
		default:
			return gf <= wf
		}
	default:
		return false
	}
}

// jsonPath looks up a dotted path (e.g. "data.status") in a decoded JSON document
// of nested objects.
func jsonPath(doc any, path string) (any, bool) {
	cur := doc
	for _, key := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[key]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// jsonValueString renders a decoded JSON scalar for comparison with the expected
// (string) value from config.
func jsonValueString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v", t)
	}
}

// statusMatcher matches an HTTP status against exact codes and/or classes (the
// leading digit of an Nxx pattern), or — when op is set — an operator comparison
// against value (e.g. op "<" value "500").
type statusMatcher struct {
	codes   []int
	classes []int
	op      string
	value   string
}

func (m statusMatcher) matches(code int) bool {
	if m.op != "" {
		ok, _ := compareValue(strconv.Itoa(code), m.op, m.value)
		return ok
	}
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
	if m.op != "" {
		return m.op + " " + m.value
	}
	parts := make([]string, 0, len(m.codes)+len(m.classes))
	for _, c := range m.codes {
		parts = append(parts, strconv.Itoa(c))
	}
	for _, cl := range m.classes {
		parts = append(parts, strconv.Itoa(cl)+"xx")
	}
	return strings.Join(parts, ",")
}

// commandCheck runs a command and compares its exit code, and optionally its
// stdout/stderr, to expectations (section 12). The command is always an argv
// array, never a shell string (section 30/34).
type commandCheck struct {
	base
	runner     execx.Runner
	argv       []string
	expectExit int
	stdout     OutputMatcher
	stderr     OutputMatcher
}

func (c commandCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	res, _ := c.runner.Run(ctx, c.argv[0], c.argv[1:]...)
	if res.ExitCode != c.expectExit {
		msg := fmt.Sprintf("exit %d (want %d)", res.ExitCode, c.expectExit)
		if stderr := firstLine(res.Stderr); stderr != "" {
			msg += ": " + stderr
		}
		return c.result(false, msg, start)
	}
	if ok, detail := c.stdout.Match(res.Stdout); !ok {
		return c.result(false, fmt.Sprintf("exit %d; stdout %s", res.ExitCode, detail), start)
	}
	if ok, detail := c.stderr.Match(res.Stderr); !ok {
		return c.result(false, fmt.Sprintf("exit %d; stderr %s", res.ExitCode, detail), start)
	}
	return c.result(true, fmt.Sprintf("exit %d (want %d)", res.ExitCode, c.expectExit), start)
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
//
// This is the one internal use of an external tool: ldd consults the dynamic
// loader (search paths, ld.so.cache, transitive deps), which cannot be faithfully
// reimplemented from debug/elf alone, so per the native-Go policy it stays a
// documented exception (AGENTS.md).
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
