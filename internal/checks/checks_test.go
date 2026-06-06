package checks

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

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

	ok := httpCheck{base: base{name: "h", timeout: time.Second}, client: srv.Client(), url: srv.URL + "/health", method: "GET", expectStatus: 200}
	if res := ok.Run(context.Background()); !res.OK {
		t.Errorf("200 should pass: %s", res.Message)
	}

	bad := httpCheck{base: base{name: "h", timeout: time.Second}, client: srv.Client(), url: srv.URL + "/down", method: "GET", expectStatus: 200}
	if res := bad.Run(context.Background()); res.OK {
		t.Errorf("503 should fail when expecting 200")
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

func TestProcessCheck(t *testing.T) {
	observe := func(exe, user string) string {
		if exe == "/usr/bin/mariabackup" {
			return "running"
		}
		return "absent"
	}
	ok := processCheck{base: base{name: "p"}, exe: "/usr/bin/mariabackup", expect: "running", observe: observe}
	if res := ok.Run(context.Background()); !res.OK {
		t.Errorf("running==running should pass: %s", res.Message)
	}
	absent := processCheck{base: base{name: "p"}, exe: "/usr/bin/mariabackup", expect: "absent", observe: observe}
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
	n, ok := intField(s)
	if !ok {
		t.Fatalf("bad int %q", s)
	}
	return n
}
