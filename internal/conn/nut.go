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

// nutInterestingVars are the upsd variables exposed in the probe Result (and so
// to `expect`, hooks as SERMO_*, and the web detail) when a UPS is selected. They
// cover the operationally useful signals — power/battery state, charge and
// runtime, load, voltages, temperature — plus identity. `ups.status` is also
// mirrored into the change fingerprint so `on_change` alerts on state transitions
// (e.g. OL -> OB DISCHRG -> OB LB).
var nutInterestingVars = []string{
	"ups.status",
	"ups.load",
	"ups.temperature",
	"ups.power",
	"ups.realpower",
	"battery.charge",
	"battery.charge.low",
	"battery.runtime",
	"battery.runtime.low",
	"battery.voltage",
	"input.voltage",
	"input.frequency",
	"output.voltage",
	"ups.mfr",
	"ups.model",
}

// nutProtocol probes a NUT (Network UPS Tools) upsd server over its line-based
// TCP protocol (default 3493). Anonymously it asks `VER` for the server version;
// with credentials it authenticates with USERNAME/PASSWORD and, when a UPS is
// selected, LOGIN-s to it to verify access. For the selected UPS it reads the
// device variables (status, battery charge/runtime, load, voltages, …) into the
// result so an operator can alert on them with `expect` (e.g. battery.charge) or
// on state changes with `on_change`. The UPS is the `ups` field, or — when a
// single UPS is configured on the server — auto-detected. TLS, when requested, is
// implicit (operator sets `tls: true`); upsd's STARTTLS upgrade is not used,
// matching the other natively-probed protocols.
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
// pipe): VER for liveness/version, UPS selection (explicit `query`/`ups` or a
// single auto-detected device), optional USERNAME/PASSWORD/LOGIN, and the device
// variables for the selected UPS.
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
	if ups == "" {
		// Auto-detect a single configured UPS so the common one-UPS host needs no
		// `ups` field. Zero or several UPSes leave the check at VER liveness.
		if names, err := nutListUPS(rw, br); err == nil && len(names) == 1 {
			ups = names[0]
		}
	}

	// USERNAME/PASSWORD are not validated by upsd on their own; LOGIN to the
	// selected UPS is what actually verifies the credentials and access.
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

	if ups != "" {
		res.Extra["ups"] = ups
		vars, err := nutListVars(rw, br, ups)
		if err != nil {
			return Result{}, fmt.Errorf("list vars: %w", err)
		}
		for _, key := range nutInterestingVars {
			if v, ok := vars[key]; ok {
				res.Extra[key] = v
			}
		}
		if status, ok := vars["ups.status"]; ok {
			res.Extra["fingerprint"] = status // drives on_change (state transitions)
		}
	}

	_ = writeNUT(rw, "LOGOUT") // best effort
	return res, nil
}

// nutListUPS returns the UPS names from `LIST UPS`.
func nutListUPS(rw io.ReadWriter, br *bufio.Reader) ([]string, error) {
	if err := writeNUT(rw, "LIST UPS"); err != nil {
		return nil, err
	}
	first, err := readNUTLine(br)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(first, "ERR") {
		return nil, errors.New(nutErr(first))
	}
	if !strings.HasPrefix(first, "BEGIN LIST UPS") {
		return nil, fmt.Errorf("unexpected reply: %s", first)
	}
	var names []string
	for {
		line, err := readNUTLine(br)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(line, "END LIST UPS") {
			return names, nil
		}
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "UPS" {
			names = append(names, f[1])
		}
	}
}

// nutListVars returns every variable for ups from `LIST VAR <ups>`.
func nutListVars(rw io.ReadWriter, br *bufio.Reader, ups string) (map[string]string, error) {
	if err := writeNUT(rw, "LIST VAR "+ups); err != nil {
		return nil, err
	}
	first, err := readNUTLine(br)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(first, "ERR") {
		return nil, errors.New(nutErr(first))
	}
	if !strings.HasPrefix(first, "BEGIN LIST VAR") {
		return nil, fmt.Errorf("unexpected reply: %s", first)
	}
	vars := map[string]string{}
	for {
		line, err := readNUTLine(br)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(line, "END LIST VAR") {
			return vars, nil
		}
		if name, val, ok := parseNUTVarLine(line); ok {
			vars[name] = val
		}
	}
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

// parseNUTVarLine parses a `VAR <ups> <var> "<value>"` reply into the variable
// name and its value.
func parseNUTVarLine(line string) (name, value string, ok bool) {
	if !strings.HasPrefix(line, "VAR ") {
		return "", "", false
	}
	q := strings.IndexByte(line, '"')
	if q < 0 {
		return "", "", false
	}
	head := strings.Fields(line[:q]) // VAR <ups> <var>
	if len(head) < 3 {
		return "", "", false
	}
	v, ok := parseNUTVar(line)
	if !ok {
		return "", "", false
	}
	return head[2], v, true
}

// parseNUTVar extracts the quoted value from a `VAR …"<value>"` reply.
func parseNUTVar(line string) (string, bool) {
	i := strings.IndexByte(line, '"')
	j := strings.LastIndexByte(line, '"')
	if i < 0 || j <= i {
		return "", false
	}
	return line[i+1 : j], true
}
