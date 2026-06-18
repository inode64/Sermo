package checks

import (
	"context"
	"crypto/tls"
	"debug/elf"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/execx"
	"sermo/internal/servicemgr"
)

func TestTCPCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port := atoi(t, portStr)

	open := tcpCheck{base: base{name: "open", timeout: time.Second}, host: "127.0.0.1", port: port}
	if res := open.Run(context.Background()); !res.OK {
		t.Errorf("open port should pass: %s", res.Message)
	}

	// A bound, non-existent interface must fail the dial (never silently use the
	// default route).
	bound := tcpCheck{base: base{name: "bound", timeout: time.Second}, host: "127.0.0.1", ifaces: []string{"sermo-nonexistent0"}, port: port}
	if res := bound.Run(context.Background()); res.OK {
		t.Errorf("tcp check bound to a bogus interface should fail")
	}

	// A port with no listener should fail fast.
	ln.Close()
	closed := tcpCheck{base: base{name: "closed", timeout: time.Second}, host: "127.0.0.1", port: port}
	if res := closed.Run(context.Background()); res.OK {
		t.Errorf("closed port should fail")
	}
}

func TestHTTPCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(503)
	}))
	defer srv.Close()

	ok := httpCheck{base: base{name: "h", timeout: time.Second}, client: srv.Client(), url: srv.URL + "/health", method: "GET", expect: statusMatcher{codes: []int{200}}}
	if res := ok.Run(context.Background()); !res.OK {
		t.Errorf("200 should pass: %s", res.Message)
	}

	bad := httpCheck{base: base{name: "h", timeout: time.Second}, client: srv.Client(), url: srv.URL + "/down", method: "GET", expect: statusMatcher{codes: []int{200}}}
	if res := bad.Run(context.Background()); res.OK {
		t.Errorf("503 should fail when expecting 200")
	}

	// A 2xx class accepts 200; a list accepts 200 or 204.
	class := httpCheck{base: base{name: "h", timeout: time.Second}, client: srv.Client(), url: srv.URL + "/health", method: "GET", expect: statusMatcher{classes: []int{2}}}
	if res := class.Run(context.Background()); !res.OK {
		t.Errorf("2xx class should accept 200: %s", res.Message)
	}
	classBad := httpCheck{base: base{name: "h", timeout: time.Second}, client: srv.Client(), url: srv.URL + "/down", method: "GET", expect: statusMatcher{classes: []int{2}}}
	if res := classBad.Run(context.Background()); res.OK {
		t.Errorf("2xx class should reject 503")
	}
}

func TestBuildHTTPCertRequiresHTTPS(t *testing.T) {
	built, warns := Build(map[string]any{
		"h": map[string]any{"type": "http", "url": "http://example.com", "cert_expires_in_days": 14},
	}, Deps{DefaultTimeout: time.Second})
	if len(built) != 0 || len(warns) == 0 {
		t.Fatalf("cert_* on an http url must warn and build nothing: built=%d warns=%v", len(built), warns)
	}
	if !strings.Contains(warns[0], "https") {
		t.Fatalf("warning should mention https: %q", warns[0])
	}
}

func TestBuildHTTPSCertActivates(t *testing.T) {
	built, warns := Build(map[string]any{
		"h": map[string]any{"type": "http", "url": "https://example.com", "cert_expires_in_days": 14},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("a valid https cert check must build cleanly: warns=%v", warns)
	}
	hc, ok := built[0].Check.(*httpCheck)
	if !ok {
		t.Fatalf("http check must build to *httpCheck, got %T", built[0].Check)
	}
	if hc.certHost != "example.com" || hc.certClient == nil || hc.certOpts.expiresInDays != 14 {
		t.Fatalf("cert inspection not wired: host=%q client=%v opts=%+v", hc.certHost, hc.certClient, hc.certOpts)
	}
	if !hc.certOpts.verify {
		t.Fatal("cert_verify must default to true when inspection is active")
	}
}

func hostOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname()
}

func TestHTTPCheckCertExpiry(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	insecure := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // test client must read the test server's self-signed cert

	// Threshold far in the future → the server's short-lived cert "expires soon".
	c := &httpCheck{
		base: base{name: "h", timeout: time.Second}, client: insecure, certClient: insecure,
		url: srv.URL, method: "GET", expect: statusMatcher{codes: []int{200}},
		certHost: hostOf(t, srv.URL), certOpts: certOptions{expiresInDays: 1000000},
	}
	res := c.Run(context.Background())
	if res.OK {
		t.Fatalf("a cert inside the expiry threshold must fail the http check: %q", res.Message)
	}
	if res.Data["fingerprint"] == nil {
		t.Fatalf("cert data must be exposed: %v", res.Data)
	}
}

func TestHTTPCheckCertVerifyDisabledPasses(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	insecure := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // test client must read the test server's self-signed cert

	c := &httpCheck{
		base: base{name: "h", timeout: time.Second}, client: insecure, certClient: insecure,
		url: srv.URL, method: "GET", expect: statusMatcher{codes: []int{200}},
		certHost: hostOf(t, srv.URL), certOpts: certOptions{verify: false},
	}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("a reachable cert with no failing assertion must pass: %q", res.Message)
	}
	if _, ok := res.Data["not_after"].(string); !ok {
		t.Fatalf("cert data must carry not_after: %v", res.Data)
	}
}

func TestHTTPCheckCertVerifyFails(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	insecure := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // test client must read the test server's self-signed cert

	c := &httpCheck{
		base: base{name: "h", timeout: time.Second}, client: insecure, certClient: insecure,
		url: srv.URL, method: "GET", expect: statusMatcher{codes: []int{200}},
		certHost: hostOf(t, srv.URL), certOpts: certOptions{verify: true},
	}
	if c.Run(context.Background()).OK {
		t.Fatal("verify=true against a self-signed test server must fail")
	}
}

func TestHTTPCheckPostHeadersJSON(t *testing.T) {
	var gotMethod, gotAuth, gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"ok","data":{"count":3,"ready":true}}`))
	}))
	defer srv.Close()

	c, warn := buildHTTP(t, srv, map[string]any{
		"type":   "http",
		"url":    srv.URL + "/api",
		"method": "post",
		"headers": map[string]any{
			"Authorization": "Bearer xyz",
		},
		"json": map[string]any{"name": "sermo", "n": 2},
		"expect_json": map[string]any{
			"status":     "ok",
			"data.count": 3,    // number compared as string
			"data.ready": true, // bool compared as string
		},
		"expect_body": "ready",
	})
	if warn != "" {
		t.Fatalf("build warn: %s", warn)
	}
	if res := c.Run(context.Background()); !res.OK {
		t.Fatalf("expected pass, got: %s", res.Message)
	}
	if gotMethod != "POST" || gotAuth != "Bearer xyz" || gotCT != "application/json" {
		t.Fatalf("request not built right: method=%s auth=%q ct=%q", gotMethod, gotAuth, gotCT)
	}
	if gotBody != `{"n":2,"name":"sermo"}` {
		t.Fatalf("json body = %q", gotBody)
	}
}

func TestHTTPCheckExpectJSONMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"degraded"}`))
	}))
	defer srv.Close()

	c, _ := buildHTTP(t, srv, map[string]any{
		"type": "http", "url": srv.URL, "expect_json": map[string]any{"status": "ok"},
	})
	res := c.Run(context.Background())
	if res.OK {
		t.Fatal("a mismatched json field must fail")
	}
	if !strings.Contains(res.Message, `json "status"`) {
		t.Fatalf("message should name the field: %s", res.Message)
	}
}

func TestHTTPCheckExpectJSONOperators(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"replicas":3,"state":"healthy","errors":0}`))
	}))
	defer srv.Close()

	pass, _ := buildHTTP(t, srv, map[string]any{
		"type": "http", "url": srv.URL,
		"expect_json": map[string]any{
			"replicas": map[string]any{"op": ">=", "value": 2},
			"errors":   map[string]any{"op": "<", "value": 1},
			"state":    map[string]any{"op": "contains", "value": "health"},
		},
	})
	if res := pass.Run(context.Background()); !res.OK {
		t.Fatalf("operator assertions should pass: %s", res.Message)
	}

	fail, _ := buildHTTP(t, srv, map[string]any{
		"type": "http", "url": srv.URL,
		"expect_json": map[string]any{"replicas": map[string]any{"op": ">", "value": 5}},
	})
	if res := fail.Run(context.Background()); res.OK {
		t.Fatal("replicas 3 > 5 must fail")
	}
}

func TestHTTPCheckNonJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	c, _ := buildHTTP(t, srv, map[string]any{"type": "http", "url": srv.URL, "expect_json": map[string]any{"a": "b"}})
	if c.Run(context.Background()).OK {
		t.Fatal("expect_json against a non-JSON response must fail")
	}
}

// buildHTTP builds an http check from a config entry, using the test server's
// client so TLS/transport match.
func buildHTTP(t *testing.T, srv *httptest.Server, entry map[string]any) (Check, string) {
	t.Helper()
	built, warns := Build(map[string]any{"h": entry}, Deps{HTTPClient: srv.Client(), DefaultTimeout: time.Second})
	if len(warns) > 0 {
		return nil, warns[0]
	}
	return built[0].Check, ""
}

func TestParseStatusMatcher(t *testing.T) {
	cases := []struct {
		in    any
		code  int
		match bool
	}{
		{nil, 200, true},   // default 200
		{nil, 201, false},  //
		{200, 200, true},   // single
		{"2xx", 204, true}, // class
		{"2xx", 301, false},
		{[]any{200, 204}, 204, true}, // list
		{[]any{200, 204}, 500, false},
		{[]any{"2xx", 301}, 301, true}, // mixed list
	}
	for _, tc := range cases {
		m, err := parseStatusMatcher(tc.in)
		if err != nil {
			t.Fatalf("parseStatusMatcher(%v) error = %v", tc.in, err)
		}
		if got := m.matches(tc.code); got != tc.match {
			t.Errorf("matches(%v, %d) = %v, want %v", tc.in, tc.code, got, tc.match)
		}
	}
	if _, err := parseStatusMatcher("nope"); err == nil {
		t.Error("invalid expect_status should error")
	}
}

type fakeRunner struct {
	result execx.Result
}

func (r fakeRunner) Run(context.Context, string, ...string) (execx.Result, error) {
	return r.result, nil
}

func TestCommandCheck(t *testing.T) {
	ok := commandCheck{base: base{name: "c", timeout: time.Second}, runner: fakeRunner{execx.Result{ExitCode: 0}}, argv: []string{"true"}, expectExit: 0}
	if res := ok.Run(context.Background()); !res.OK {
		t.Errorf("exit 0 should pass: %s", res.Message)
	}
	bad := commandCheck{base: base{name: "c", timeout: time.Second}, runner: fakeRunner{execx.Result{ExitCode: 1, Stderr: "boom\n"}}, argv: []string{"false"}, expectExit: 0}
	res := bad.Run(context.Background())
	if res.OK {
		t.Errorf("exit 1 should fail")
	}
	if res.Message == "" {
		t.Errorf("failure message should include detail")
	}
}

func TestServiceCheck(t *testing.T) {
	status := func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil }
	ok := serviceCheck{base: base{name: "s", timeout: time.Second}, expect: "active", status: status}
	if res := ok.Run(context.Background()); !res.OK {
		t.Errorf("active==active should pass: %s", res.Message)
	}
	bad := serviceCheck{base: base{name: "s", timeout: time.Second}, expect: "inactive", status: status}
	if res := bad.Run(context.Background()); res.OK {
		t.Errorf("active!=inactive should fail")
	}
}

func TestFileExistsAndBinaryChecks(t *testing.T) {
	dir := t.TempDir()
	flag := filepath.Join(dir, "flag")
	if err := os.WriteFile(flag, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if res := (fileExistsCheck{base: base{name: "f"}, path: flag}).Run(context.Background()); !res.OK {
		t.Errorf("existing file should pass")
	}
	if res := (fileExistsCheck{base: base{name: "f"}, path: filepath.Join(dir, "absent")}).Run(context.Background()); res.OK {
		t.Errorf("absent file should fail")
	}
	if res := (binaryCheck{base: base{name: "b"}, path: bin}).Run(context.Background()); !res.OK {
		t.Errorf("executable should pass")
	}
	if res := (binaryCheck{base: base{name: "b"}, path: flag}).Run(context.Background()); res.OK {
		t.Errorf("non-executable file should fail")
	}
}

func TestLibrariesCheck(t *testing.T) {
	// Non-existent binary should fail (open error).
	c := librariesCheck{base: base{name: "lib"}, binary: "/non/existent/binary/that/does/not/exist"}
	if res := c.Run(context.Background()); res.OK {
		t.Fatalf("non-existent binary should fail, got OK with %q", res.Message)
	}

	// A real dynamically-linked binary on the test host must resolve (now with transitive).
	c = librariesCheck{base: base{name: "lib", timeout: time.Second}, binary: "/bin/sh"}
	if res := c.Run(context.Background()); !res.OK {
		t.Fatalf("/bin/sh libraries should resolve: %s", res.Message)
	}
}

func TestLibrariesCheckRealBinary(t *testing.T) {
	// /bin/sh is dynamically linked on this host; its libraries must resolve.
	c := librariesCheck{base: base{name: "lib", timeout: time.Second}, binary: "/bin/sh"}
	if res := c.Run(context.Background()); !res.OK {
		t.Fatalf("/bin/sh libraries should resolve: %s", res.Message)
	}
}

func TestLibrariesCheckHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := librariesCheck{base: base{name: "lib", timeout: time.Second}, binary: "/bin/sh"}
	res := c.Run(ctx)
	if res.OK {
		t.Fatal("libraries check must fail when the context is already cancelled")
	}
	if !strings.Contains(res.Message, context.Canceled.Error()) {
		t.Fatalf("message = %q, want context.Canceled", res.Message)
	}
}

// Low-level tests for the native resolver helpers.
func TestFindLibrary(t *testing.T) {
	// Absolute path
	abs := "/bin/sh"
	if got := findLibrary(abs, nil); got != abs {
		t.Fatalf("absolute: got %q", got)
	}
	if got := findLibrary("/nonexistent/abs/path", nil); got != "" {
		t.Fatalf("missing absolute should return empty")
	}

	// Relative via dirs
	dirs := []string{"/bin", "/usr/bin"}
	if got := findLibrary("sh", dirs); got == "" {
		t.Fatalf("sh should be found via dirs")
	}
	if got := findLibrary("nonexistentlib.so.9", dirs); got != "" {
		t.Fatalf("missing should return empty")
	}
}

func TestExpandOrigin(t *testing.T) {
	bin := "/usr/local/bin/myapp"
	if got := expandOrigin("$ORIGIN/../lib", bin); !strings.HasSuffix(got, "/usr/local/lib") {
		t.Fatalf("$ORIGIN expand: %q", got)
	}
	if got := expandOrigin("/fixed/path", bin); got != "/fixed/path" {
		t.Fatalf("no origin: %q", got)
	}
}

func TestResolveNeededBasic(t *testing.T) {
	// Exercises the recursive resolver (direct + transitive).
	ef, err := elf.Open("/bin/sh")
	if err != nil {
		t.Skip("cannot open /bin/sh for transitive test")
	}
	defer ef.Close()

	dirs := collectLibrarySearchDirs("/bin/sh", ef)
	missing := resolveNeeded(context.Background(), []string{"libc.so.6"}, dirs, make(map[string]bool))
	if len(missing) > 0 {
		t.Logf("note: resolveNeeded reported missing in smoke test (distro dependent): %v", missing)
	}
}

func TestProcessCheck(t *testing.T) {
	observe := func(exe, user string) string {
		if exe == "/usr/bin/mariadb-backup" {
			return "running"
		}
		return "absent"
	}
	ok := processCheck{base: base{name: "p"}, exe: "/usr/bin/mariadb-backup", expect: "running", observe: observe}
	if res := ok.Run(context.Background()); !res.OK {
		t.Errorf("running==running should pass: %s", res.Message)
	}
	absent := processCheck{base: base{name: "p"}, exe: "/usr/bin/mariadb-backup", expect: "absent", observe: observe}
	if res := absent.Run(context.Background()); res.OK {
		t.Errorf("running!=absent should fail")
	}
}

func TestBuildProcessCheckNeedsObserver(t *testing.T) {
	section := map[string]any{"p": map[string]any{"type": "process", "exe": "/x", "state": "running"}}
	if _, warnings := Build(section, Deps{}); len(warnings) != 1 {
		t.Fatalf("process check without observer should warn, got %v", warnings)
	}
	built, warnings := Build(section, Deps{Processes: func(string, string) string { return "running" }})
	if len(warnings) != 0 || len(built) != 1 {
		t.Fatalf("process check should build with observer: built=%d warnings=%v", len(built), warnings)
	}
}

func TestBuildProcessCheckRequiresExe(t *testing.T) {
	section := map[string]any{"p": map[string]any{"type": "process", "user": "mysql", "state": "running"}}
	if _, warnings := Build(section, Deps{Processes: func(string, string) string { return "running" }}); len(warnings) != 1 {
		t.Fatalf("process check without exe should warn, got %v", warnings)
	} else if !strings.Contains(warnings[0], "requires exe") {
		t.Fatalf("warning = %q, want requires exe", warnings[0])
	}
}

func TestRunConcurrentPreservesOrderAndOptional(t *testing.T) {
	built := []Built{
		{Check: fileExistsCheck{base: base{name: "a"}, path: "/definitely/missing"}, Optional: true},
		{Check: binaryCheck{base: base{name: "b"}, path: "/bin/sh"}},
	}
	results := Run(context.Background(), built, 0)
	if len(results) != 2 || results[0].Check != "a" || results[1].Check != "b" {
		t.Fatalf("results out of order: %+v", results)
	}
	if !results[0].Optional {
		t.Errorf("first result should carry Optional=true")
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, ok := cfgval.Int(s)
	if !ok {
		t.Fatalf("bad int %q", s)
	}
	return n
}

func TestParseProxyURL(t *testing.T) {
	if u, w := parseProxyURL(map[string]any{}); u != nil || w != "" {
		t.Fatalf("no proxy = nil/empty, got %v/%q", u, w)
	}
	u, w := parseProxyURL(map[string]any{"proxy": "http://squid:3128"})
	if w != "" || u == nil || u.Host != "squid:3128" {
		t.Fatalf("valid proxy: %v / %q", u, w)
	}
	if _, w := parseProxyURL(map[string]any{"proxy": "ftp://x:1"}); w == "" {
		t.Fatal("a non-http/socks scheme must warn")
	}
	if _, w := parseProxyURL(map[string]any{"proxy": "://nope"}); w == "" {
		t.Fatal("a malformed proxy url must warn")
	}
}

func TestHTTPCheckThroughProxy(t *testing.T) {
	// A fake forward proxy that answers 200 to any proxied request.
	var gotProxied bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A forward-proxy request carries an absolute URI in the request line.
		if r.URL.IsAbs() {
			gotProxied = true
		}
		w.WriteHeader(200)
	}))
	defer proxy.Close()

	built, warns := Build(map[string]any{
		"web": map[string]any{"type": "http", "url": "http://example.invalid/health", "proxy": proxy.URL, "expect_status": 200},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("proxied http check should build: warns=%v", warns)
	}
	res := built[0].Check.Run(context.Background())
	if !res.OK {
		t.Fatalf("request through the proxy should pass: %q", res.Message)
	}
	if !gotProxied {
		t.Fatal("the request did not go through the proxy")
	}
}

func TestBuildHTTPProxyBadURL(t *testing.T) {
	_, warns := Build(map[string]any{
		"web": map[string]any{"type": "http", "url": "http://x/", "proxy": "ftp://bad:1"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) == 0 {
		t.Fatal("a bad proxy scheme must warn")
	}
}

// --- output pattern analysis (analyze:) ---

type fakeCheck struct{ res Result }

func (f fakeCheck) Name() string               { return f.res.Check }
func (f fakeCheck) Run(context.Context) Result { return f.res }

func TestRunRespectsCheckEscalatedOptional(t *testing.T) {
	built := []Built{{Check: fakeCheck{res: Result{Check: "c", OK: false, Optional: true}}, Optional: false}}
	got := Run(context.Background(), built, 0)
	if !got[0].Optional {
		t.Fatalf("a check that returns Optional:true must stay optional, got %+v", got[0])
	}
}

func buildAnalyzeCmd(t *testing.T, out, sev string) Result {
	t.Helper()
	built, warns := Build(map[string]any{
		"cfgtest": map[string]any{
			"type":    "command",
			"command": []any{"true"},
			"analyze": map[string]any{"rules": []any{
				map[string]any{"id": "r", "match": "(?i)deprecated|BACK UP DATA NOW", "severity": sev},
			}},
		},
	}, Deps{DefaultTimeout: time.Second, Runner: fakeRunner{execx.Result{Stdout: out}}})
	if len(warns) != 0 {
		t.Fatalf("build warns=%v", warns)
	}
	return built[0].Check.Run(context.Background())
}

func TestCommandCheckAnalyzeWarning(t *testing.T) {
	res := buildAnalyzeCmd(t, "X is deprecated\n", "warning")
	if res.OK || !res.Optional {
		t.Fatalf("warning pattern must give OK=false Optional=true, got %+v", res)
	}
	if res.Data["pattern_severity"] != "warning" || res.Data["pattern_id"] != "r" {
		t.Fatalf("missing pattern data: %+v", res.Data)
	}
}

func TestCommandCheckAnalyzeError(t *testing.T) {
	res := buildAnalyzeCmd(t, "BACK UP DATA NOW\n", "error")
	if res.OK || res.Optional {
		t.Fatalf("error pattern must give OK=false Optional=false, got %+v", res)
	}
}

func TestCommandCheckAnalyzeClean(t *testing.T) {
	res := buildAnalyzeCmd(t, "all good\n", "warning")
	if !res.OK {
		t.Fatalf("no pattern match must pass, got %+v", res)
	}
}

func TestTrimOutput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"only ws", "   \t\n  ", ""},
		{"single line", " hello world \n", "hello world"},
		{"leading blank lines", "\n\n\nline1\nline2", "line1\nline2"},
		{"trailing blank lines", "line1\nline2\n\n\n", "line1\nline2"},
		{"both ends + internal blank", "\n\n  \nfirst\n\nmiddle\n\nlast\n\n  ", "first\n\nmiddle\n\nlast"},
		{"all blank lines", "\n\n\t\n  \n", ""},
		{"mixed whitespace lines", "  \r\n\t\nreal\n   \n  ", "real"},
		{"version banner typical", "\n\nPostgreSQL 15.3\n\n", "PostgreSQL 15.3"},
		{"sql with trailing", "col1\nval1\n\n", "col1\nval1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TrimOutput(tc.in); got != tc.want {
				t.Fatalf("TrimOutput(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFirstNonEmptyLineUsesTrimOutput(t *testing.T) {
	if got := FirstNonEmptyLine("\n\n  \nreal line\n\n  \n"); got != "real line" {
		t.Fatalf("FirstNonEmptyLine after trim gave %q", got)
	}
	if got := FirstNonEmptyLine("\n\n\n"); got != "" {
		t.Fatalf("FirstNonEmptyLine all blank gave %q", got)
	}
}
