package conn

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

func init() { Register(nutProtocol{}, protocolAliasUPS, protocolAliasUPSD) }

const nutVarUPSStatus = "ups.status"

const (
	nutCmdListUPS        = "LIST UPS"
	nutCmdListVarPrefix  = "LIST VAR "
	nutCmdLoginPrefix    = "LOGIN "
	nutCmdLogout         = "LOGOUT"
	nutCmdPasswordPrefix = "PASSWORD "
	nutCmdUsernamePrefix = "USERNAME "
	nutCmdVER            = "VER"
	nutLineTerminator    = "\n"
	nutReplyBeginListUPS = "BEGIN LIST UPS"
	nutReplyBeginListVar = "BEGIN LIST VAR"
	nutReplyEndListUPS   = "END LIST UPS"
	nutReplyEndListVar   = "END LIST VAR"
	nutReplyERR          = "ERR"
	nutReplyOK           = "OK"
	nutReplyUPSToken     = "UPS"
	nutReplyVarPrefix    = "VAR "
	nutVersionPrefix     = "Network UPS Tools upsd "
	nutSingleUPSCount    = 1
	nutSingleUPSIndex    = 0
	nutUPSLineMinFields  = 2
	nutUPSLineTypeIndex  = 0
	nutUPSLineNameIndex  = 1
	nutVarHeadMinFields  = 3
	nutVarNameIndex      = 2
)

// nutInterestingVars are the upsd variables exposed in the probe Result (and so
// to `expect`, hooks as SERMO_*, and the web detail) when a UPS is selected. They
// cover the operationally useful signals — power/battery state, charge and
// runtime, load, voltages, temperature — plus identity. `ups.status` is also
// mirrored into the change fingerprint so `on_change` alerts on state transitions
// (e.g. OL -> OB DISCHRG -> OB LB).
var nutInterestingVars = []string{
	nutVarUPSStatus,
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
func (nutProtocol) Name() string { return ProtocolNameNUT }

// DefaultPort is upsd's IANA port.
func (nutProtocol) DefaultPort() int { return defaultPortNUT }

// RequiresUser reports that authentication is optional (an anonymous VER probe is
// a valid liveness check).
func (nutProtocol) RequiresUser() bool { return false }

// Probe dials upsd and runs the handshake.
func (nutProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	return probeBanner(ctx, cfg, defaultPortNUT, nutHandshake)
}

// nutHandshake runs the upsd exchange over rw (split out so it is testable with a
// pipe): VER for liveness/version, UPS selection (explicit `query`/`ups` or a
// single auto-detected device), optional USERNAME/PASSWORD/LOGIN, and the device
// variables for the selected UPS.
func nutHandshake(rw io.ReadWriter, cfg Config) (Result, error) {
	br := bufio.NewReader(rw)

	if err := writeNUT(rw, nutCmdVER); err != nil {
		return Result{}, err
	}
	ver, err := readNUTLine(br)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", nutCmdVER, err)
	}
	if strings.HasPrefix(ver, nutReplyERR) {
		return Result{}, fmt.Errorf("%s: %s", nutCmdVER, nutErr(ver))
	}
	res := Result{Version: nutVersion(ver), Extra: map[string]string{ExtraKeyServer: ver}}

	ups := cfg.Query
	if ups == "" {
		// Auto-detect a single configured UPS so the common one-UPS host needs no
		// `ups` field. Zero or several UPSes leave the check at VER liveness.
		if names, err := nutListUPS(rw, br); err == nil && len(names) == nutSingleUPSCount {
			ups = names[nutSingleUPSIndex]
		}
	}

	// USERNAME/PASSWORD are not validated by upsd on their own; LOGIN to the
	// selected UPS is what actually verifies the credentials and access.
	if cfg.User != "" {
		if err := nutCmdOK(rw, br, nutCmdUsernamePrefix+cfg.User); err != nil {
			return Result{}, fmt.Errorf("username: %w", err)
		}
		if err := nutCmdOK(rw, br, nutCmdPasswordPrefix+cfg.Password); err != nil {
			return Result{}, fmt.Errorf("password: %w", err)
		}
		if ups != "" {
			if err := nutCmdOK(rw, br, nutCmdLoginPrefix+ups); err != nil {
				return Result{}, fmt.Errorf("login: %w", err)
			}
			res.Extra[extraLogin] = ups
		}
	}

	if ups != "" {
		res.Extra[extraUPS] = ups
		vars, err := nutListVars(rw, br, ups)
		if err != nil {
			return Result{}, fmt.Errorf("list vars: %w", err)
		}
		for _, key := range nutInterestingVars {
			if v, ok := vars[key]; ok {
				res.Extra[key] = v
			}
		}
		if status, ok := vars[nutVarUPSStatus]; ok {
			res.Extra[ExtraKeyFingerprint] = status // drives on_change (state transitions)
		}
	}

	_ = writeNUT(rw, nutCmdLogout) // best effort
	return res, nil
}

// nutListUPS returns the UPS names from `LIST UPS`.
func nutListUPS(rw io.ReadWriter, br *bufio.Reader) ([]string, error) {
	if err := writeNUT(rw, nutCmdListUPS); err != nil {
		return nil, err
	}
	first, err := readNUTLine(br)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(first, nutReplyERR) {
		return nil, errors.New(nutErr(first))
	}
	if !strings.HasPrefix(first, nutReplyBeginListUPS) {
		return nil, fmt.Errorf("unexpected reply: %s", first)
	}
	var names []string
	for {
		line, err := readNUTLine(br)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(line, nutReplyEndListUPS) {
			return names, nil
		}
		if f := strings.Fields(line); len(f) >= nutUPSLineMinFields && f[nutUPSLineTypeIndex] == nutReplyUPSToken {
			names = append(names, f[nutUPSLineNameIndex])
		}
	}
}

// nutListVars returns every variable for ups from `LIST VAR <ups>`.
func nutListVars(rw io.ReadWriter, br *bufio.Reader, ups string) (map[string]string, error) {
	if err := writeNUT(rw, nutCmdListVarPrefix+ups); err != nil {
		return nil, err
	}
	first, err := readNUTLine(br)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(first, nutReplyERR) {
		return nil, errors.New(nutErr(first))
	}
	if !strings.HasPrefix(first, nutReplyBeginListVar) {
		return nil, fmt.Errorf("unexpected reply: %s", first)
	}
	vars := map[string]string{}
	for {
		line, err := readNUTLine(br)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(line, nutReplyEndListVar) {
			return vars, nil
		}
		if name, val, ok := parseNUTVarLine(line); ok {
			vars[name] = val
		}
	}
}

// writeNUT sends a single newline-terminated command.
func writeNUT(w io.Writer, cmd string) error {
	_, err := io.WriteString(w, cmd+nutLineTerminator)
	return err
}

// readNUTLine reads one CRLF/LF-terminated reply line.
func readNUTLine(br *bufio.Reader) (string, error) {
	s, err := br.ReadString(protocolLineBreak)
	if err != nil && s == "" {
		return "", err
	}
	return strings.TrimRight(s, protocolTrimCRLF), nil
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
	case strings.HasPrefix(line, nutReplyOK):
		return nil
	case strings.HasPrefix(line, nutReplyERR):
		return errors.New(nutErr(line))
	default:
		return fmt.Errorf("unexpected reply: %s", line)
	}
}

// nutErr returns the reason after the ERR token.
func nutErr(line string) string {
	return strings.TrimSpace(strings.TrimPrefix(line, nutReplyERR))
}

// nutVersion extracts the version from a `VER` reply such as
// "Network UPS Tools upsd 2.8.0 - http://…", falling back to the first token of
// the whole line when the prefix is absent.
func nutVersion(line string) string {
	v := strings.TrimPrefix(line, nutVersionPrefix)
	if i := strings.IndexByte(v, ' '); i >= 0 {
		v = v[:i]
	}
	return v
}

// parseNUTVarLine parses a `VAR <ups> <var> "<value>"` reply into the variable
// name and its value.
func parseNUTVarLine(line string) (name, value string, ok bool) {
	if !strings.HasPrefix(line, nutReplyVarPrefix) {
		return "", "", false
	}
	before, _, ok0 := strings.Cut(line, "\"")
	if !ok0 {
		return "", "", false
	}
	head := strings.Fields(before) // VAR <ups> <var>
	if len(head) < nutVarHeadMinFields {
		return "", "", false
	}
	v, ok := parseNUTVar(line)
	if !ok {
		return "", "", false
	}
	return head[nutVarNameIndex], v, true
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
