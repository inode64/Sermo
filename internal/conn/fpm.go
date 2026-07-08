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

func init() { Register(fpmProtocol{}, protocolAliasPHPFPM) }

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

const (
	fpmDefaultPingPath  = "/ping"
	fpmStatusFormatJSON = "json"
)

const (
	fpmCGIHeaderSeparatorCRLF = "\r\n\r\n"
	fpmCGIHeaderSeparatorLF   = "\n\n"
	fpmGatewayInterfaceCGI11  = "CGI/1.1"
	fpmQuerySeparator         = "?"
	fpmRequestMethodGET       = "GET"
	fpmServerProtocolHTTP11   = "HTTP/1.1"
	fpmServerSoftware         = "sermo"
)

const (
	fpmParamGatewayInterface = "GATEWAY_INTERFACE"
	fpmParamQueryString      = "QUERY_STRING"
	fpmParamRequestMethod    = "REQUEST_METHOD"
	fpmParamRequestURI       = "REQUEST_URI"
	fpmParamScriptFilename   = "SCRIPT_FILENAME"
	fpmParamScriptName       = "SCRIPT_NAME"
	fpmParamServerProtocol   = "SERVER_PROTOCOL"
	fpmParamServerSoftware   = "SERVER_SOFTWARE"
)

const (
	fpmExtraAcceptedConn       = "accepted_conn"
	fpmExtraActiveProcesses    = "active_processes"
	fpmExtraIdleProcesses      = "idle_processes"
	fpmExtraListenQueue        = "listen_queue"
	fpmExtraMaxActiveProcesses = "max_active_processes"
	fpmExtraMaxChildrenReached = "max_children_reached"
	fpmExtraMaxListenQueue     = "max_listen_queue"
	fpmExtraSlowRequests       = "slow_requests"
	fpmExtraTotalProcesses     = "total_processes"
	fpmExtraUptimeSeconds      = "uptime_seconds"
)

const (
	fcgiBeginRequestFlagsClose        = 0
	fcgiBeginRequestRoleHigh          = 0
	fcgiContentLengthHighShift        = 8
	fcgiHeaderBytes                   = 8
	fcgiHeaderContentLengthHighOffset = 4
	fcgiHeaderContentLengthLowOffset  = 5
	fcgiHeaderPaddingLengthOffset     = 6
	fcgiHeaderTypeOffset              = 1
	fcgiLongParamLenFlag              = 0x80
	fcgiLongParamLenByte3Shift        = 24
	fcgiLongParamLenByte2Shift        = 16
	fcgiLongParamLenByte1Shift        = 8
	fcgiPaddingLengthNone             = 0
	fcgiParamNameIndex                = 0
	fcgiParamPairFields               = 2
	fcgiParamValueIndex               = 1
	fcgiRequestIDHigh                 = 0
	fcgiReservedByte                  = 0
	fcgiShortParamLenMax              = 128
)

type fcgiParam [fcgiParamPairFields]string

// fpmProtocol probes a PHP-FPM pool over FastCGI by requesting its ping path
// (default /ping) and expecting "pong". It speaks FastCGI natively (no driver).
// The pool must have `ping.path = /ping` enabled. No authentication.
type fpmProtocol struct{}

func (fpmProtocol) Name() string       { return ProtocolNameFPM }
func (fpmProtocol) DefaultPort() int   { return defaultPortFPM } // FPM over TCP
func (fpmProtocol) RequiresUser() bool { return false }

// Probe dials the FPM socket (Unix when Socket is set, else TCP host:port) and
// performs a FastCGI /ping. When a status path is configured (cfg.Query, from
// the check's status_path — pm.status_path in the pool config), it additionally
// fetches the pool status page and exposes its metrics in Extra.
func (fpmProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := dialDeadline(ctx, cfg, defaultPortFPM)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	res, err := fpmHandshake(c, fpmDefaultPingPath)
	if err != nil {
		return res, err
	}
	// Pool status is best effort and needs a fresh connection (FastCGI closed
	// the first after the ping). Its metrics (active/idle processes, listen
	// queue, slow requests, …) become assertable via expect:.
	if cfg.Query != "" {
		if sc, derr := dialDeadline(ctx, cfg, defaultPortFPM); derr == nil {
			defer func() { _ = sc.Close() }()
			if stdout, _, rerr := fpmRequest(sc, cfg.Query, fpmStatusFormatJSON); rerr == nil {
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
	if err := writeFCGIRecord(rw, fcgiBeginRequest, []byte{
		fcgiBeginRequestRoleHigh,
		fcgiResponder,
		fcgiBeginRequestFlagsClose,
		fcgiReservedByte,
		fcgiReservedByte,
		fcgiReservedByte,
		fcgiReservedByte,
		fcgiReservedByte,
	}); err != nil {
		return "", "", err
	}
	uri := script
	if query != "" {
		uri += fpmQuerySeparator + query
	}
	params := encodeFCGIParams([]fcgiParam{
		{fpmParamScriptName, script},
		{fpmParamScriptFilename, script},
		{fpmParamRequestMethod, fpmRequestMethodGET},
		{fpmParamRequestURI, uri},
		{fpmParamQueryString, query},
		{fpmParamServerProtocol, fpmServerProtocolHTTP11},
		{fpmParamGatewayInterface, fpmGatewayInterfaceCGI11},
		{fpmParamServerSoftware, fpmServerSoftware},
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
	if _, after, ok := strings.Cut(stdout, fpmCGIHeaderSeparatorCRLF); ok { // strip CGI headers
		body = after
	} else if _, after, ok := strings.Cut(stdout, fpmCGIHeaderSeparatorLF); ok {
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
	putIfSet(extra, extraPool, s.Pool)
	putIfSet(extra, extraProcessManager, s.ProcessManager)
	for k, v := range map[string]int{
		fpmExtraActiveProcesses:    s.ActiveProcesses,
		fpmExtraIdleProcesses:      s.IdleProcesses,
		fpmExtraTotalProcesses:     s.TotalProcesses,
		fpmExtraMaxActiveProcesses: s.MaxActiveProcesses,
		fpmExtraListenQueue:        s.ListenQueue,
		fpmExtraMaxListenQueue:     s.MaxListenQueue,
		fpmExtraMaxChildrenReached: s.MaxChildrenReached,
		fpmExtraSlowRequests:       s.SlowRequests,
		fpmExtraAcceptedConn:       s.AcceptedConn,
		fpmExtraUptimeSeconds:      s.StartSince,
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
		fcgiRequestIDHigh, fcgiRequestID,
		byte(n >> fcgiContentLengthHighShift), byte(n),
		fcgiPaddingLengthNone, fcgiReservedByte,
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
func encodeFCGIParams(pairs []fcgiParam) []byte {
	var b bytes.Buffer
	writeLen := func(n int) {
		if n < fcgiShortParamLenMax {
			b.WriteByte(byte(n))
			return
		}
		b.WriteByte(byte(n>>fcgiLongParamLenByte3Shift) | fcgiLongParamLenFlag)
		b.WriteByte(byte(n >> fcgiLongParamLenByte2Shift))
		b.WriteByte(byte(n >> fcgiLongParamLenByte1Shift))
		b.WriteByte(byte(n))
	}
	for _, kv := range pairs {
		writeLen(len(kv[fcgiParamNameIndex]))
		writeLen(len(kv[fcgiParamValueIndex]))
		b.WriteString(kv[fcgiParamNameIndex])
		b.WriteString(kv[fcgiParamValueIndex])
	}
	return b.Bytes()
}

// readFCGIResponse reads records until FCGI_END_REQUEST, returning the
// accumulated STDOUT and STDERR.
func readFCGIResponse(r io.Reader) (stdout, stderr string, err error) {
	var out, errOut bytes.Buffer
	header := make([]byte, fcgiHeaderBytes)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			return "", "", err
		}
		recType := header[fcgiHeaderTypeOffset]
		clen := int(header[fcgiHeaderContentLengthHighOffset])<<fcgiContentLengthHighShift |
			int(header[fcgiHeaderContentLengthLowOffset])
		plen := int(header[fcgiHeaderPaddingLengthOffset])
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
