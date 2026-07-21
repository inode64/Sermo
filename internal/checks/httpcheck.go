package checks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/netutil"
	"sermo/internal/units"
)

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
		return c.result(false, fmt.Sprintf("%s %s: %v", c.method, netutil.RedactURL(c.url), netutil.URLErrorCause(err)), start)
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
	for key := range strings.SplitSeq(path, ".") {
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
