package conn

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/textproto"
)

func init() { Register(ftpProtocol{}) }

// ftpProtocol probes an FTP server natively (RFC 959). With no credentials it is
// an anonymous connectivity check (verify the server greets 220). With a
// user/password it performs USER/PASS login; a password with no user logs in as
// "anonymous". TLS is implicit (FTPS) when enabled — use port 990.
type ftpProtocol struct{}

func (ftpProtocol) Name() string       { return ProtocolNameFTP }
func (ftpProtocol) DefaultPort() int   { return defaultPortFTP }
func (ftpProtocol) RequiresUser() bool { return false }

func (ftpProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	return probeBanner(ctx, cfg, defaultPortFTP, ftpHandshake)
}

// ftpHandshake reads the 220 greeting, logs in with USER/PASS when credentials
// are supplied, and quits. FTP shares SMTP's multi-line reply format.
func ftpHandshake(rw io.ReadWriter, cfg Config) (Result, error) {
	tp := textproto.NewReader(bufio.NewReader(rw))
	code, greeting, err := tp.ReadResponse(0)
	if err != nil {
		return Result{}, err
	}
	res := Result{Extra: map[string]string{extraGreeting: greeting}}
	if code != 220 {
		return Result{}, fmt.Errorf("unexpected greeting: %d %s", code, greeting)
	}

	if cfg.User != "" || cfg.Password != "" {
		user := cfg.User
		if user == "" {
			user = "anonymous"
		}
		if _, err := fmt.Fprintf(rw, "USER %s\r\n", user); err != nil {
			return Result{}, err
		}
		code, text, err := tp.ReadResponse(0)
		if err != nil {
			return Result{}, err
		}
		switch {
		case code == 230: // logged in, no password needed
		case code == 331 || code == 332: // password (or account) required
			if _, err := fmt.Fprintf(rw, "PASS %s\r\n", cfg.Password); err != nil {
				return Result{}, err
			}
			code, text, err = tp.ReadResponse(0)
			if err != nil {
				return Result{}, err
			}
			if code != 230 {
				return Result{}, fmt.Errorf("login failed: %d %s", code, text)
			}
		default:
			return Result{}, fmt.Errorf("USER refused: %d %s", code, text)
		}
	}

	_, _ = fmt.Fprint(rw, "QUIT\r\n") // best effort
	return res, nil
}
