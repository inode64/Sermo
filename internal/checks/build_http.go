package checks

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/quic-go/quic-go/http3"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
	"sermo/internal/httpx"
)

const (
	defaultHTTPStatusCode       = http.StatusOK
	httpHeaderAccept            = httpx.HeaderAccept
	httpHeaderContentType       = httpx.HeaderContentType
	httpContentTypeJSON         = httpx.ContentTypeJSON
	httpStatusClassPatternLen   = 3
	httpStatusClassDigitIndex   = 0
	httpStatusClassWildcard1    = 1
	httpStatusClassWildcard2    = 2
	httpStatusClassMinDigit     = '1'
	httpStatusClassMaxDigit     = '5'
	httpStatusClassWildcard     = 'x'
	httpStatusClassWildcardCaps = 'X'
	httpStatusClassDigitBase    = '0'
)

// buildHTTPCheck builds an http(s) check, configuring proxy, http3 and interface
// egress on a per-check client when requested.
func buildHTTPCheck(b base, entry map[string]any, client *http.Client) (Check, string) {
	rawURL := cfgval.AsString(entry[CheckKeyURL])
	if rawURL == "" {
		return nil, "http check requires a url"
	}
	method, warn := ParseHTTPMethod(entry[CheckKeyMethod])
	if warn != "" {
		return nil, "http check: " + warn
	}
	expect, err := parseStatusMatcher(entry[CheckKeyExpectStatus])
	if err != nil {
		return nil, "http check: " + err.Error()
	}
	body, contentType, warn := httpRequestBody(entry)
	if warn != "" {
		return nil, warn
	}
	reqClient, warn := httpRequestClient(rawURL, entry, client)
	if warn != "" {
		return nil, warn
	}
	expectJSON, warn := parseAssertionMap(entry[CheckKeyExpectJSON], CheckKeyExpectJSON)
	if warn != "" {
		return nil, "http check: " + warn
	}
	hc := &httpCheck{
		base:        b,
		client:      httpClientWithRedirectPolicy(reqClient, boolDefaultTrue(entry[CheckKeyFollowRedirects])),
		url:         rawURL,
		method:      method,
		headers:     cfgval.StringMap(entry[CheckKeyHeaders]),
		body:        body,
		contentType: contentType,
		expect:      expect,
		expectJSON:  expectJSON,
	}
	if warn := configureHTTPBodyAssertion(hc, entry); warn != "" {
		return nil, warn
	}
	if warn := configureHTTPLatency(hc, entry); warn != "" {
		return nil, warn
	}
	if warn := configureHTTPCert(hc, entry, rawURL); warn != "" {
		return nil, warn
	}
	if hc.certClient != nil {
		hc.certClient = httpClientWithRedirectPolicy(hc.certClient, boolDefaultTrue(entry[CheckKeyFollowRedirects]))
	}
	return hc, ""
}

func httpRequestBody(entry map[string]any) ([]byte, string, string) {
	jsonBody, hasJSON := entry[CheckKeyJSON]
	if hasJSON && jsonBody != nil {
		if _, hasBody := entry[CheckKeyBody]; hasBody {
			return nil, "", "http check: body and json are mutually exclusive"
		}
		raw, err := json.Marshal(jsonBody)
		if err != nil {
			return nil, "", "http check: invalid json body: " + err.Error()
		}
		return raw, httpContentTypeJSON, ""
	}
	if body := cfgval.AsString(entry[CheckKeyBody]); body != "" {
		return []byte(body), "", ""
	}
	return nil, "", ""
}

// httpRequestClient configures the per-check transport. HTTP/3 always uses
// QUIC, while proxy and interface routing use an HTTP transport dialer.
func httpRequestClient(rawURL string, entry map[string]any, client *http.Client) (*http.Client, string) {
	proxyURL, warn := parseProxyURL(entry)
	if warn != "" {
		return nil, warn
	}
	http3Enabled := cfgval.Bool(entry[CheckKeyHTTP3])
	if http3Enabled {
		if u, err := url.Parse(rawURL); err != nil || u.Scheme != URLSchemeHTTPS {
			return nil, "http check: http3 requires an https url"
		}
		if proxyURL != nil {
			return nil, "http check: http3 and proxy are mutually exclusive"
		}
		client = &http.Client{Transport: &http3.Transport{}}
	} else if proxyURL != nil {
		client = httpClientWithTransport(proxyURL, "")
	}
	// interface: egress the HTTP request (and any proxy connection) through a
	// specific interface by binding the transport's dialer. The http client has
	// one fixed transport, so it honors a single interface (the first listed).
	if ifaces := parseInterfaces(entry[CheckKeyInterface]); len(ifaces) > 0 {
		if http3Enabled {
			return nil, "http check: http3 and interface are mutually exclusive"
		}
		return httpClientWithTransport(proxyURL, ifaces[0]), ""
	}
	return client, ""
}

func httpClientWithTransport(proxyURL *url.URL, iface string) *http.Client {
	tr := httpx.CloneDefaultTransport()
	if proxyURL != nil {
		tr.Proxy = http.ProxyURL(proxyURL)
	}
	if iface != "" {
		tr.DialContext = conn.BindDialer(iface).DialContext
	}
	return &http.Client{Transport: tr}
}

func configureHTTPBodyAssertion(check *httpCheck, entry map[string]any) string {
	// expect_body is an {op, value} operator comparison against the trimmed body.
	bodyMatch, present := entry[CheckKeyExpectBody]
	if !present {
		return ""
	}
	fields, ok := bodyMatch.(map[string]any)
	if !ok {
		return "http expect_body must be an {op, value} mapping"
	}
	op := cfgval.AsString(fields[CheckKeyOp])
	if !validCompareOp(op) {
		return "http expect_body op must be one of " + cfgval.AssertOpSummary
	}
	value := cfgval.String(fields[CheckKeyValue])
	if err := ValidateAssertionValue(CheckKeyExpectBody, op, value); err != nil {
		return "http " + err.Error()
	}
	check.bodyOp, check.bodyValue = op, value
	return ""
}

func configureHTTPLatency(check *httpCheck, entry map[string]any) string {
	op, value, warn := parseExpectLatency(entry)
	if warn != "" {
		return "http " + warn
	}
	check.latencyOp, check.latencyValue = op, value
	return ""
}

// boolDefaultTrue reads an optional boolean entry value that defaults to true
// when absent or not a bool — the shape follow_redirects and cert_verify share.
func boolDefaultTrue(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return true
}

func httpClientWithRedirectPolicy(client *http.Client, follow bool) *http.Client {
	if follow {
		return client
	}
	if client == nil {
		client = &http.Client{}
	}
	copied := *client
	copied.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &copied
}

// HTTPMethodList is the user-facing list of standard HTTP methods accepted by
// HTTP checks.
const HTTPMethodList = "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS, TRACE, CONNECT"

var standardHTTPMethods = map[string]struct{}{
	http.MethodGet:     {},
	http.MethodHead:    {},
	http.MethodPost:    {},
	http.MethodPut:     {},
	http.MethodPatch:   {},
	http.MethodDelete:  {},
	http.MethodOptions: {},
	http.MethodTrace:   {},
	http.MethodConnect: {},
}

// ParseHTTPMethod returns the normalized standard HTTP method for a check
// config value.
func ParseHTTPMethod(raw any) (string, string) {
	if raw == nil {
		return http.MethodGet, ""
	}
	s, ok := raw.(string)
	if !ok {
		return "", "method must be a string"
	}
	method := strings.ToUpper(strings.TrimSpace(s))
	if _, known := standardHTTPMethods[method]; !known {
		return "", fmt.Sprintf("method %q is not a standard HTTP method (%s)", s, HTTPMethodList)
	}
	return method, ""
}

// HTTPProxySchemeList is the user-facing list of accepted HTTP check proxy
// schemes.
const HTTPProxySchemeList = URLSchemeHTTP + ", " + URLSchemeHTTPS + ", " + URLSchemeSOCKS5 + " or " + URLSchemeSOCKS5H

// IsHTTPProxyScheme reports whether scheme is accepted for an HTTP check proxy.
func IsHTTPProxyScheme(scheme string) bool {
	switch scheme {
	case URLSchemeHTTP, URLSchemeHTTPS, URLSchemeSOCKS5, URLSchemeSOCKS5H:
		return true
	default:
		return false
	}
}

// parseProxyURL reads the optional `proxy` field of an http check (e.g. a Squid
// proxy, "http://[user:pass@]squid:3128"). It returns the parsed URL, or a
// warning when the value is malformed. A nil URL with no warning means no proxy.
func parseProxyURL(entry map[string]any) (*url.URL, string) {
	s := cfgval.AsString(entry[CheckKeyProxy])
	if s == "" {
		return nil, ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil, "http check: invalid proxy url " + strconv.Quote(s)
	}
	if IsHTTPProxyScheme(u.Scheme) {
		return u, ""
	}
	return nil, "http check: proxy scheme must be " + HTTPProxySchemeList
}

// httpCertKeys are the optional certificate-inspection keys on the http check.
var httpCertKeys = []string{
	CheckKeyCertExpiresInDays,
	CheckKeyCertVerify,
	CheckKeyCertOnChange,
	CheckKeyCertOnIssuerChange,
	CheckKeyCertOnAlgorithmChange,
}

// configureHTTPCert enables certificate inspection on hc when any cert_* key is
// present. It requires an https url and returns a warning string on a config
// error (empty when there is nothing to configure or configuration succeeded).
func configureHTTPCert(hc *httpCheck, entry map[string]any, rawURL string) string {
	active := false
	for _, k := range httpCertKeys {
		if _, ok := entry[k]; ok {
			active = true
			break
		}
	}
	if !active {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "http check: invalid url: " + err.Error()
	}
	if u.Scheme != URLSchemeHTTPS {
		return "http check: cert_* options require an https url"
	}
	verify := boolDefaultTrue(entry[CheckKeyCertVerify])
	days := 0
	if v, ok := cfgval.Int(entry[CheckKeyCertExpiresInDays]); ok {
		days = v
	}
	hc.certHost = u.Hostname()
	hc.certOpts = certOptions{
		expiresInDays:  days,
		verify:         verify,
		onAlgoChange:   cfgval.Bool(entry[CheckKeyCertOnAlgorithmChange]),
		onIssuerChange: cfgval.Bool(entry[CheckKeyCertOnIssuerChange]),
		onChange:       cfgval.Bool(entry[CheckKeyCertOnChange]),
	}
	if cfgval.Bool(entry[CheckKeyHTTP3]) {
		// Read the leaf over QUIC too; http3 populates resp.TLS so the same
		// certificate logic applies. TLS 1.3 is enforced by QUIC.
		hc.certClient = &http.Client{Transport: &http3.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}, //nolint:gosec // leaf inspected and verified manually via verifyCertChain
		}}
		return ""
	}
	tr := httpx.CloneDefaultTransport()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // leaf inspected and verified manually via verifyCertChain
	if pu, _ := parseProxyURL(entry); pu != nil {
		tr.Proxy = http.ProxyURL(pu) // cert inspection also goes through the proxy (CONNECT for https)
	}
	hc.certClient = &http.Client{Transport: tr}
	return ""
}

// parseStatusMatcher parses an expect_status field: a single code, a class
// ("2xx"), or a list of either. Empty defaults to 200.
func parseStatusMatcher(v any) (statusMatcher, error) {
	if v == nil {
		return statusMatcher{codes: []int{defaultHTTPStatusCode}}, nil
	}
	// Operator form: {op, value} (e.g. status < 500).
	if cond, ok := v.(map[string]any); ok {
		op := cfgval.AsString(cond[CheckKeyOp])
		if !validCompareOp(op) {
			return statusMatcher{}, fmt.Errorf("expect_status op must be one of %s", cfgval.AssertOpSummary)
		}
		value := cfgval.String(cond[CheckKeyValue])
		if err := ValidateAssertionValue(CheckKeyExpectStatus, op, value); err != nil {
			return statusMatcher{}, err
		}
		return statusMatcher{op: op, value: value}, nil
	}
	var m statusMatcher
	var items []any
	if list, ok := v.([]any); ok {
		items = list
	} else {
		items = []any{v}
	}
	for _, item := range items {
		if n, ok := cfgval.Int(item); ok {
			m.codes = append(m.codes, n)
			continue
		}
		s := strings.TrimSpace(cfgval.AsString(item))
		if isHTTPStatusClassPattern(s) {
			m.classes = append(m.classes, int(s[httpStatusClassDigitIndex]-httpStatusClassDigitBase))
			continue
		}
		return statusMatcher{}, fmt.Errorf("invalid expect_status %q", s)
	}
	return m, nil
}

func isHTTPStatusClassPattern(s string) bool {
	return len(s) == httpStatusClassPatternLen &&
		(s[httpStatusClassWildcard1] == httpStatusClassWildcard || s[httpStatusClassWildcard1] == httpStatusClassWildcardCaps) &&
		(s[httpStatusClassWildcard2] == httpStatusClassWildcard || s[httpStatusClassWildcard2] == httpStatusClassWildcardCaps) &&
		s[httpStatusClassDigitIndex] >= httpStatusClassMinDigit &&
		s[httpStatusClassDigitIndex] <= httpStatusClassMaxDigit
}
