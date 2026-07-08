package conn

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
)

func init() { Register(dhcpProtocol{}, protocolAliasDHCPD) }

// DHCP message format and option codes (RFC 2131 / RFC 2132).
const (
	dhcpOpBootRequest = 1
	dhcpOpBootReply   = 2
	dhcpHTypeEthernet = 1
	dhcpHLenEthernet  = 6
	dhcpFlagBroadcast = 0x8000
	dhcpServerPort    = 67
	dhcpClientPort    = 68

	dhcpMACLocalAdminBit = 0x02
	dhcpMACMulticastBit  = 0x01

	// DHCP message types (option 53).
	dhcpDiscover = 1
	dhcpOffer    = 2

	// DHCP options (RFC 2132).
	dhcpOptSubnetMask   = 1
	dhcpOptRouter       = 3
	dhcpOptLeaseTime    = 51
	dhcpOptMessageType  = 53
	dhcpOptServerID     = 54
	dhcpOptParamReqList = 55
	dhcpOptEnd          = 255

	dhcpModeBroadcast = "broadcast"
	dhcpModeUnicast   = "unicast"
	dhcpMessageOffer  = "offer"
)

const (
	dhcpPacketMinBytes            = 240
	dhcpOpOffset                  = 0
	dhcpHardwareTypeOffset        = 1
	dhcpHardwareLengthOffset      = 2
	dhcpXIDOffset                 = 4
	dhcpXIDEndOffset              = 8
	dhcpFlagsOffset               = 10
	dhcpOfferedIPOffset           = 16
	dhcpOfferedIPEndOffset        = 20
	dhcpClientHardwareAddrOffset  = 28
	dhcpClientHardwareAddrEnd     = dhcpClientHardwareAddrOffset + dhcpHLenEthernet
	dhcpMagicCookieOffset         = 236
	dhcpMagicCookieEndOffset      = 240
	dhcpOptionsOffset             = dhcpMagicCookieEndOffset
	dhcpOptionPad                 = 0
	dhcpOptionLengthOffset        = 1
	dhcpOptionValueOffset         = 2
	dhcpOptionHeaderBytes         = 2
	dhcpOptionFirstValueOffset    = 0
	dhcpMessageTypeOptionBytes    = 1
	dhcpFourByteOptionBytes       = net.IPv4len
	dhcpParameterRequestListBytes = 4
	dhcpUDPBufferBytes            = 1500
)

// dhcpMagicCookie precedes the options field in a DHCP message (RFC 2131 §3).
var dhcpMagicCookie = []byte{99, 130, 83, 99}

// dhcpProtocol probes a DHCP server (dhcpd) natively (RFC 2131): it sends a
// DHCPDISCOVER and verifies the server answers with a DHCPOFFER, which proves
// the server is up and handing out leases. It does not send a DHCPREQUEST, so
// no real lease is consumed. No authentication.
//
// Two modes, selected by the optional `interface` param: with an interface it
// broadcasts the DISCOVER out that link (discovering any dhcpd without knowing
// its address); without one it unicasts to the configured host (a known server
// or relay). Either way it must bind the privileged client port 68 to receive
// the reply, so it needs CAP_NET_BIND_SERVICE (and CAP_NET_RAW for the
// per-interface bind), or root — the same elevated-privilege model as the icmp
// check. The chaddr is an anonymous, randomly generated locally-administered MAC
// by default; set the optional `mac` param to use a fixed address (e.g. for a
// server that only answers reserved clients).
type dhcpProtocol struct{}

func (dhcpProtocol) Name() string       { return ProtocolNameDHCP }
func (dhcpProtocol) DefaultPort() int   { return dhcpServerPort }
func (dhcpProtocol) RequiresUser() bool { return false }

func (dhcpProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	iface := cfg.Interface
	mac, err := dhcpClientMAC(cfg.Params[ParamKeyMAC])
	if err != nil {
		return Result{}, err
	}
	xid := randXID32()
	packet := buildDHCPDiscover(xid, mac)

	// No interface -> unicast to a known server/relay; resolve its address now.
	var server string
	if iface == "" {
		host := cfg.Host
		if host == "" {
			host = DefaultHost
		}
		port := cfg.Port
		if port == 0 {
			port = dhcpServerPort
		}
		server = hostPort(host, port)
	}

	reply, err := dhcpExchange(ctx, iface, server, packet, xid)
	if err != nil {
		return Result{}, err
	}
	info, err := parseDHCPOffer(reply, xid)
	if err != nil {
		return Result{}, err
	}

	extra := map[string]string{
		extraDHCPMessage: dhcpMessageOffer,
		extraOfferedIP:   info.offeredIP,
		extraClientMAC:   mac.String(),
	}
	if iface != "" {
		extra[extraMode], extra[extraInterface] = dhcpModeBroadcast, iface
	} else {
		extra[extraMode] = dhcpModeUnicast
	}
	if info.serverID != "" {
		extra[extraServerID] = info.serverID
	}
	if info.subnetMask != "" {
		extra[extraSubnetMask] = info.subnetMask
	}
	if info.leaseSeconds > 0 {
		extra[extraLeaseSeconds] = strconv.Itoa(info.leaseSeconds)
	}
	return Result{Extra: extra}, nil
}

// dhcpClientMAC returns the chaddr to put in the DISCOVER. An empty s yields a
// fresh random locally-administered unicast MAC (an anonymous probe); otherwise
// s is parsed and must be a 6-byte Ethernet address.
func dhcpClientMAC(s string) (net.HardwareAddr, error) {
	if s != "" {
		mac, err := net.ParseMAC(s)
		if err != nil {
			return nil, fmt.Errorf("invalid mac %q: %w", s, err)
		}
		if len(mac) != dhcpHLenEthernet {
			return nil, fmt.Errorf("mac %q must be a 6-byte ethernet address", s)
		}
		return mac, nil
	}
	mac := make(net.HardwareAddr, dhcpHLenEthernet)
	if _, err := rand.Read(mac); err != nil {
		return nil, err
	}
	mac[0] = (mac[0] | dhcpMACLocalAdminBit) &^ dhcpMACMulticastBit
	return mac, nil
}

// buildDHCPDiscover builds a DHCPDISCOVER: the 236-byte BOOTP header, the magic
// cookie and the options (message type 53 = DISCOVER, plus a parameter request
// list). The broadcast flag is set so the server broadcasts the OFFER back to
// port 68, where the probe can receive it.
func buildDHCPDiscover(xid uint32, mac net.HardwareAddr) []byte {
	msg := make([]byte, dhcpPacketMinBytes)
	msg[dhcpOpOffset] = dhcpOpBootRequest
	msg[dhcpHardwareTypeOffset] = dhcpHTypeEthernet
	msg[dhcpHardwareLengthOffset] = dhcpHLenEthernet
	binary.BigEndian.PutUint32(msg[dhcpXIDOffset:], xid)
	binary.BigEndian.PutUint16(msg[dhcpFlagsOffset:], dhcpFlagBroadcast)
	copy(msg[dhcpClientHardwareAddrOffset:dhcpClientHardwareAddrEnd], mac)
	copy(msg[dhcpMagicCookieOffset:dhcpMagicCookieEndOffset], dhcpMagicCookie)
	msg = append(msg,
		dhcpOptMessageType, dhcpMessageTypeOptionBytes, dhcpDiscover,
		dhcpOptParamReqList, dhcpParameterRequestListBytes, dhcpOptSubnetMask, dhcpOptRouter, dhcpOptLeaseTime, dhcpOptServerID,
		dhcpOptEnd,
	)
	return msg
}

// dhcpOfferInfo is what a parsed DHCPOFFER carries.
type dhcpOfferInfo struct {
	offeredIP    string
	serverID     string
	subnetMask   string
	leaseSeconds int
	messageType  int
}

// parseDHCPOffer validates a DHCP reply is a well-formed OFFER for xid and
// extracts the offered address (yiaddr) and the common options.
func parseDHCPOffer(b []byte, xid uint32) (dhcpOfferInfo, error) {
	if len(b) < dhcpPacketMinBytes {
		return dhcpOfferInfo{}, errors.New("short DHCP reply")
	}
	if b[dhcpOpOffset] != dhcpOpBootReply {
		return dhcpOfferInfo{}, fmt.Errorf("not a DHCP reply (op=%d)", b[dhcpOpOffset])
	}
	if got := binary.BigEndian.Uint32(b[dhcpXIDOffset:dhcpXIDEndOffset]); got != xid {
		return dhcpOfferInfo{}, errors.New("DHCP reply xid mismatch")
	}
	if !bytes.Equal(b[dhcpMagicCookieOffset:dhcpMagicCookieEndOffset], dhcpMagicCookie) {
		return dhcpOfferInfo{}, errors.New("missing DHCP magic cookie")
	}

	info := dhcpOfferInfo{offeredIP: net.IP(b[dhcpOfferedIPOffset:dhcpOfferedIPEndOffset]).String()}
	opts := b[dhcpOptionsOffset:]
	for i := 0; i < len(opts); {
		code := opts[i]
		if code == dhcpOptEnd {
			break
		}
		if code == dhcpOptionPad {
			i++
			continue
		}
		if i+dhcpOptionLengthOffset >= len(opts) {
			break
		}
		l := int(opts[i+dhcpOptionLengthOffset])
		if i+dhcpOptionValueOffset+l > len(opts) {
			break
		}
		data := opts[i+dhcpOptionValueOffset : i+dhcpOptionValueOffset+l]
		switch code {
		case dhcpOptMessageType:
			if l == dhcpMessageTypeOptionBytes {
				info.messageType = int(data[dhcpOptionFirstValueOffset])
			}
		case dhcpOptServerID:
			if l == dhcpFourByteOptionBytes {
				info.serverID = net.IP(data).String()
			}
		case dhcpOptSubnetMask:
			if l == dhcpFourByteOptionBytes {
				info.subnetMask = net.IP(data).String()
			}
		case dhcpOptLeaseTime:
			if l == dhcpFourByteOptionBytes {
				info.leaseSeconds = int(binary.BigEndian.Uint32(data))
			}
		}
		i += dhcpOptionHeaderBytes + l
	}
	if info.messageType != dhcpOffer {
		return dhcpOfferInfo{}, fmt.Errorf("expected DHCPOFFER, got message type %d", info.messageType)
	}
	return info, nil
}
