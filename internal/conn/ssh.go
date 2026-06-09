package conn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

func init() { Register(sshProtocol{}) }

// sshProtocol probes an SSH server using golang.org/x/crypto/ssh. With no
// credentials it is an anonymous check: it completes the key exchange to capture
// the server's host key (fingerprint + algorithm) and identification banner —
// authentication then fails, which is expected and ignored. With a
// user/password it requires authentication to succeed. The host-key fingerprint
// is exposed so a watch with `on_change: true` alerts when it changes.
//
// SSH carries its own transport encryption, so the generic `tls` field is not
// used.
type sshProtocol struct{}

func (sshProtocol) Name() string       { return "ssh" }
func (sshProtocol) DefaultPort() int   { return 22 }
func (sshProtocol) RequiresUser() bool { return false }

func (sshProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}

	// Read the server identification banner ourselves (byte by byte, so no kex
	// bytes are consumed) and replay it to the ssh handshake via prefixConn.
	raw, banner, err := readSSHBanner(c)
	if err != nil {
		return Result{}, fmt.Errorf("read ssh banner: %w", err)
	}
	if !strings.HasPrefix(banner, "SSH-") {
		return Result{}, fmt.Errorf("not an SSH server: %q", banner)
	}

	var hostKey ssh.PublicKey
	user := cfg.User
	if user == "" {
		user = "anonymous"
	}
	var auth []ssh.AuthMethod
	if cfg.Password != "" {
		auth = []ssh.AuthMethod{ssh.Password(cfg.Password)}
	}
	clientCfg := &ssh.ClientConfig{
		User: user,
		Auth: auth,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			hostKey = key // captured during kex, before authentication
			return nil
		},
	}

	pc := &prefixConn{Conn: c, pre: bytes.NewReader(raw)}
	sshConn, chans, reqs, hsErr := ssh.NewClientConn(pc, addr, clientCfg)
	authed := hsErr == nil
	if authed {
		go ssh.DiscardRequests(reqs)
		go func() {
			for ch := range chans {
				_ = ch.Reject(ssh.Prohibited, "")
			}
		}()
		_ = sshConn.Close()
	}

	if hostKey == nil {
		// Key exchange never reached the host-key step: not an SSH server we can
		// talk to, or the transport handshake failed.
		return Result{}, fmt.Errorf("ssh handshake: %v", hsErr)
	}
	requireAuth := cfg.User != "" || cfg.Password != ""
	if !sshSucceeds(true, authed, requireAuth) {
		return Result{}, fmt.Errorf("authentication failed: %v", hsErr)
	}

	proto, software := parseSSHBanner(banner)
	return Result{
		Version: software,
		Extra: map[string]string{
			"fingerprint":    ssh.FingerprintSHA256(hostKey),
			"host_key_algo":  hostKey.Type(),
			"server_version": banner,
			"protocol":       proto,
		},
	}, nil
}

// sshSucceeds reports the overall outcome: the host key must be captured (the
// server is up and speaking SSH), and when credentials are required the
// authentication must also succeed.
func sshSucceeds(hostKeyCaptured, authed, requireAuth bool) bool {
	return hostKeyCaptured && (!requireAuth || authed)
}

// parseSSHBanner splits an SSH identification string ("SSH-2.0-OpenSSH_9.6 …")
// into its protocol version and software/comment portion.
func parseSSHBanner(banner string) (protocol, software string) {
	rest := strings.TrimPrefix(banner, "SSH-")
	proto, sw, ok := strings.Cut(rest, "-")
	if !ok {
		return rest, ""
	}
	return proto, sw
}

// readSSHBanner reads the server identification, returning the raw bytes (to
// replay to the ssh handshake) and the trimmed "SSH-…" line. RFC 4253 allows the
// server to send other lines before the identification, so it reads (and keeps,
// for replay) until a line beginning with "SSH-". It reads one byte at a time so
// it never consumes the key-exchange bytes that follow.
func readSSHBanner(c net.Conn) (raw []byte, banner string, err error) {
	var line []byte
	one := make([]byte, 1)
	for {
		if len(raw) > 16*1024 {
			return raw, "", errors.New("ssh banner too long")
		}
		n, rerr := c.Read(one)
		if n == 1 {
			raw = append(raw, one[0])
			if one[0] == '\n' {
				s := strings.TrimRight(string(line), "\r\n")
				line = line[:0]
				if strings.HasPrefix(s, "SSH-") {
					return raw, s, nil
				}
				continue // a pre-identification line; keep reading
			}
			line = append(line, one[0])
		}
		if rerr != nil {
			return raw, "", rerr
		}
	}
}

// prefixConn is a net.Conn that first serves bytes from pre (the already-read
// banner) and then reads from the underlying connection.
type prefixConn struct {
	net.Conn
	pre *bytes.Reader
}

func (c *prefixConn) Read(p []byte) (int, error) {
	if c.pre != nil && c.pre.Len() > 0 {
		return c.pre.Read(p)
	}
	return c.Conn.Read(p)
}
