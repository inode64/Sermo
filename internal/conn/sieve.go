package conn

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
)

func init() { Register(sieveProtocol{}, "managesieve") }

// sieveProtocol probes a ManageSieve server natively (RFC 5804). On connect the
// server sends a greeting: capability lines (quoted name/value pairs) terminated
// by an OK/NO/BYE response. Reading it and seeing the final OK proves the server
// is up and speaking ManageSieve; the IMPLEMENTATION capability gives the server
// banner/version. No auth (the greeting precedes authentication). `tls` enables
// implicit TLS.
type sieveProtocol struct{}

func (sieveProtocol) Name() string       { return "sieve" }
func (sieveProtocol) DefaultPort() int   { return 4190 }
func (sieveProtocol) RequiresUser() bool { return false }

func (sieveProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	port := cfg.Port
	if port == 0 {
		port = 4190
	}
	c, err := dialConn(ctx, cfg, port)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}

	br := bufio.NewReader(c)
	impl := ""
	for i := 0; i < 64; i++ {
		line, rerr := br.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line != "" {
			upper := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(upper, "OK"):
				extra := map[string]string{"greeting": line}
				if impl != "" {
					extra["implementation"] = impl
				}
				return Result{Version: impl, Extra: extra}, nil
			case strings.HasPrefix(upper, "NO"), strings.HasPrefix(upper, "BYE"):
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
	if len(parts) >= 4 && parts[1] == "IMPLEMENTATION" {
		return parts[3], true
	}
	return "", false
}
