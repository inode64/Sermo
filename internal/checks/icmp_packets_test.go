package checks

import (
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

type pingPacketConn struct {
	net.PacketConn
	write func([]byte, net.Addr) (int, error)
	read  func([]byte) (int, net.Addr, error)
}

func (c *pingPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) { return c.write(b, addr) }
func (c *pingPacketConn) ReadFrom(b []byte) (int, net.Addr, error)     { return c.read(b) }
func (*pingPacketConn) SetWriteDeadline(time.Time) error               { return nil }
func (*pingPacketConn) SetReadDeadline(time.Time) error                { return nil }

func pingReply(t *testing.T, request []byte) []byte {
	t.Helper()
	msg, err := icmp.ParseMessage(icmpV4ProtocolNumber, request)
	if err != nil {
		t.Fatal(err)
	}
	msg.Type = ipv4.ICMPTypeEchoReply
	reply, err := msg.Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}
	return reply
}

func TestPingPacketsRejectsAnotherSample(t *testing.T) {
	addr := &net.IPAddr{IP: net.ParseIP("192.0.2.10")}
	var foreign []byte
	first := &pingPacketConn{
		write: func(b []byte, _ net.Addr) (int, error) {
			foreign = pingReply(t, b)
			return len(b), nil
		},
		read: func([]byte) (int, net.Addr, error) { return 0, nil, os.ErrDeadlineExceeded },
	}
	if sample, err := pingPackets(first, addr, 1, time.Second); err != nil || sample.Reachable {
		t.Fatalf("lost sample = %+v, %v", sample, err)
	}
	for _, ownReply := range []bool{false, true} {
		t.Run(map[bool]string{false: "foreign only", true: "foreign then own"}[ownReply], func(t *testing.T) {
			var replies [][]byte
			reads := 0
			second := &pingPacketConn{
				write: func(b []byte, _ net.Addr) (int, error) {
					replies = append(replies, foreign)
					if ownReply {
						replies = append(replies, pingReply(t, b))
					}
					return len(b), nil
				},
				read: func(b []byte) (int, net.Addr, error) {
					reads++
					if len(replies) == 0 {
						return 0, nil, os.ErrDeadlineExceeded
					}
					n := copy(b, replies[0])
					replies = replies[1:]
					return n, addr, nil
				},
			}
			sample, err := pingPackets(second, addr, 1, time.Second)
			if err != nil || sample.Reachable != ownReply || sample.RTTKnown != ownReply || reads != 2 {
				t.Fatalf("second sample = %+v, %v; reads=%d, want own reply=%v and 2 reads", sample, err, reads, ownReply)
			}
		})
	}
}
