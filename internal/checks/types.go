package checks

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/netutil"
	"sermo/internal/output"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
	"sermo/internal/units"
)

const (
	binaryExecutableModeMask = 0o111

	ldSoConfDir       = "/etc/ld.so.conf.d"
	ldSoConfFile      = "/etc/ld.so.conf"
	ldSoConfSuffix    = ".conf"
	ldSoIncludePrefix = "include "
	ldLibraryPathEnv  = "LD_LIBRARY_PATH"
	ldPathSeparator   = ":"
	ldCommentHash     = "#"
	ldCommentSemi     = ";"
	elfOriginToken    = "$ORIGIN"
	elfOriginBraced   = "${ORIGIN}"
	libDirAArch64     = "/lib/aarch64-linux-gnu"
	libDirARMHF       = "/lib/arm-linux-gnueabihf"
	libDirI386        = "/lib/i386-linux-gnu"
	libDirRoot        = "/lib"
	libDirRoot64      = "/lib64"
	libDirUsr         = "/usr/lib"
	libDirUsr64       = "/usr/lib64"
	libDirUsrAArch64  = "/usr/lib/aarch64-linux-gnu"
	libDirUsrARMHF    = "/usr/lib/arm-linux-gnueabihf"
	libDirUsrI386     = "/usr/lib/i386-linux-gnu"
	libDirUsrX8664    = "/usr/lib/x86_64-linux-gnu"
	libDirX8664       = "/lib/x86_64-linux-gnu"
)

// tcpCheck dials a TCP host:port, optionally egressing through one
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

	addr := netutil.JoinHostPort(c.host, c.port)
	chosen, perIface, err := tryInterfaces(c.ifaces, c.ifaceAll, func(iface string) error {
		nc, e := conn.BindDialer(iface).DialContext(ctx, conn.TransportTCP, addr)
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
// (expect), optionally the body via an operator comparison and JSON response
// matches at dotted paths (expectJSON). The request may carry custom headers and
// a raw or JSON body.
type httpCheck struct {
	base
	client       *http.Client
	url          string
	method       string
	headers      map[string]string
	body         []byte
	contentType  string // set when the body is JSON, unless headers override it
	expect       statusMatcher
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
const maxHTTPBody = units.BytesPerMiB

const httpStatusClassDivisor = 100

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
		req.Header.Set(httpHeaderContentType, c.contentType)
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
		ms := strconv.FormatInt(elapsed.Milliseconds(), numericBaseDecimal)
		ok, err := compareValue(ms, c.latencyOp, c.latencyValue)
		if err != nil {
			return c.result(false, fmt.Sprintf("latency: %v", err), start)
		}
		if !ok {
			return c.result(false, fmt.Sprintf("status %d; latency %sms not %s %s", resp.StatusCode, ms, c.latencyOp, c.latencyValue), start)
		}
	}
	if c.bodyOp == "" && len(c.expectJSON) == 0 {
		return c.success(resp, elapsed, fmt.Sprintf("status %d", resp.StatusCode), start)
	}

	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody))
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
		res.Data = map[string]any{DataKeyStatus: resp.StatusCode, DataKeyLatencyMS: elapsed.Milliseconds(), DataKeyProtocol: resp.Proto}
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
	data[DataKeyStatus], data[DataKeyLatencyMS], data[DataKeyProtocol] = resp.StatusCode, elapsed.Milliseconds(), resp.Proto
	res.Data = data
	return res
}

// jsonAssert compares a decoded JSON value against want under op. Numeric
// comparisons require both sides to parse as numbers; ==/!=/contains compare the
// stringified value.
func jsonAssert(got any, op, want string) bool {
	gotStr := jsonValueString(got)
	switch op {
	case "", cfgval.CompareOpEqual:
		return gotStr == want
	case cfgval.CompareOpNotEqual:
		return gotStr != want
	case cfgval.AssertOpContains:
		return strings.Contains(gotStr, want)
	case cfgval.AssertOpRegex:
		ok, _ := compareValue(gotStr, cfgval.AssertOpRegex, want)
		return ok
	case cfgval.CompareOpGreater, cfgval.CompareOpGreaterEqual, cfgval.CompareOpLess, cfgval.CompareOpLessEqual:
		gf, err1 := strconv.ParseFloat(gotStr, numericBits64)
		wf, err2 := strconv.ParseFloat(want, numericBits64)
		if err1 != nil || err2 != nil {
			return false
		}
		switch op {
		case cfgval.CompareOpGreater:
			return gf > wf
		case cfgval.CompareOpGreaterEqual:
			return gf >= wf
		case cfgval.CompareOpLess:
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
		return strconv.FormatFloat(t, floatFormatFixed, floatPrecisionAuto, numericBits64)
	case bool:
		return strconv.FormatBool(t)
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
	return slices.Contains(m.codes, code) || slices.Contains(m.classes, code/httpStatusClassDivisor)
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

// cmdState persists on_change state while a service worker or host watch reuses
// the check instance across cycles. A config reload/worker rebuild creates a
// fresh baseline.
type cmdState struct {
	primed  bool
	last    string // comparison key (raw line, or version_short truncated to a level)
	lastRaw string // the raw line behind `last`, shown in change messages
}

// commandCheck runs a command and compares its exit code, and optionally its
// stdout/stderr, to expectations. With on_change it also alerts when
// the command's stdout changes between cycles (e.g. a `version` command whose
// output changed). The command is always an argv array, never a shell string
// .
type commandCheck struct {
	base
	runner     execx.Runner
	argv       []string
	user       string
	expectExit []int
	stdout     OutputMatcher
	stderr     OutputMatcher
	version    VersionMatcher
	exports    []commandExport
	analyzer   *outputAnalyzer
	onChange   bool
	// changeLevel selects how on_change compares output: 0 compares the trimmed
	// raw line; 1/2/3 compare version_short truncated to that many components
	// (major/minor/patch), with a raw-line fallback when no version is parseable.
	changeLevel int
	state       *cmdState
}

func (c commandCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	res, err := c.runCommand(ctx)
	// fail builds a failing result and attaches the bounded command output so the
	// emitted event can show WHY the command failed.
	fail := func(msg string) Result {
		r := c.result(false, msg, start)
		if out := output.Bounded(res.Stdout, res.Stderr); out != "" {
			r.Data = map[string]any{DataKeyOutput: out}
		}
		return r
	}
	if res.ExitCode == execx.ExitCodeRunFailure {
		msg := execx.OperatorFailure(err, res, c.timeout)
		if msg == "" {
			msg = execx.CommandDidNotStart
		}
		return fail(msg)
	}
	if !ExitCodeExpected(res.ExitCode, c.expectExit) {
		msg := fmt.Sprintf("exit %d (want %s)", res.ExitCode, ExpectExitText(c.expectExit))
		if stderr := output.FirstNonEmptyLine(res.Stderr); stderr != "" {
			msg += ": " + stderr
		} else if err != nil {
			msg += ": " + err.Error()
		}
		return fail(msg)
	}
	if ok, detail := c.stdout.Match(res.Stdout); !ok {
		return fail(fmt.Sprintf("exit %d; stdout %s", res.ExitCode, detail))
	}
	if ok, detail := c.stderr.Match(res.Stderr); !ok {
		return fail(fmt.Sprintf("exit %d; stderr %s", res.ExitCode, detail))
	}
	if ok, detail := c.version.Match(VersionOutput(res.Stdout, res.Stderr)); !ok {
		return fail(fmt.Sprintf("exit %d; version %s", res.ExitCode, detail))
	}
	if c.analyzer.Active() {
		if sev, id, line := c.analyzer.Analyze(res.Stdout, res.Stderr); sev != SevOK {
			r := c.result(false, fmt.Sprintf("exit %d; %s pattern %q: %s", res.ExitCode, sev, id, output.FirstNonEmptyLine(line)), start)
			r.Optional = sev == SevWarning
			r.Data = map[string]any{DataKeyPatternID: id, DataKeyPatternSeverity: sev.String(), DataKeyPatternLine: line}
			if out := output.Bounded(res.Stdout, res.Stderr); out != "" {
				r.Data[DataKeyOutput] = out
			}
			return r
		}
	}
	if c.onChange && c.state != nil {
		raw := output.Trim(res.Stdout)
		key := c.changeKey(raw)
		if c.state.primed && key != c.state.last {
			r := c.result(false, fmt.Sprintf("output changed (%s -> %s)", output.FirstNonEmptyLine(c.state.lastRaw), output.FirstNonEmptyLine(raw)), start)
			r.Data = map[string]any{DataKeyOld: c.state.lastRaw, DataKeyNew: raw}
			c.state.last, c.state.lastRaw = key, raw
			return r
		}
		c.state.last, c.state.lastRaw, c.state.primed = key, raw, true
	}
	r := c.result(true, fmt.Sprintf("exit %d (want %s)", res.ExitCode, ExpectExitText(c.expectExit)), start)
	if data := c.exportData(res.Stdout, res.Stderr); len(data) > 0 {
		r.Data = data
	}
	return r
}

// changeKey turns a trimmed command output into the value on_change compares
// across cycles. With changeLevel==0 the raw line is the key. With a level set
// (version monitor), the key is version_short truncated to that level so only a
// change at or above the chosen granularity fires; when no version is parseable
// it falls back to the raw line so a change is never silently missed.
func (c commandCheck) changeKey(raw string) string {
	if c.changeLevel <= 0 {
		return raw
	}
	if short := TruncateVersion(ShortVersion(raw), c.changeLevel); short != "" {
		return short
	}
	return raw
}

func (c commandCheck) runCommand(ctx context.Context) (execx.Result, error) {
	if c.user != "" {
		return execx.RunUser(ctx, c.runner, execx.NoTimeout, c.user, c.argv[0], c.argv[1:]...)
	}
	return c.runner.Run(ctx, c.argv[0], c.argv[1:]...)
}

// ExitCodeExpected reports whether got matches one of the expected command exit
// codes. A nil or empty expected list means the default success code, 0.
func ExitCodeExpected(got int, want []int) bool {
	if len(want) == 0 {
		want = []int{CommandDefaultExpectedExit}
	}
	for _, n := range want {
		if got == n {
			return true
		}
	}
	return false
}

// ExpectExitText formats expected exit codes for operator-facing messages.
func ExpectExitText(want []int) string {
	if len(want) == 0 {
		return strconv.Itoa(CommandDefaultExpectedExit)
	}
	parts := make([]string, 0, len(want))
	for _, n := range want {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, " or ")
}

func (c commandCheck) exportData(stdout, stderr string) map[string]any {
	if len(c.exports) == 0 {
		return nil
	}
	out := make(map[string]any, len(c.exports))
	for _, e := range c.exports {
		out[e.name] = e.value(stdout, stderr)
	}
	return out
}

// serviceCheck compares the service's backend status to an expected value
// . The status function is injected so the check stays single-shot.
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

// fileExistsCheck passes when a path exists. It must point at a
// foreign flag/lock file, never Sermo's own runtime locks.
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

// fileCheck passes when a path exists and is a regular file.
type fileCheck struct {
	base
	path     string
	nonEmpty bool
}

func (c fileCheck) Run(_ context.Context) Result {
	start := time.Now()
	info, err := os.Stat(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return c.result(false, c.path+" does not exist", start)
		}
		return c.result(false, fmt.Sprintf("stat %s: %v", c.path, err), start)
	}
	if !info.Mode().IsRegular() {
		return c.result(false, c.path+" is not a regular file", start)
	}
	if c.nonEmpty && info.Size() == 0 {
		return c.result(false, c.path+" is empty", start)
	}
	res := c.result(true, c.path+" is a regular file", start)
	res.Data = map[string]any{DataKeyPath: c.path, DataKeySize: info.Size()}
	return res
}

// lockfileCheck passes when any configured candidate exists and is a regular
// file. It is for runtime lock artifacts created by the monitored service.
type lockfileCheck struct {
	base
	paths []string
}

func (c lockfileCheck) Run(_ context.Context) Result {
	return pathMatchResult(c.base, c.paths, lockfileCandidate, CheckTypeLockfile)
}

type pathMatch struct {
	message string
	data    map[string]any
	failure string
}

func firstMatchingPath(paths []string, predicate func(string, os.FileInfo) pathMatch, kindMsg string) pathMatch {
	if len(paths) == 0 {
		return pathMatch{failure: kindMsg + " check has no path candidates"}
	}
	var failures []string
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			failures = append(failures, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		match := predicate(path, info)
		if match.failure != "" {
			failures = append(failures, match.failure)
			continue
		}
		return match
	}
	if len(failures) > 0 {
		return pathMatch{failure: strings.Join(failures, "; ")}
	}
	if len(paths) == 1 {
		return pathMatch{failure: paths[0] + " does not exist"}
	}
	return pathMatch{failure: fmt.Sprintf("none of %s candidates exist (%s)", kindMsg, strings.Join(paths, ", "))}
}

func pathMatchResult(b base, paths []string, predicate func(string, os.FileInfo) pathMatch, kindMsg string) Result {
	start := time.Now()
	match := firstMatchingPath(paths, predicate, kindMsg)
	if match.failure != "" {
		return b.result(false, match.failure, start)
	}
	res := b.result(true, match.message, start)
	res.Data = match.data
	return res
}

func lockfileCandidate(path string, info os.FileInfo) pathMatch {
	if !info.Mode().IsRegular() {
		return pathMatch{failure: path + " is not a regular file"}
	}
	return pathMatch{
		message: path + " is a regular lockfile",
		data:    map[string]any{DataKeyPath: path, DataKeySize: info.Size()},
	}
}

// binaryCheck passes when a path exists and is an executable file.
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
	if info.Mode().Perm()&binaryExecutableModeMask == 0 {
		return c.result(false, c.path+" is not executable", start)
	}
	return c.result(true, c.path+" is executable", start)
}

// socketCheck passes when any configured candidate exists and is a Unix socket.
type socketCheck struct {
	base
	paths []string
}

func (c socketCheck) Run(_ context.Context) Result {
	return pathMatchResult(c.base, c.paths, socketCandidate, CheckTypeSocket)
}

func socketCandidate(path string, info os.FileInfo) pathMatch {
	if info.Mode()&os.ModeSocket == 0 {
		return pathMatch{failure: path + " is not a socket"}
	}
	return pathMatch{
		message: path + " is a socket",
		data:    map[string]any{DataKeyPath: path},
	}
}

// metricCheck reads a sampled metric and compares it to a threshold. Its OK is
// the comparison result (the threshold being met), so
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
	res := c.result(met, fmt.Sprintf("%s/%s %s %s = %t", c.scope, c.metric, c.op, c.value, met), start)
	res.Data = map[string]any{
		DataKeyType:      CheckTypeMetric,
		DataKeyScope:     c.scope,
		DataKeyMetric:    c.metric,
		DataKeyOp:        c.op,
		DataKeyThreshold: c.value,
	}
	if value, unit, ok, err := metrics.ReadingValueForThreshold(reading, c.value); err == nil && ok {
		res.Data[DataKeyValue] = value
		res.Data[DataKeyUnit] = unit
	}
	return res
}

// processCheck passes when the observed state of processes matching its
// exe/user selector equals the expected state. Matching uses the
// exact resolved-exe and real-UID rules .
type processCheck struct {
	base
	exes       []string
	user       string
	expect     string
	observe    func(exe, user string) string
	observeAny func(exes []string, user string) string
}

func (c processCheck) Run(_ context.Context) Result {
	start := time.Now()
	if c.observe == nil && c.observeAny == nil {
		return c.result(false, "process discovery unavailable", start)
	}
	state := c.observedState()
	ok := state == c.expect
	return c.result(ok, fmt.Sprintf("state %s (want %s)", state, c.expect), start)
}

func (c processCheck) observedState() string {
	if c.observeAny != nil {
		return c.observeAny(c.exes, c.user)
	}
	matchedZombie := false
	for _, exe := range c.exes {
		switch c.observe(exe, c.user) {
		case process.StateRunning:
			return process.StateRunning
		case process.StateZombie:
			matchedZombie = true
		}
	}
	if matchedZombie {
		return process.StateZombie
	}
	return process.StateAbsent
}

// librariesCheck verifies that all DT_NEEDED shared libraries for a binary
// can be resolved using the dynamic linker's search rules (rpath/runpath,
// system library directories and /etc/ld.so.conf*). Implemented with debug/elf
// only (no external ldd), per the native-Go policy.
type librariesCheck struct {
	base
	binary string
}

func (c librariesCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	ef, err := elf.Open(c.binary)
	if err != nil {
		return c.result(false, c.binary+": "+err.Error(), start)
	}
	defer ef.Close()

	needed, err := ef.DynString(elf.DT_NEEDED)
	if err != nil || len(needed) == 0 {
		return c.result(true, c.binary+": static binary, no shared libraries", start)
	}

	dirs := collectLibrarySearchDirs(c.binary, ef)

	// LD_LIBRARY_PATH takes precedence (as the real dynamic linker does).
	// We prepend it so it is searched first.
	if lp := os.Getenv(ldLibraryPathEnv); lp != "" {
		for _, p := range strings.Split(lp, ldPathSeparator) {
			if p != "" {
				dirs = append([]string{expandOrigin(p, c.binary)}, dirs...)
			}
		}
		dirs = dedupPreserveOrder(dirs)
	}

	missing := resolveNeeded(ctx, needed, dirs, make(map[string]bool))
	if err := ctx.Err(); err != nil {
		return c.result(false, c.binary+": "+execx.ContextFailure(err, c.timeout), start)
	}
	if len(missing) > 0 {
		return c.result(false, c.binary+": missing shared libraries", start)
	}
	return c.result(true, c.binary+": all shared libraries resolve", start)
}

// resolveNeeded recursively resolves DT_NEEDED entries (including transitive
// dependencies of the resolved libraries). It returns the list of sonames
// that could not be located.
func resolveNeeded(ctx context.Context, needed []string, dirs []string, seen map[string]bool) []string {
	var missing []string
	for _, soname := range needed {
		if err := ctx.Err(); err != nil {
			return missing
		}
		if seen[soname] {
			continue
		}
		seen[soname] = true

		path := findLibrary(soname, dirs)
		if path == "" {
			missing = append(missing, soname)
			continue
		}

		// Open the resolved library to collect its own DT_NEEDED (transitive).
		ef, err := elf.Open(path)
		if err != nil {
			missing = append(missing, soname+" (open failed)")
			continue
		}
		subNeeded, _ := ef.DynString(elf.DT_NEEDED)
		ef.Close()

		if len(subNeeded) > 0 {
			subMissing := resolveNeeded(ctx, subNeeded, dirs, seen)
			missing = append(missing, subMissing...)
		}
	}
	return missing
}

// collectLibrarySearchDirs builds the library search path list for the given
// binary, honouring its DT_RUNPATH / DT_RPATH (with $ORIGIN expansion),
// its directory, common multi-arch paths, and a best-effort parse of
// /etc/ld.so.conf (and .d fragments).
func collectLibrarySearchDirs(binary string, ef *elf.File) []string {
	var dirs []string

	// Prefer RUNPATH, fall back to RPATH (older binaries).
	if rps, _ := ef.DynString(elf.DT_RUNPATH); len(rps) > 0 && rps[0] != "" {
		for _, p := range strings.Split(rps[0], ldPathSeparator) {
			if p != "" {
				dirs = append(dirs, expandOrigin(p, binary))
			}
		}
	} else if rps, _ := ef.DynString(elf.DT_RPATH); len(rps) > 0 && rps[0] != "" {
		for _, p := range strings.Split(rps[0], ldPathSeparator) {
			if p != "" {
				dirs = append(dirs, expandOrigin(p, binary))
			}
		}
	}

	// Directory of the binary itself (some apps ship private libs next to exe).
	if d := filepath.Dir(binary); d != "" && d != "." {
		dirs = append(dirs, d)
	}

	// Common system locations (covers most distros and multi-arch setups).
	dirs = append(dirs,
		libDirRoot, libDirUsr,
		libDirRoot64, libDirUsr64,
		libDirX8664, libDirUsrX8664,
		libDirAArch64, libDirUsrAArch64,
		libDirI386, libDirUsrI386,
		libDirARMHF, libDirUsrARMHF,
	)

	// Best-effort augmentation from ld.so.conf and fragments.
	if more := parseLdSoConf(ldSoConfFile); len(more) > 0 {
		dirs = append(dirs, more...)
	}
	// Common drop-in directory even if main conf doesn't include it.
	if entries, _ := os.ReadDir(ldSoConfDir); len(entries) > 0 {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ldSoConfSuffix) {
				if more := parseLdSoConf(filepath.Join(ldSoConfDir, e.Name())); len(more) > 0 {
					dirs = append(dirs, more...)
				}
			}
		}
	}

	return dedupPreserveOrder(dirs)
}

func expandOrigin(p, binary string) string {
	dir := filepath.Dir(binary)
	p = strings.ReplaceAll(p, elfOriginToken, dir)
	p = strings.ReplaceAll(p, elfOriginBraced, dir)
	return filepath.Clean(p)
}

func findLibrary(soname string, dirs []string) string {
	if filepath.IsAbs(soname) {
		if _, err := os.Stat(soname); err == nil {
			return soname
		}
		return ""
	}
	for _, d := range dirs {
		cand := filepath.Join(d, soname)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return ""
}

// parseLdSoConf returns directory paths listed in a simple ld.so.conf file.
// It ignores comments and basic "include" lines (we separately scan the
// common /etc/ld.so.conf.d directory).
func parseLdSoConf(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), checkLineSeparator) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ldCommentHash) || strings.HasPrefix(line, ldCommentSemi) {
			continue
		}
		if strings.HasPrefix(line, ldSoIncludePrefix) {
			continue // we handle .d explicitly
		}
		out = append(out, line)
	}
	return out
}

// dedupPreserveOrder removes duplicate directories while keeping the first
// occurrence (used after prepending LD_LIBRARY_PATH).
func dedupPreserveOrder(dirs []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}
