package checks

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// PingSample is one ICMP observation of a host.
type PingSample struct {
	Reachable bool
	RTTms     float64
	RTTKnown  bool
}

// PingSamplerFunc probes a host with count echo requests bounded by timeout,
// egressing through iface when non-empty. Injected for tests; the default uses
// native ICMP via golang.org/x/net.
type PingSamplerFunc func(host, iface string, count int, timeout time.Duration) (PingSample, error)

// icmpCheck watches one metric (state|latency) of one external host. Stateful
// across cycles (baseline for on:change / change), hence a pointer type; safe
// because a watch ticks sequentially on its own goroutine. OK==true means "fire".
type icmpCheck struct {
	base
	host         string
	iface        string
	count        int
	metric       string
	expect       string // state: "up"|"down"; "" means on-change
	onChange     bool
	hasThreshold bool
	op           string
	value        float64
	hasChange    bool
	delta        float64
	sampler      PingSamplerFunc

	primed    bool
	lastState string
	lastRTT   float64
}

func (c *icmpCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultPingSampler
	}
	s, err := sampler(c.host, c.iface, c.count, c.timeout)
	if err != nil {
		return c.result(false, fmt.Sprintf("icmp %s: %v", c.host, err), start)
	}
	data := map[string]any{"host": c.host, "metric": c.metric}

	switch c.metric {
	case "state":
		state := "down"
		if s.Reachable {
			state = "up"
		}
		if c.expect != "" {
			data["value"] = state
			res := c.result(state == c.expect, fmt.Sprintf("%s %s (want %s)", c.host, state, c.expect), start)
			res.Data = data
			return res
		}
		if !c.primed {
			c.primed, c.lastState = true, state
			res := c.result(false, fmt.Sprintf("%s state baseline %s", c.host, state), start)
			res.Data = data
			return res
		}
		changed := state != c.lastState
		data["old"], data["new"], data["value"] = c.lastState, state, state
		msg := fmt.Sprintf("%s state %s->%s", c.host, c.lastState, state)
		c.lastState = state
		res := c.result(changed, msg, start)
		res.Data = data
		return res

	case "latency":
		if !s.RTTKnown {
			res := c.result(false, fmt.Sprintf("%s unreachable (no rtt)", c.host), start)
			res.Data = data
			return res
		}
		if c.hasThreshold {
			data["value"] = s.RTTms
			met := compareFloat(s.RTTms, c.op, c.value)
			res := c.result(met, fmt.Sprintf("%s rtt %.1fms %s %.1f", c.host, s.RTTms, c.op, c.value), start)
			res.Data = data
			return res
		}
		// change mode
		if !c.primed {
			c.primed, c.lastRTT = true, s.RTTms
			res := c.result(false, fmt.Sprintf("%s rtt baseline %.1fms", c.host, s.RTTms), start)
			res.Data = data
			return res
		}
		diff := s.RTTms - c.lastRTT
		if diff < 0 {
			diff = -diff
		}
		changed := diff > c.delta
		data["old"], data["new"], data["value"] = c.lastRTT, s.RTTms, s.RTTms
		msg := fmt.Sprintf("%s rtt %.1f->%.1fms (|Δ|=%.1f > %.1f)", c.host, c.lastRTT, s.RTTms, diff, c.delta)
		c.lastRTT = s.RTTms
		res := c.result(changed, msg, start)
		res.Data = data
		return res

	default:
		res := c.result(false, "unknown icmp metric "+c.metric, start)
		res.Data = data
		return res
	}
}

// defaultPingSampler sends count ICMPv4 echo requests via a raw socket
// (needs CAP_NET_RAW) and reports reachability + mean RTT in ms. When iface is
// set it binds the socket to that interface's IPv4 address (the `ping -I <addr>`
// mechanism) so the echo requests leave through it on a multi-homed host.
func defaultPingSampler(host, iface string, count int, timeout time.Duration) (PingSample, error) {
	if count <= 0 {
		count = 3
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	addr, err := net.ResolveIPAddr("ip4", host)
	if err != nil {
		return PingSample{}, err
	}
	listen := "0.0.0.0"
	if iface != "" {
		ip, err := interfaceIPv4(iface)
		if err != nil {
			return PingSample{}, err
		}
		listen = ip
	}
	conn, err := icmp.ListenPacket("ip4:icmp", listen)
	if err != nil {
		return PingSample{}, err
	}
	defer conn.Close()

	perPacket := timeout / time.Duration(count)
	if perPacket <= 0 {
		perPacket = time.Second
	}
	id := os.Getpid() & 0xffff
	var rtts []float64
	for seq := 0; seq < count; seq++ {
		msg := icmp.Message{
			Type: ipv4.ICMPTypeEcho, Code: 0,
			Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("sermo")},
		}
		b, err := msg.Marshal(nil)
		if err != nil {
			continue
		}
		sent := time.Now()
		_ = conn.SetWriteDeadline(time.Now().Add(perPacket))
		if _, err := conn.WriteTo(b, addr); err != nil {
			continue
		}
		reply := make([]byte, 1500)
		_ = conn.SetReadDeadline(time.Now().Add(perPacket))
		n, _, err := conn.ReadFrom(reply)
		if err != nil {
			continue
		}
		rm, err := icmp.ParseMessage(1, reply[:n]) // 1 = ICMPv4 protocol number
		if err != nil {
			continue
		}
		if rm.Type == ipv4.ICMPTypeEchoReply {
			rtts = append(rtts, float64(time.Since(sent).Microseconds())/1000.0)
		}
	}
	if len(rtts) == 0 {
		return PingSample{}, nil
	}
	var sum float64
	for _, r := range rtts {
		sum += r
	}
	return PingSample{Reachable: true, RTTKnown: true, RTTms: sum / float64(len(rtts))}, nil
}

// interfaceIPv4 returns the first IPv4 address of the named interface, for binding
// a probe's source address to it.
func interfaceIPv4(iface string) (string, error) {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return "", err
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				return ip4.String(), nil
			}
		}
	}
	return "", fmt.Errorf("interface %s has no IPv4 address", iface)
}
