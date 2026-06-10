package conn

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

func init() { Register(nutProtocol{}, "ups", "upsd") }

// nutProtocol probes a NUT (Network UPS Tools) upsd server over its line-based
// TCP protocol (default 3493). Anonymously it asks `VER` for the server version;
// with credentials it authenticates with USERNAME/PASSWORD and, when a UPS name
// is given (the `query` field), LOGIN-s to it to verify access. When a UPS is
// named it also reads `ups.status` into the result so an operator can assert it
// with `expect`. TLS, when requested, is implicit (operator sets `tls: true`);
// upsd's STARTTLS upgrade is not used, matching the other natively-probed
// protocols.
type nutProtocol struct{}

// Name returns the canonical type token.
func (nutProtocol) Name() string { return "nut" }

// DefaultPort is upsd's IANA port.
func (nutProtocol) DefaultPort() int { return 3493 }

// RequiresUser reports that authentication is optional (an anonymous VER probe is
// a valid liveness check).
func (nutProtocol) RequiresUser() bool { return false }

// Probe dials upsd and runs the handshake.
func (nutProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	port := cfg.Port
	if port == 0 {
		port = 3493
	}
	c, err := dialConn(ctx, cfg, port)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	return nutHandshake(c, cfg)
}

// nutHandshake runs the upsd exchange over rw (split out so it is testable with a
// pipe): VER for liveness/version, optional USERNAME/PASSWORD/LOGIN, and an
// optional ups.status read when a UPS is named.
func nutHandshake(rw io.ReadWriter, cfg Config) (Result, error) {
	br := bufio.NewReader(rw)

	if err := writeNUT(rw, "VER"); err != nil {
		return Result{}, err
	}
	ver, err := readNUTLine(br)
	if err != nil {
		return Result{}, fmt.Errorf("VER: %w", err)
	}
	if strings.HasPrefix(ver, "ERR") {
		return Result{}, fmt.Errorf("VER: %s", nutErr(ver))
	}
	res := Result{Version: nutVersion(ver), Extra: map[string]string{"server": ver}}

	ups := cfg.Query

	// USERNAME/PASSWORD are not validated by upsd on their own; LOGIN to the named
	// UPS is what actually verifies the credentials and access.
	if cfg.User != "" {
		if err := nutCmdOK(rw, br, "USERNAME "+cfg.User); err != nil {
			return Result{}, fmt.Errorf("username: %w", err)
		}
		if err := nutCmdOK(rw, br, "PASSWORD "+cfg.Password); err != nil {
			return Result{}, fmt.Errorf("password: %w", err)
		}
		if ups != "" {
			if err := nutCmdOK(rw, br, "LOGIN "+ups); err != nil {
				return Result{}, fmt.Errorf("login: %w", err)
			}
			res.Extra["login"] = ups
		}
	}

	// When a UPS is named, expose ups.status so an operator can `expect` it. An
	// UNKNOWN-UPS error means the configured UPS does not exist (a real problem);
	// other errors (the variable is unsupported) are tolerated.
	if ups != "" {
		if err := writeNUT(rw, "GET VAR "+ups+" ups.status"); err != nil {
			return Result{}, err
		}
		line, err := readNUTLine(br)
		if err != nil {
			return Result{}, fmt.Errorf("get ups.status: %w", err)
		}
		switch {
		case strings.HasPrefix(line, "VAR "):
			if v, ok := parseNUTVar(line); ok {
				res.Extra["ups.status"] = v
			}
		case strings.HasPrefix(line, "ERR"):
			if e := nutErr(line); strings.Contains(e, "UNKNOWN-UPS") {
				return Result{}, fmt.Errorf("UPS %q: %s", ups, e)
			}
		}
	}

	_ = writeNUT(rw, "LOGOUT") // best effort
	return res, nil
}

// writeNUT sends a single newline-terminated command.
func writeNUT(w io.Writer, cmd string) error {
	_, err := io.WriteString(w, cmd+"\n")
	return err
}

// readNUTLine reads one CRLF/LF-terminated reply line.
func readNUTLine(br *bufio.Reader) (string, error) {
	s, err := br.ReadString('\n')
	if err != nil && s == "" {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

// nutCmdOK sends cmd and requires an `OK` reply, mapping `ERR <reason>` to an error.
func nutCmdOK(w io.Writer, br *bufio.Reader, cmd string) error {
	if err := writeNUT(w, cmd); err != nil {
		return err
	}
	line, err := readNUTLine(br)
	if err != nil {
		return err
	}
	switch {
	case strings.HasPrefix(line, "OK"):
		return nil
	case strings.HasPrefix(line, "ERR"):
		return errors.New(nutErr(line))
	default:
		return fmt.Errorf("unexpected reply: %s", line)
	}
}

// nutErr returns the reason after the ERR token.
func nutErr(line string) string {
	return strings.TrimSpace(strings.TrimPrefix(line, "ERR"))
}

// nutVersion extracts the version from a `VER` reply such as
// "Network UPS Tools upsd 2.8.0 - http://…", falling back to the first token of
// the whole line when the prefix is absent.
func nutVersion(line string) string {
	v := strings.TrimPrefix(line, "Network UPS Tools upsd ")
	if i := strings.IndexByte(v, ' '); i >= 0 {
		v = v[:i]
	}
	return v
}

// parseNUTVar extracts the quoted value from a `VAR <ups> <var> "<value>"` reply.
func parseNUTVar(line string) (string, bool) {
	i := strings.IndexByte(line, '"')
	j := strings.LastIndexByte(line, '"')
	if i < 0 || j <= i {
		return "", false
	}
	return line[i+1 : j], true
}
