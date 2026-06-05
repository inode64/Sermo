package checks

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"sermo/internal/execx"
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
type httpCheck struct {
	base
	client       *http.Client
	url          string
	method       string
	expectStatus int
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

	ok := resp.StatusCode == c.expectStatus
	return c.result(ok, fmt.Sprintf("status %d (want %d)", resp.StatusCode, c.expectStatus), start)
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

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
