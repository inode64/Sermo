package conn

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
)

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

	offer := make([]byte, 240)
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
