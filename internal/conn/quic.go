package conn

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"

	"github.com/quic-go/quic-go"
)

// BindQUICDialer returns an HTTP/3-compatible QUIC dial function whose UDP
// socket egresses through iface. It fails if binding is requested but cannot be
// applied; it never falls back to the host's default route.
func BindQUICDialer(iface string) func(
	context.Context,
	string,
	*tls.Config,
	*quic.Config,
) (*quic.Conn, error) {
	listenConfig := BindListenConfig(iface)
	return func(
		ctx context.Context,
		address string,
		tlsConfig *tls.Config,
		quicConfig *quic.Config,
	) (*quic.Conn, error) {
		remote, network, local, err := resolveQUICAddress(ctx, address)
		if err != nil {
			return nil, err
		}
		packetConn, err := listenConfig.ListenPacket(ctx, network, local)
		if err != nil {
			return nil, fmt.Errorf("open QUIC socket on interface %q: %w", iface, err)
		}
		transport := &quic.Transport{Conn: packetConn}
		quicConn, err := transport.Dial(ctx, remote, tlsConfig, quicConfig)
		if err != nil {
			_ = transport.Close()
			_ = packetConn.Close()
			return nil, fmt.Errorf("dial QUIC %s on interface %q: %w", address, iface, err)
		}
		go closeQUICSocket(quicConn, transport, packetConn)
		return quicConn, nil
	}
}

func resolveQUICAddress(ctx context.Context, address string) (*net.UDPAddr, string, string, error) {
	host, service, err := net.SplitHostPort(address)
	if err != nil {
		return nil, "", "", fmt.Errorf("parse QUIC address %q: %w", address, err)
	}
	port, err := net.DefaultResolver.LookupPort(ctx, networkUDP, service)
	if err != nil {
		return nil, "", "", fmt.Errorf("resolve QUIC port %q: %w", service, err)
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, "", "", fmt.Errorf("resolve QUIC host %q: %w", host, err)
	}
	if len(addresses) == 0 {
		return nil, "", "", fmt.Errorf("resolve QUIC host %q: no addresses", host)
	}

	remote := &net.UDPAddr{IP: addresses[0].IP, Port: port, Zone: addresses[0].Zone}
	if remote.IP.To4() != nil {
		return remote, "udp4", "0.0.0.0:0", nil
	}
	return remote, "udp6", "[::]:0", nil
}

func closeQUICSocket(quicConn *quic.Conn, transport *quic.Transport, packetConn net.PacketConn) {
	<-quicConn.Context().Done()
	_ = transport.Close()
	_ = packetConn.Close()
}
