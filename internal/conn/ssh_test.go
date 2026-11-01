package conn

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSSHProbeAuthenticationBoundary(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name     string
		user     string
		password string
		wantAuth bool
		wantErr  bool
	}{
		{name: "anonymous"},
		{name: "user without password", user: "monitor"},
		{name: "password accepted", user: "monitor", password: "correct", wantAuth: true},
		{name: "password rejected", user: "monitor", password: "wrong", wantAuth: true, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			auth := make(chan struct{}, 1)
			done := make(chan struct{})
			server := &ssh.ServerConfig{
				NoClientAuth: tt.password == "",
				NoClientAuthCallback: func(ssh.ConnMetadata) (*ssh.Permissions, error) {
					auth <- struct{}{}
					return nil, nil
				},
				PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
					auth <- struct{}{}
					if string(password) != "correct" {
						return nil, errors.New("password rejected")
					}
					return nil, nil
				},
			}
			server.AddHostKey(signer)
			port := serveOnce(t, func(c net.Conn) {
				defer close(done)
				_ = c.SetDeadline(time.Now().Add(time.Second))
				conn, _, _, _ := ssh.NewServerConn(c, server)
				if conn != nil {
					_ = conn.Close()
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			res, err := (sshProtocol{}).Probe(ctx, Config{Host: "127.0.0.1", Port: port, User: tt.user, Password: tt.password})
			if (err != nil) != tt.wantErr {
				t.Fatalf("Probe() = %+v, %v; want error %v", res, err, tt.wantErr)
			}
			if err == nil && res.Extra[ExtraKeyFingerprint] != ssh.FingerprintSHA256(signer.PublicKey()) {
				t.Fatalf("fingerprint = %q, want verified server key", res.Extra[ExtraKeyFingerprint])
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("server handshake did not finish")
			}
			if got := len(auth) > 0; got != tt.wantAuth {
				t.Fatalf("authentication attempted = %v, want %v", got, tt.wantAuth)
			}
		})
	}
}

type mismatchedSSHSigner struct {
	ssh.Signer
	publicKey ssh.PublicKey
}

func (s mismatchedSSHSigner) PublicKey() ssh.PublicKey { return s.publicKey }

func TestSSHAnonymousProbeRejectsInvalidExchangeSignature(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	server := &ssh.ServerConfig{NoClientAuth: true}
	server.AddHostKey(mismatchedSSHSigner{Signer: signer, publicKey: key})
	port := serveOnce(t, func(c net.Conn) {
		_ = c.SetDeadline(time.Now().Add(time.Second))
		conn, _, _, _ := ssh.NewServerConn(c, server)
		if conn != nil {
			_ = conn.Close()
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	res, err := (sshProtocol{}).Probe(ctx, Config{Host: "127.0.0.1", Port: port})
	if err == nil || res.Extra[ExtraKeyFingerprint] != "" {
		t.Fatalf("Probe() = %+v, %v; want no trusted fingerprint for an invalid signature", res, err)
	}
}

func TestParseSSHBanner(t *testing.T) {
	proto, sw := parseSSHBanner("SSH-2.0-OpenSSH_9.6p1 Debian-1")
	if proto != "2.0" || sw != "OpenSSH_9.6p1 Debian-1" {
		t.Fatalf("parse = %q / %q", proto, sw)
	}
	if p, _ := parseSSHBanner("SSH-1.99-Server"); p != "1.99" {
		t.Fatalf("proto = %q", p)
	}
}

func TestPrefixConnReplaysThenReadsConn(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		_, _ = server.Write([]byte("CD"))
		server.Close()
	}()
	pc := &prefixConn{Conn: client, pre: bytes.NewReader([]byte("AB"))}

	buf := make([]byte, 2)
	n, _ := pc.Read(buf)
	if string(buf[:n]) != "AB" {
		t.Fatalf("first read = %q, want AB (the prefix)", buf[:n])
	}
	n, _ = pc.Read(buf)
	if string(buf[:n]) != "CD" {
		t.Fatalf("second read = %q, want CD (the conn)", buf[:n])
	}
}

func TestReadSSHBanner(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		// A pre-banner line (allowed by RFC 4253), then the SSH id, then kex bytes.
		_, _ = server.Write([]byte("hello there\r\nSSH-2.0-OpenSSH_9.6\r\n"))
		_, _ = server.Write([]byte{0x00, 0x01, 0x02}) // start of kex (must not be consumed)
	}()
	raw, banner, err := readSSHBanner(client)
	if err != nil {
		t.Fatal(err)
	}
	if banner != "SSH-2.0-OpenSSH_9.6" {
		t.Fatalf("banner = %q", banner)
	}
	if string(raw) != "hello there\r\nSSH-2.0-OpenSSH_9.6\r\n" {
		t.Fatalf("raw must include the pre-banner line for replay: %q", raw)
	}
}

func TestReadSSHBannerReportsServerRejection(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go func() {
		_, _ = server.Write([]byte("Not allowed at this time\r\n"))
		server.Close()
	}()

	raw, banner, err := readSSHBanner(client)
	if err == nil || !strings.Contains(err.Error(), `EOF after server message "Not allowed at this time"`) {
		t.Fatalf("error = %v, want the server's pre-banner rejection", err)
	}
	if banner != "" || string(raw) != "Not allowed at this time\r\n" {
		t.Fatalf("raw/banner = %q/%q, want the rejected pre-banner only", raw, banner)
	}
}

func TestSSHAdmissionRefusalIsBusy(t *testing.T) {
	for _, refusal := range []string{sshAdmissionRefused, sshMaxStartupsRefused} {
		t.Run(refusal, func(t *testing.T) {
			port := serveBanner(t, refusal+"\r\n", nil)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			res, err := (sshProtocol{}).Probe(ctx, Config{Host: "127.0.0.1", Port: port})
			if err != nil || !res.Busy || res.Failure == "" || res.Extra[ExtraKeyBusyReason] != refusal {
				t.Fatalf("probe = %+v, %v; want an explicit busy refusal", res, err)
			}
		})
	}
}

func TestSSHBannerBusyRequiresExactCompletedRefusal(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		err  error
		want bool
	}{
		{name: "refusal", raw: "Not allowed at this time\r\n", err: io.EOF, want: true},
		{name: "MaxStartups refusal", raw: "Exceeded MaxStartups\r\n", err: io.EOF, want: true},
		{name: "MaxStartups timeout", raw: "Exceeded MaxStartups\r\n", err: context.DeadlineExceeded},
		{name: "MaxStartups before a banner", raw: "Exceeded MaxStartups\r\nSSH-2.0-test\r\n", err: io.EOF},
		{name: "timeout", raw: "Not allowed at this time\r\n", err: context.DeadlineExceeded},
		{name: "cancelled", raw: "Not allowed at this time\r\n", err: context.Canceled},
		{name: "no error", raw: "Not allowed at this time\r\n"},
		{name: "unknown refusal", raw: "Access denied\r\n", err: io.EOF},
		{name: "empty close", err: io.EOF},
		{name: "valid banner", raw: "Not allowed at this time\r\nSSH-2.0-test\r\n", err: io.EOF},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, got := sshBannerBusy([]byte(tt.raw), tt.err)
			if got != tt.want {
				t.Fatalf("busy = %v, want %v", got, tt.want)
			}
		})
	}
}
