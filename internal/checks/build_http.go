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
	httpStatusMinCode           = 100
	httpStatusMaxCode           = 599
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
	clientOpts, warn := parseHTTPClientOptions(rawURL, entry)
	if warn != "" {
		return nil, warn
	}
	expectJSON, warn := parseAssertionMap(entry[CheckKeyExpectJSON], CheckKeyExpectJSON)
	if warn != "" {
		return nil, "http check: " + warn
	}
	hc := &httpCheck{
		base:        b,
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
	if !hasHTTPCertOptions(entry) {
		hc.client = httpClientWithRedirectPolicy(clientOpts.requestClient(client), boolDefaultTrue(entry[CheckKeyFollowRedirects]))
		return hc, ""
	}
	target, warn := clientOpts.certTarget(rawURL)
	if warn != "" {
		return nil, warn
	}
	if warn := configureHTTPCert(hc, target, clientOpts, entry); warn != "" {
		return nil, warn
	}
	hc.certClient = httpClientWithRedirectPolicy(hc.certClient, boolDefaultTrue(entry[CheckKeyFollowRedirects]))
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

// httpClientOptions describes the transport shared by a normal HTTP request
// and a certificate-inspection request. HTTP/3 uses QUIC; every transport binds
// its underlying TCP or UDP socket when requested.
type httpClientOptions struct {
	proxyURL *url.URL
	iface    string
	http3    bool
	target   *url.URL
}

func parseHTTPClientOptions(rawURL string, entry map[string]any) (httpClientOptions, string) {
	proxyURL, warn := parseProxyURL(entry)
	if warn != "" {
		return httpClientOptions{}, warn
	}
	opts := httpClientOptions{proxyURL: proxyURL, iface: firstHTTPInterface(entry), http3: cfgval.Bool(entry[CheckKeyHTTP3])}
	if opts.http3 {
		u, err := url.Parse(rawURL)
		if err != nil || u.Scheme != URLSchemeHTTPS {
			return httpClientOptions{}, "http check: http3 requires an https url"
		}
		opts.target = u
		if proxyURL != nil {
			return httpClientOptions{}, "http check: http3 and proxy are mutually exclusive"
		}
	}
	return opts, ""
}

func (o httpClientOptions) requestClient(client *http.Client) *http.Client {
	if o.http3 {
		return http3Client(o.iface, nil)
	}
	// interface: egress the HTTP request (and any proxy connection) through a
	// specific interface by binding the transport's dialer. The http client has
	// one fixed transport, so it honors a single interface (the first listed).
	if o.iface != "" {
		return httpClientWithTransport(o.proxyURL, o.iface)
	}
	if o.proxyURL != nil {
		return httpClientWithTransport(o.proxyURL, "")
	}
	return client
}

func (o httpClientOptions) certTarget(rawURL string) (url.URL, string) {
	if o.target != nil {
		return *o.target, ""
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return url.URL{}, "http check: invalid url: " + err.Error()
	}
	return *target, ""
}

func firstHTTPInterface(entry map[string]any) string {
	if ifaces := cfgval.StringList(entry[CheckKeyInterface]); len(ifaces) > 0 {
		return ifaces[0]
	}
	return ""
}

func http3Client(iface string, tlsConfig *tls.Config) *http.Client {
	transport := &http3.Transport{TLSClientConfig: tlsConfig}
	if iface != "" {
		transport.Dial = conn.BindQUICDialer(iface)
	}
	return &http.Client{Transport: transport}
}

func httpClientWithTransport(proxyURL *url.URL, iface string) *http.Client {
	return httpx.NewClient(httpx.ClientOptions{Proxy: proxyFunc(proxyURL), DialContext: conn.BindDialContext(iface)})
}

// proxyFunc turns an optional proxy URL into the transport hook, nil keeping
// the environment proxy selection.
func proxyFunc(proxyURL *url.URL) func(*http.Request) (*url.URL, error) {
	if proxyURL == nil {
		return nil
	}
	return http.ProxyURL(proxyURL)
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
	op, value, err := parseAssertionOpValue(fields, CheckKeyExpectBody, "", false)
	if err != nil {
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
func hasHTTPCertOptions(entry map[string]any) bool {
	for _, k := range httpCertKeys {
		if _, ok := entry[k]; ok {
			return true
		}
	}
	return false
}

func configureHTTPCert(hc *httpCheck, target url.URL, clientOpts httpClientOptions, entry map[string]any) string {
	if target.Scheme != URLSchemeHTTPS {
		return "http check: cert_* options require an https url"
	}
	verify := boolDefaultTrue(entry[CheckKeyCertVerify])
	days := 0
	if v, ok := cfgval.Int(entry[CheckKeyCertExpiresInDays]); ok {
		days = v
	}
	hc.certHost = target.Hostname()
	hc.certOpts = certOptions{
		expiresInDays:  days,
		verify:         verify,
		onAlgoChange:   cfgval.Bool(entry[CheckKeyCertOnAlgorithmChange]),
		onIssuerChange: cfgval.Bool(entry[CheckKeyCertOnIssuerChange]),
		onChange:       cfgval.Bool(entry[CheckKeyCertOnChange]),
	}
	hc.certVerification = newCertVerification(verify, hc.certHost)
	if clientOpts.http3 {
		// Read the leaf over QUIC too; http3 populates resp.TLS so the same
		// certificate logic applies. TLS 1.3 is enforced by QUIC.
		hc.certClient = http3Client(clientOpts.iface, inspectionTLSConfig("", tls.VersionTLS13, hc.certVerification))
		return ""
	}
	// Cert inspection also goes through the proxy (CONNECT for https).
	hc.certClient = httpx.NewClient(httpx.ClientOptions{
		TLS:         inspectionTLSConfig("", 0, hc.certVerification),
		Proxy:       proxyFunc(clientOpts.proxyURL),
		DialContext: conn.BindDialContext(clientOpts.iface),
	})
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
		op, value, err := parseAssertionOpValue(cond, CheckKeyExpectStatus, "", false)
		if err != nil {
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
		s := strings.TrimSpace(cfgval.String(item))
		code, class, ok := parseHTTPStatus(s)
		if !ok {
			return statusMatcher{}, fmt.Errorf("invalid expect_status %q", s)
		}
		if class != 0 {
			m.classes = append(m.classes, class)
			continue
		}
		m.codes = append(m.codes, code)
	}
	return m, nil
}

// ValidHTTPStatus reports whether value is a status code or class accepted by
// the HTTP check's expect_status matcher. Lists are validated element by
// element by configuration traversal and by parseStatusMatcher at runtime.
func ValidHTTPStatus(value string) bool {
	_, _, ok := parseHTTPStatus(strings.TrimSpace(value))
	return ok
}

func parseHTTPStatus(value string) (int, int, bool) {
	if isHTTPStatusClassPattern(value) {
		return 0, int(value[httpStatusClassDigitIndex] - httpStatusClassDigitBase), true
	}
	code, err := strconv.Atoi(value)
	return code, 0, err == nil && code >= httpStatusMinCode && code <= httpStatusMaxCode
}

func isHTTPStatusClassPattern(s string) bool {
	return len(s) == httpStatusClassPatternLen &&
		(s[httpStatusClassWildcard1] == httpStatusClassWildcard || s[httpStatusClassWildcard1] == httpStatusClassWildcardCaps) &&
		(s[httpStatusClassWildcard2] == httpStatusClassWildcard || s[httpStatusClassWildcard2] == httpStatusClassWildcardCaps) &&
		s[httpStatusClassDigitIndex] >= httpStatusClassMinDigit &&
		s[httpStatusClassDigitIndex] <= httpStatusClassMaxDigit
}
