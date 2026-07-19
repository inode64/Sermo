package checks

import (
	"context"
	"crypto/tls"
	"debug/elf"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
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

func TestHTTPCheckCanDisableRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusMovedPermanently)
	}))
	defer srv.Close()

	built, warns := Build(map[string]any{
		"h": map[string]any{
			"type":             "http",
			"url":              srv.URL,
			"follow_redirects": false,
			"expect_status":    map[string]any{"op": "<", "value": 500},
		},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("http check should build cleanly: built=%d warns=%v", len(built), warns)
	}
	res := built[0].Check.Run(context.Background())
	if !res.OK || !strings.Contains(res.Message, "301") {
		t.Fatalf("redirect response should be evaluated without following it: ok=%v msg=%q", res.OK, res.Message)
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

// runHTTPCertCheck runs an httpCheck with the given certOpts against a fresh
// self-signed TLS test server (read with an insecure client), returning the
// result.
func runHTTPCertCheck(t *testing.T, opts certOptions) Result {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	insecure := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // test client must read the test server's self-signed cert
	c := &httpCheck{
		base: base{name: "h", timeout: time.Second}, client: insecure, certClient: insecure,
		url: srv.URL, method: "GET", expect: statusMatcher{codes: []int{200}},
		certHost: hostOf(t, srv.URL), certOpts: opts,
	}
	return c.Run(context.Background())
}

func TestHTTPCheckCertExpiry(t *testing.T) {
	// Threshold far in the future → the server's short-lived cert "expires soon".
	res := runHTTPCertCheck(t, certOptions{expiresInDays: 1000000})
	if res.OK {
		t.Fatalf("a cert inside the expiry threshold must fail the http check: %q", res.Message)
	}
	if res.Data["fingerprint"] == nil {
		t.Fatalf("cert data must be exposed: %v", res.Data)
	}
}

func TestHTTPCheckCertVerifyDisabledPasses(t *testing.T) {
	res := runHTTPCertCheck(t, certOptions{verify: false})
	if !res.OK {
		t.Fatalf("a reachable cert with no failing assertion must pass: %q", res.Message)
	}
	if _, ok := res.Data["not_after"].(string); !ok {
		t.Fatalf("cert data must carry not_after: %v", res.Data)
	}
}

func TestHTTPCheckCertVerifyFails(t *testing.T) {
	if runHTTPCertCheck(t, certOptions{verify: true}).OK {
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
		w.WriteHeader(http.StatusOK)
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
		"expect_body": map[string]any{"op": "contains", "value": "ready"},
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

type recordingUserRunner struct {
	result execx.Result
	user   string
	name   string
	args   []string
}

func (r *recordingUserRunner) Run(context.Context, string, ...string) (execx.Result, error) {
	return execx.Result{ExitCode: -1}, nil
}

func (r *recordingUserRunner) RunUser(_ context.Context, user, name string, args ...string) (execx.Result, error) {
	r.user = user
	r.name = name
	r.args = append([]string(nil), args...)
	return r.result, nil
}

type slowCommandRunner struct{}

func (slowCommandRunner) Run(ctx context.Context, _ string, _ ...string) (execx.Result, error) {
	<-ctx.Done()
	return execx.Result{ExitCode: -1}, fmt.Errorf("run tool: %w", ctx.Err())
}

func TestCommandCheckTimeoutMessage(t *testing.T) {
	check := commandCheck{
		base:       base{name: "c", timeout: time.Millisecond},
		runner:     slowCommandRunner{},
		argv:       []string{"/bin/tool", "--version"},
		expectExit: []int{0},
	}
	res := check.Run(context.Background())
	if res.OK {
		t.Fatal("expected timeout failure")
	}
	if !strings.Contains(res.Message, "timeout after 1ms") {
		t.Fatalf("message = %q, want timeout after duration", res.Message)
	}
}

func TestCommandCheck(t *testing.T) {
	ok := commandCheck{base: base{name: "c", timeout: time.Second}, runner: fakeRunner{execx.Result{ExitCode: 0}}, argv: []string{"true"}, expectExit: []int{0}}
	if res := ok.Run(context.Background()); !res.OK {
		t.Errorf("exit 0 should pass: %s", res.Message)
	}
	bad := commandCheck{base: base{name: "c", timeout: time.Second}, runner: fakeRunner{execx.Result{ExitCode: 1, Stderr: "boom\n"}}, argv: []string{"false"}, expectExit: []int{0}}
	res := bad.Run(context.Background())
	if res.OK {
		t.Errorf("exit 1 should fail")
	}
	if !strings.Contains(res.Message, "exit 1") || !strings.Contains(res.Message, "boom") {
		t.Errorf("failure message %q should report the exit code and the stderr detail", res.Message)
	}
}

// assertRunsAsUser runs check (which must embed runner) and asserts it passes and
// invoked RunUser as postgres with wantName and a single "--check" argument.
func assertRunsAsUser(t *testing.T, runner *recordingUserRunner, check Check, wantName string) {
	t.Helper()
	if res := check.Run(context.Background()); !res.OK {
		t.Fatalf("check with user should pass: %s", res.Message)
	}
	if runner.user != "postgres" || runner.name != wantName || len(runner.args) != 1 || runner.args[0] != "--check" {
		t.Fatalf("RunUser call = user=%q name=%q args=%v", runner.user, runner.name, runner.args)
	}
}

func TestCommandCheckUser(t *testing.T) {
	runner := &recordingUserRunner{result: execx.Result{ExitCode: 0}}
	check := commandCheck{
		base:       base{name: "c", timeout: time.Second},
		runner:     runner,
		argv:       []string{"/usr/bin/postgres", "--check"},
		user:       "postgres",
		expectExit: []int{0},
	}
	assertRunsAsUser(t, runner, check, "/usr/bin/postgres")
}

func TestCommandCheckUserRequiresUserRunner(t *testing.T) {
	check := commandCheck{
		base:       base{name: "c", timeout: time.Second},
		runner:     fakeRunner{execx.Result{ExitCode: 0}},
		argv:       []string{"/usr/bin/postgres", "--check"},
		user:       "postgres",
		expectExit: []int{0},
	}

	res := check.Run(context.Background())
	if res.OK {
		t.Fatal("command check with user must fail closed when the runner cannot switch users")
	}
	if !strings.Contains(res.Message, "does not support user") {
		t.Fatalf("message = %q, want missing user runner detail", res.Message)
	}
}

func TestCommandCheckExportsData(t *testing.T) {
	built, warns := Build(map[string]any{
		"version_short": map[string]any{
			"type":    "command",
			"command": []any{"php-fpm", "-v"},
			"export": map[string]any{
				"api":     map[string]any{"regex": "API ([0-9]+)"},
				"errcode": map[string]any{"from": "stderr", "trim": false},
				"missing": map[string]any{"regex": "missing ([0-9]+)", "default": "none"},
			},
		},
	}, Deps{
		DefaultTimeout: time.Second,
		Runner: fakeRunner{execx.Result{
			Stdout: "8.3\nAPI 12\n",
			Stderr: " warn \n",
		}},
	})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("Build warns=%v built=%d", warns, len(built))
	}
	res := built[0].Check.Run(context.Background())
	if !res.OK {
		t.Fatalf("command should pass: %+v", res)
	}
	want := map[string]any{"version_short": "8.3\nAPI 12", "api": "12", "errcode": " warn \n", "missing": "none"}
	for k, v := range want {
		if res.Data[k] != v {
			t.Fatalf("data[%s] = %#v, want %#v; all=%#v", k, res.Data[k], v, res.Data)
		}
	}

	built, warns = Build(map[string]any{
		"version": map[string]any{
			"type":    "command",
			"command": []any{"php-fpm", "-v"},
		},
	}, Deps{
		DefaultTimeout: time.Second,
		Runner:         fakeRunner{execx.Result{Stdout: "PHP 8.3.6 (fpm-fcgi)\n"}},
	})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("Build version warns=%v built=%d", warns, len(built))
	}
	res = built[0].Check.Run(context.Background())
	if res.Data["version"] != "PHP 8.3.6 (fpm-fcgi)" || res.Data["version_short"] != "8.3.6" {
		t.Fatalf("version exports = %#v", res.Data)
	}

	built, warns = Build(map[string]any{
		"version": map[string]any{
			"type":    "command",
			"command": []any{"pkexec", "--version"},
		},
	}, Deps{
		DefaultTimeout: time.Second,
		Runner:         fakeRunner{execx.Result{Stdout: "pkexec version 126\n"}},
	})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("Build integer version warns=%v built=%d", warns, len(built))
	}
	res = built[0].Check.Run(context.Background())
	if res.Data["version"] != "pkexec version 126" || res.Data["version_short"] != "126" {
		t.Fatalf("integer version exports = %#v", res.Data)
	}

	built, warns = Build(map[string]any{
		"version": map[string]any{
			"type":    "command",
			"command": []any{"numad", "-V"},
		},
	}, Deps{
		DefaultTimeout: time.Second,
		Runner:         fakeRunner{execx.Result{Stdout: "/usr/bin/numad version: 20150602: compiled Sep 12 2024\n"}},
	})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("Build numad version warns=%v built=%d", warns, len(built))
	}
	res = built[0].Check.Run(context.Background())
	if res.Data["version"] != "/usr/bin/numad version: 20150602: compiled Sep 12 2024" || res.Data["version_short"] != "20150602" {
		t.Fatalf("numad version exports = %#v", res.Data)
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
	if res := (fileCheck{base: base{name: "file"}, path: flag}).Run(context.Background()); !res.OK {
		t.Errorf("regular file should pass")
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if res := (fileCheck{base: base{name: "file"}, path: empty, nonEmpty: true}).Run(context.Background()); res.OK {
		t.Errorf("empty file should fail a non-empty file check")
	}
	if res := (fileCheck{base: base{name: "file"}, path: dir}).Run(context.Background()); res.OK {
		t.Errorf("directory should fail a regular file check")
	}
	if res := (lockfileCheck{base: base{name: "lock"}, paths: []string{filepath.Join(dir, "absent.lock"), flag}}).Run(context.Background()); !res.OK {
		t.Fatalf("lockfile candidate should pass: %s", res.Message)
	} else if res.Data["path"] != flag {
		t.Fatalf("lockfile data path = %v, want %s", res.Data["path"], flag)
	}
	if res := (lockfileCheck{base: base{name: "lock"}, paths: []string{dir}}).Run(context.Background()); res.OK {
		t.Errorf("directory should fail a lockfile check")
	}
	if res := (binaryCheck{base: base{name: "b"}, path: bin}).Run(context.Background()); !res.OK {
		t.Errorf("executable should pass")
	}
	if res := (binaryCheck{base: base{name: "b"}, path: flag}).Run(context.Background()); res.OK {
		t.Errorf("non-executable file should fail")
	}

	sock := filepath.Join(dir, "svc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if res := (socketCheck{base: base{name: "sock"}, paths: []string{filepath.Join(dir, "absent.sock"), sock}}).Run(context.Background()); !res.OK {
		t.Fatalf("socket candidate should pass: %s", res.Message)
	} else if res.Data["path"] != sock {
		t.Fatalf("socket data path = %v, want %s", res.Data["path"], sock)
	}
	if res := (socketCheck{base: base{name: "sock"}, paths: []string{flag}}).Run(context.Background()); res.OK {
		t.Errorf("regular file should fail a socket check")
	}
}

func TestBuildFileAndSocketChecksNeedPath(t *testing.T) {
	if _, warn := buildFileCheck(base{}, map[string]any{}); warn == "" {
		t.Fatal("file check without a path must warn")
	}
	if c, warn := buildFileCheck(base{}, map[string]any{"path": "/etc/passwd"}); warn != "" || c == nil {
		t.Fatalf("valid file check should build: warn=%q", warn)
	}
	if _, warn := buildLockfileCheck(base{}, map[string]any{}); warn == "" {
		t.Fatal("lockfile check without a path must warn")
	}
	if c, warn := buildLockfileCheck(base{}, map[string]any{"path": []any{"/run/a.lock", "/run/b.lock"}}); warn != "" || c == nil {
		t.Fatalf("valid lockfile candidate list should build: warn=%q", warn)
	}
	if _, warn := buildSocketCheck(base{}, map[string]any{}); warn == "" {
		t.Fatal("socket check without a path must warn")
	}
	if c, warn := buildSocketCheck(base{}, map[string]any{"path": []any{"/run/a.sock", "/run/b.sock"}}); warn != "" || c == nil {
		t.Fatalf("valid socket candidate list should build: warn=%q", warn)
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

func TestLibrariesCheckHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := librariesCheck{base: base{name: "lib", timeout: time.Second}, binary: "/bin/sh"}
	res := c.Run(ctx)
	if res.OK {
		t.Fatal("libraries check must fail when the context is already cancelled")
	}
	if !strings.Contains(res.Message, "cancelled") {
		t.Fatalf("message = %q, want cancelled", res.Message)
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
	ok := processCheck{base: base{name: "p"}, exes: []string{"/usr/bin/mariadb-backup"}, expect: "running", observe: observe}
	if res := ok.Run(context.Background()); !res.OK {
		t.Errorf("running==running should pass: %s", res.Message)
	}
	absent := processCheck{base: base{name: "p"}, exes: []string{"/usr/bin/mariadb-backup"}, expect: "absent", observe: observe}
	if res := absent.Run(context.Background()); res.OK {
		t.Errorf("running!=absent should fail")
	}
}

func TestBuildProcessCheckExeAny(t *testing.T) {
	section := map[string]any{"p": map[string]any{
		"type":    "process",
		"exe_any": []any{"/usr/bin/mysqldump", "/usr/bin/xtrabackup"},
		"user":    "mysql",
		"state":   "running",
	}}
	var gotExes []string
	var gotUser string
	built, warnings := Build(section, Deps{ProcessesAny: func(exes []string, user string) string {
		gotExes = append(gotExes, exes...)
		gotUser = user
		return "running"
	}})
	if len(warnings) != 0 || len(built) != 1 {
		t.Fatalf("process exe_any should build: built=%d warnings=%v", len(built), warnings)
	}
	if res := built[0].Check.Run(context.Background()); !res.OK {
		t.Fatalf("process exe_any result = %+v, want OK", res)
	}
	if !slices.Equal(gotExes, []string{"/usr/bin/mysqldump", "/usr/bin/xtrabackup"}) || gotUser != "mysql" {
		t.Fatalf("ProcessesAny call = exes %v user %q", gotExes, gotUser)
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
	} else if !strings.Contains(warnings[0], "requires exe or exe_any") {
		t.Fatalf("warning = %q, want requires exe or exe_any", warnings[0])
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
	u, w = parseProxyURL(map[string]any{"proxy": "socks5h://proxy.local:1080"})
	if w != "" || u == nil || u.Scheme != "socks5h" {
		t.Fatalf("socks5h proxy should be valid: %v / %q", u, w)
	}
	if _, w := parseProxyURL(map[string]any{"proxy": "ftp://x:1"}); !strings.Contains(w, "socks5h") {
		t.Fatalf("a non-http/socks scheme must warn with full scheme list, got %q", w)
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
		w.WriteHeader(http.StatusOK)
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
