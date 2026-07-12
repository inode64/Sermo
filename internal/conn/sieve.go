package conn

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
)

func init() { Register(sieveProtocol{}, protocolAliasManageSieve) }

const (
	sieveCapabilityImplementation = "IMPLEMENTATION"
	sieveCapabilityMinParts       = 4
	sieveCapabilityNameIndex      = 1
	sieveCapabilityValueIndex     = 3
	sieveGreetingLimit            = 64
	sieveReplyBye                 = "BYE"
	sieveReplyNo                  = "NO"
	sieveReplyOK                  = "OK"
)

// sieveProtocol probes a ManageSieve server natively (RFC 5804). On connect the
// server sends a greeting: capability lines (quoted name/value pairs) terminated
// by an OK/NO/BYE response. Reading it and seeing the final OK proves the server
// is up and speaking ManageSieve; the IMPLEMENTATION capability gives the server
// banner/version. No auth (the greeting precedes authentication). `tls` enables
// implicit TLS.
type sieveProtocol struct{}

func (sieveProtocol) Name() string       { return ProtocolNameSieve }
func (sieveProtocol) DefaultPort() int   { return defaultPortSieve }
func (sieveProtocol) RequiresUser() bool { return false }

func (sieveProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := dialDeadline(ctx, cfg, defaultPortSieve)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()

	br := bufio.NewReader(c)
	impl := ""
	for range sieveGreetingLimit {
		line, rerr := br.ReadString(protocolLineBreak)
		line = strings.TrimRight(line, protocolTrimCRLF)
		if line != "" {
			upper := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(upper, sieveReplyOK):
				extra := map[string]string{extraGreeting: line}
				if impl != "" {
					extra[extraImplementation] = impl
				}
				return Result{Version: impl, Extra: extra}, nil
			case strings.HasPrefix(upper, sieveReplyNo), strings.HasPrefix(upper, sieveReplyBye):
				return Result{}, fmt.Errorf("ManageSieve greeting refused: %s", line)
			default:
				if v, ok := sieveImplementation(line); ok {
					impl = v
				}
			}
		}
		if rerr != nil {
			return Result{}, fmt.Errorf("ManageSieve greeting incomplete: %w", rerr)
		}
	}
	return Result{}, errors.New("no ManageSieve OK greeting")
}

// sieveImplementation extracts the value of an IMPLEMENTATION capability line
// (`"IMPLEMENTATION" "Dovecot …"` -> "Dovecot …").
func sieveImplementation(line string) (string, bool) {
	parts := strings.Split(line, `"`)
	if len(parts) >= sieveCapabilityMinParts && parts[sieveCapabilityNameIndex] == sieveCapabilityImplementation {
		return parts[sieveCapabilityValueIndex], true
	}
	return "", false
}
