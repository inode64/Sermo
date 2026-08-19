package conn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

const quicTestProtocol = "sermo-quic-test"

func TestBindQUICDialer(t *testing.T) {
	serverTLS := quicTestTLS(t)
	packetConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = packetConn.Close() })

	listener, err := quic.Listen(packetConn, serverTLS, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	accepted := make(chan error, 1)
	go func() {
		serverConn, acceptErr := listener.Accept(ctx)
		if acceptErr == nil {
			acceptErr = serverConn.CloseWithError(0, "test complete")
		}
		accepted <- acceptErr
	}()

	clientTLS := &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{quicTestProtocol},
	}
	clientConn, err := BindQUICDialer("")(ctx, packetConn.LocalAddr().String(), clientTLS, nil)
	if err != nil {
		t.Fatalf("dial QUIC: %v", err)
	}
	if err := clientConn.CloseWithError(0, "test complete"); err != nil {
		t.Fatalf("close client connection: %v", err)
	}
	if err := <-accepted; err != nil {
		t.Fatalf("accept QUIC: %v", err)
	}
}

func TestBindQUICDialerBadInterface(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	clientTLS := &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{quicTestProtocol},
	}
	if clientConn, err := BindQUICDialer("sermo-nonexistent0")(
		ctx,
		"127.0.0.1:443",
		clientTLS,
		nil,
	); err == nil {
		_ = clientConn.CloseWithError(0, "unexpected connection")
		t.Fatal("dialing QUIC through a bogus interface must fail")
	}
}

func quicTestTLS(t *testing.T) *tls.Config {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: privateKey}
	return &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{quicTestProtocol},
	}
}
