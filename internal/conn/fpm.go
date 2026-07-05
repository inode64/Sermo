package conn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func init() { Register(fpmProtocol{}, "php-fpm") }

// FastCGI record types and constants (FastCGI spec 1.0).
const (
	fcgiVersion1     = 1
	fcgiBeginRequest = 1
	fcgiEndRequest   = 3
	fcgiParams       = 4
	fcgiStdin        = 5
	fcgiStdout       = 6
	fcgiStderr       = 7
	fcgiResponder    = 1
	fcgiRequestID    = 1
)

// fpmProtocol probes a PHP-FPM pool over FastCGI by requesting its ping path
// (default /ping) and expecting "pong". It speaks FastCGI natively (no driver).
// The pool must have `ping.path = /ping` enabled. No authentication.
type fpmProtocol struct{}

func (fpmProtocol) Name() string       { return "fpm" }
func (fpmProtocol) DefaultPort() int   { return 9000 } // FPM over TCP
func (fpmProtocol) RequiresUser() bool { return false }

// Probe dials the FPM socket (Unix when Socket is set, else TCP host:port) and
// performs a FastCGI /ping. When a status path is configured (cfg.Query, from
// the check's status_path — pm.status_path in the pool config), it additionally
// fetches the pool status page and exposes its metrics in Extra.
func (fpmProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := dialDeadline(ctx, cfg, 9000)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	res, err := fpmHandshake(c, "/ping")
	if err != nil {
		return res, err
	}
	// Pool status is best effort and needs a fresh connection (FastCGI closed
	// the first after the ping). Its metrics (active/idle processes, listen
	// queue, slow requests, …) become assertable via expect:.
	if cfg.Query != "" {
		if sc, derr := dialDeadline(ctx, cfg, 9000); derr == nil {
			defer func() { _ = sc.Close() }()
			if stdout, _, rerr := fpmRequest(sc, cfg.Query, "json"); rerr == nil {
				mergeFPMStatus(res.Extra, stdout)
			}
		}
	}
	return res, nil
}

// fpmHandshake requests pingPath and verifies the response contains "pong".
func fpmHandshake(rw io.ReadWriter, pingPath string) (Result, error) {
	stdout, stderr, err := fpmRequest(rw, pingPath, "")
	if err != nil {
		return Result{}, err
	}
	if !strings.Contains(stdout, respPong) {
		detail := strings.TrimSpace(stdout)
		if detail == "" {
			detail = strings.TrimSpace(stderr)
		}
		return Result{}, fmt.Errorf("php-fpm did not answer pong on %s (enable ping.path): %s", pingPath, detail)
	}
	return Result{Extra: map[string]string{extraPing: respPong}}, nil
}

// fpmRequest performs one FastCGI GET for script (with an optional query string)
// and returns the response STDOUT and STDERR. The connection is single-use:
// FCGI_BEGIN_REQUEST is sent with flags 0, so the server closes it afterwards.
func fpmRequest(rw io.ReadWriter, script, query string) (stdout, stderr string, err error) {
	// FCGI_BEGIN_REQUEST: role RESPONDER, flags 0 (close after request).
	if err := writeFCGIRecord(rw, fcgiBeginRequest, []byte{0, fcgiResponder, 0, 0, 0, 0, 0, 0}); err != nil {
		return "", "", err
	}
	uri := script
	if query != "" {
		uri += "?" + query
	}
	params := encodeFCGIParams([][2]string{
		{"SCRIPT_NAME", script},
		{"SCRIPT_FILENAME", script},
		{"REQUEST_METHOD", "GET"},
		{"REQUEST_URI", uri},
		{"QUERY_STRING", query},
		{"SERVER_PROTOCOL", "HTTP/1.1"},
		{"GATEWAY_INTERFACE", "CGI/1.1"},
		{"SERVER_SOFTWARE", "sermo"},
	})
	if err := writeFCGIRecord(rw, fcgiParams, params); err != nil {
		return "", "", err
	}
	if err := writeFCGIRecord(rw, fcgiParams, nil); err != nil { // end of params
		return "", "", err
	}
	if err := writeFCGIRecord(rw, fcgiStdin, nil); err != nil { // empty body, end of stdin
		return "", "", err
	}
	return readFCGIResponse(rw)
}

// mergeFPMStatus parses a php-fpm status page (pm.status_path requested with
// ?json) out of a FastCGI STDOUT response and copies the pool metrics into
// extra. Best effort: a non-JSON body (status path not enabled) leaves extra
// untouched.
func mergeFPMStatus(extra map[string]string, stdout string) {
	body := stdout
	if _, after, ok := strings.Cut(stdout, "\r\n\r\n"); ok { // strip CGI headers
		body = after
	} else if _, after, ok := strings.Cut(stdout, "\n\n"); ok {
		body = after
	}
	var s struct {
		Pool               string `json:"pool"`
		ProcessManager     string `json:"process manager"`
		StartSince         int    `json:"start since"`
		AcceptedConn       int    `json:"accepted conn"`
		ListenQueue        int    `json:"listen queue"`
		MaxListenQueue     int    `json:"max listen queue"`
		IdleProcesses      int    `json:"idle processes"`
		ActiveProcesses    int    `json:"active processes"`
		TotalProcesses     int    `json:"total processes"`
		MaxActiveProcesses int    `json:"max active processes"`
		MaxChildrenReached int    `json:"max children reached"`
		SlowRequests       int    `json:"slow requests"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(body)), &s) != nil {
		return
	}
	putIfSet(extra, "pool", s.Pool)
	putIfSet(extra, "process_manager", s.ProcessManager)
	for k, v := range map[string]int{
		"active_processes": s.ActiveProcesses, "idle_processes": s.IdleProcesses,
		"total_processes": s.TotalProcesses, "max_active_processes": s.MaxActiveProcesses,
		"listen_queue": s.ListenQueue, "max_listen_queue": s.MaxListenQueue,
		"max_children_reached": s.MaxChildrenReached, "slow_requests": s.SlowRequests,
		"accepted_conn": s.AcceptedConn, "uptime_seconds": s.StartSince,
	} {
		extra[k] = strconv.Itoa(v)
	}
}

// writeFCGIRecord writes one FastCGI record (request id 1, no padding). content
// must be < 65536 bytes, which holds for our small params and empty streams.
func writeFCGIRecord(w io.Writer, recType byte, content []byte) error {
	n := len(content)
	header := []byte{
		fcgiVersion1, recType,
		0, fcgiRequestID, // request id (big-endian) = 1
		byte(n >> 8), byte(n), // content length (big-endian)
		0, 0, // padding length, reserved
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	if n > 0 {
		if _, err := w.Write(content); err != nil {
			return err
		}
	}
	return nil
}

// encodeFCGIParams encodes name/value pairs as a FCGI_PARAMS body. Lengths < 128
// use one byte; longer use the 4-byte form (high bit set).
func encodeFCGIParams(pairs [][2]string) []byte {
	var b bytes.Buffer
	writeLen := func(n int) {
		if n < 128 {
			b.WriteByte(byte(n))
			return
		}
		b.WriteByte(byte(n>>24) | 0x80)
		b.WriteByte(byte(n >> 16))
		b.WriteByte(byte(n >> 8))
		b.WriteByte(byte(n))
	}
	for _, kv := range pairs {
		writeLen(len(kv[0]))
		writeLen(len(kv[1]))
		b.WriteString(kv[0])
		b.WriteString(kv[1])
	}
	return b.Bytes()
}

// readFCGIResponse reads records until FCGI_END_REQUEST, returning the
// accumulated STDOUT and STDERR.
func readFCGIResponse(r io.Reader) (stdout, stderr string, err error) {
	var out, errOut bytes.Buffer
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			return "", "", err
		}
		recType := header[1]
		clen := int(header[4])<<8 | int(header[5])
		plen := int(header[6])
		body := make([]byte, clen+plen)
		if len(body) > 0 {
			if _, err := io.ReadFull(r, body); err != nil {
				return "", "", err
			}
		}
		content := body[:clen]
		switch recType {
		case fcgiStdout:
			out.Write(content)
		case fcgiStderr:
			errOut.Write(content)
		case fcgiEndRequest:
			return out.String(), errOut.String(), nil
		}
	}
}
