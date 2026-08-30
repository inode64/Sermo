package conn

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
)

const dhcpOfferOptionsBytes = 22

func TestDHCPClientMAC(t *testing.T) {
	// Empty input yields a random locally-administered unicast MAC.
	mac, err := dhcpClientMAC("")
	if err != nil {
		t.Fatal(err)
	}
	if len(mac) != dhcpHLenEthernet {
		t.Fatalf("len = %d, want %d", len(mac), dhcpHLenEthernet)
	}
	if mac[0]&0x01 != 0 {
		t.Fatalf("random MAC must be unicast, got %s", mac)
	}
	if mac[0]&0x02 == 0 {
		t.Fatalf("random MAC must be locally administered, got %s", mac)
	}

	// A configured MAC is parsed verbatim.
	mac, err = dhcpClientMAC("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatal(err)
	}
	if mac.String() != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("got %s, want aa:bb:cc:dd:ee:ff", mac)
	}

	if _, err := dhcpClientMAC("not-a-mac"); err == nil {
		t.Fatal("expected an error for an invalid MAC")
	}
}

func TestBuildDHCPDiscover(t *testing.T) {
	mac, _ := net.ParseMAC("aa:bb:cc:dd:ee:ff")
	pkt := buildDHCPDiscover(0x12345678, mac)

	if len(pkt) < 240 {
		t.Fatalf("packet too short: %d", len(pkt))
	}
	if pkt[0] != dhcpOpBootRequest {
		t.Fatalf("op = %d, want %d", pkt[0], dhcpOpBootRequest)
	}
	if pkt[1] != dhcpHTypeEthernet || pkt[2] != dhcpHLenEthernet {
		t.Fatalf("htype/hlen = %d/%d", pkt[1], pkt[2])
	}
	if got := binary.BigEndian.Uint32(pkt[4:8]); got != 0x12345678 {
		t.Fatalf("xid = %#x, want 0x12345678", got)
	}
	if binary.BigEndian.Uint16(pkt[10:12])&dhcpFlagBroadcast == 0 {
		t.Fatal("broadcast flag must be set")
	}
	if !bytes.Equal(pkt[28:34], mac) {
		t.Fatalf("chaddr = %x, want %x", pkt[28:34], []byte(mac))
	}
	if !bytes.Equal(pkt[236:240], dhcpMagicCookie) {
		t.Fatal("magic cookie missing")
	}
	if !bytes.Contains(pkt[240:], []byte{dhcpOptMessageType, 1, dhcpDiscover}) {
		t.Fatal("DISCOVER message-type option missing")
	}
}

func TestParseDHCPOffer(t *testing.T) {
	const xid = uint32(0xdeadbeef)
	mac, _ := net.ParseMAC("aa:bb:cc:dd:ee:ff")

	offer := make([]byte, dhcpPacketMinBytes, dhcpPacketMinBytes+dhcpOfferOptionsBytes)
	offer[0] = dhcpOpBootReply
	offer[1] = dhcpHTypeEthernet
	offer[2] = dhcpHLenEthernet
	binary.BigEndian.PutUint32(offer[4:], xid)
	copy(offer[16:20], net.IP{192, 168, 1, 50}.To4()) // yiaddr
	copy(offer[28:34], mac)
	copy(offer[236:240], dhcpMagicCookie)
	offer = append(offer,
		dhcpOptMessageType, 1, dhcpOffer,
		dhcpOptServerID, 4, 192, 168, 1, 1,
		dhcpOptLeaseTime, 4, 0, 0, 0x0e, 0x10, // 3600 seconds
		dhcpOptSubnetMask, 4, 255, 255, 255, 0,
		dhcpOptEnd,
	)

	info, err := parseDHCPOffer(offer, xid)
	if err != nil {
		t.Fatal(err)
	}
	if info.offeredIP != "192.168.1.50" {
		t.Fatalf("offeredIP = %q, want 192.168.1.50", info.offeredIP)
	}
	if info.serverID != "192.168.1.1" {
		t.Fatalf("serverID = %q, want 192.168.1.1", info.serverID)
	}
	if info.leaseSeconds != 3600 {
		t.Fatalf("leaseSeconds = %d, want 3600", info.leaseSeconds)
	}
	if info.subnetMask != "255.255.255.0" {
		t.Fatalf("subnetMask = %q, want 255.255.255.0", info.subnetMask)
	}

	if _, err := parseDHCPOffer(offer, xid+1); err == nil {
		t.Fatal("expected a xid-mismatch error")
	}

	// A reply that is not a DHCPOFFER (e.g. message type DISCOVER) must fail.
	notOffer := append([]byte(nil), offer...)
	notOffer[242] = dhcpDiscover // option 53 value lives at offset 240+2
	if _, err := parseDHCPOffer(notOffer, xid); err == nil {
		t.Fatal("expected an error for a non-OFFER message type")
	}

	if _, err := parseDHCPOffer(offer[:200], xid); err == nil {
		t.Fatal("expected an error for a short reply")
	}
}

func TestDHCPReplyMatches(t *testing.T) {
	mac, err := dhcpClientMAC("02:00:53:45:52:01")
	if err != nil {
		t.Fatal(err)
	}
	const xid = 0xDEADBEEF
	offer := buildDHCPDiscover(xid, mac)
	offer[dhcpOpOffset] = dhcpOpBootReply

	if !dhcpReplyMatches(offer, xid) {
		t.Error("a BOOTREPLY carrying our xid is the answer we are waiting for")
	}
	// Port 68 carries every client's traffic: another client's reply, and the
	// echo of our own request, must both be read past rather than accepted.
	if dhcpReplyMatches(offer, xid+1) {
		t.Error("a reply for another transaction must not be accepted")
	}
	request := buildDHCPDiscover(xid, mac)
	if dhcpReplyMatches(request, xid) {
		t.Error("a BOOTREQUEST is not a reply")
	}
	if dhcpReplyMatches(offer[:dhcpXIDEndOffset-1], xid) {
		t.Error("a datagram too short to carry an xid must not be read past its end")
	}
}

// A DHCP server on this same host answers with a broadcast the kernel loops
// back. The reply must be accepted when it is attributed to the link the probe
// was aimed at, which is exactly what SO_BINDTODEVICE failed to do.
func TestDHCPFromInterface(t *testing.T) {
	const eth1, br0 = 3, 7
	for _, tc := range []struct {
		name          string
		ingress, want int
		accept        bool
	}{
		{"reply from the link we probed", br0, br0, true},
		{"reply from another link", eth1, br0, false},
		{"a unicast probe names no link and accepts any", eth1, 0, true},
		{"an unattributed reply is accepted rather than lost", 0, br0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dhcpFromInterface(tc.ingress, tc.want); got != tc.accept {
				t.Errorf("dhcpFromInterface(%d, %d) = %v, want %v", tc.ingress, tc.want, got, tc.accept)
			}
		})
	}
}
