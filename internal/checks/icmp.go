package checks

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"sermo/internal/conn"
)

const (
	defaultPingCount            = 3
	defaultPingTimeout          = 5 * time.Second
	defaultPingPerPacketTimeout = time.Second
	icmpEchoCode                = 0
	icmpIDMask                  = 0xffff
	icmpListenAnyIPv4           = "0.0.0.0"
	icmpPayload                 = "sermo"
	icmpReplyBufferSize         = 1500
	icmpV4ProtocolNumber        = 1
	networkIP4                  = "ip4"
	networkIP4ICMP              = "ip4:icmp"
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
	ifaces       []string
	ifaceAll     bool
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
	s, err := c.sample(sampler)
	if err != nil {
		return c.result(false, fmt.Sprintf("icmp %s: %v", c.host, err), start)
	}
	data := map[string]any{fieldHost: c.host, fieldMetric: c.metric}

	switch c.metric {
	case NetMetricState:
		state := NetStateDown
		if s.Reachable {
			state = NetStateUp
		}
		if c.expect != "" {
			data[fieldValue] = state
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
		data[fieldOld], data[fieldNew], data[fieldValue] = c.lastState, state, state
		msg := fmt.Sprintf("%s state %s->%s", c.host, c.lastState, state)
		c.lastState = state
		res := c.result(changed, msg, start)
		res.Data = data
		return res

	case IcmpMetricLatency:
		if !s.RTTKnown {
			res := c.result(false, fmt.Sprintf("%s unreachable (no rtt)", c.host), start)
			res.Data = data
			return res
		}
		if c.hasThreshold {
			data[fieldValue] = s.RTTms
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
		data[fieldOld], data[fieldNew], data[fieldValue] = c.lastRTT, s.RTTms, s.RTTms
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

// SampleICMP probes host with the same interface aggregation semantics as the
// icmp check, using sampler when provided and the default native ICMP sampler
// otherwise. Exposed so callers like the web backend can render current ping
// state without running a stateful icmp check.
func SampleICMP(host string, ifaces []string, ifaceAll bool, count int, timeout time.Duration, sampler PingSamplerFunc) (PingSample, error) {
	if sampler == nil {
		sampler = defaultPingSampler
	}
	c := &icmpCheck{base: base{timeout: timeout}, host: host, ifaces: ifaces, ifaceAll: ifaceAll, count: count}
	return c.sample(sampler)
}

// sample runs the ping sampler over the configured interface set and combines
// reachability per interface_match (any: reachable if one interface reaches; all:
// reachable only if every one reaches, reporting the worst RTT). With no
// interfaces it samples once with default routing.
func (c *icmpCheck) sample(sampler PingSamplerFunc) (PingSample, error) {
	if len(c.ifaces) == 0 {
		return sampler(c.host, "", c.count, c.timeout)
	}
	var combined PingSample
	var lastErr error
	anyValid := false
	reachable := 0
	for _, ifc := range c.ifaces {
		s, err := sampler(c.host, ifc, c.count, c.timeout)
		if err != nil {
			lastErr = err
			if c.ifaceAll {
				return PingSample{}, err // a failed path fails an all-match check
			}
			continue
		}
		anyValid = true
		if s.Reachable {
			reachable++
			if !combined.Reachable {
				combined = s // first reachable provides the RTT baseline
			} else if c.ifaceAll && s.RTTKnown && s.RTTms > combined.RTTms {
				combined.RTTms = s.RTTms // all: report the worst path
			}
		}
	}
	if !anyValid {
		return PingSample{}, lastErr // every interface errored
	}
	if c.ifaceAll {
		combined.Reachable = reachable == len(c.ifaces)
		if !combined.Reachable {
			combined.RTTKnown = false
		}
	}
	return combined, nil
}

// defaultPingSampler sends count ICMPv4 echo requests via a raw socket
// (needs CAP_NET_RAW) and reports reachability + mean RTT in ms. When iface is
// set it binds the socket to that interface's IPv4 address (the `ping -I <addr>`
// mechanism) so the echo requests leave through it on a multi-homed host.
func defaultPingSampler(host, iface string, count int, timeout time.Duration) (PingSample, error) {
	if count <= 0 {
		count = defaultPingCount
	}
	if timeout <= 0 {
		timeout = defaultPingTimeout
	}
	addr, err := net.ResolveIPAddr(networkIP4, host)
	if err != nil {
		return PingSample{}, err
	}
	listen := icmpListenAnyIPv4
	if iface != "" {
		ip, err := conn.ResolveInterfaceIPv4(iface)
		if err != nil {
			return PingSample{}, err
		}
		listen = ip
	}
	conn, err := icmp.ListenPacket(networkIP4ICMP, listen)
	if err != nil {
		return PingSample{}, err
	}
	defer conn.Close()

	perPacket := timeout / time.Duration(count)
	if perPacket <= 0 {
		perPacket = defaultPingPerPacketTimeout
	}
	id := os.Getpid() & icmpIDMask
	reply := make([]byte, icmpReplyBufferSize)
	var rtts []float64
	for seq := 0; seq < count; seq++ {
		msg := icmp.Message{
			Type: ipv4.ICMPTypeEcho, Code: icmpEchoCode,
			Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte(icmpPayload)},
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
		// A raw ip4:icmp socket receives a copy of every echo reply on the host,
		// so a reply for another concurrent ping (which shares this PID-derived
		// id) can arrive first. Read until we see the echo reply that matches
		// THIS request — our id and seq, from the target address — or the
		// per-packet deadline passes; skip everything else instead of mistaking
		// a stray reply for ours.
		deadline := time.Now().Add(perPacket)
		_ = conn.SetReadDeadline(deadline)
		for {
			n, peer, err := conn.ReadFrom(reply)
			if err != nil {
				break // deadline or read error: no matching reply for this seq
			}
			if !sameIPv4(peer, addr.IP) {
				continue
			}
			rm, err := icmp.ParseMessage(icmpV4ProtocolNumber, reply[:n])
			if err != nil {
				continue
			}
			echo, ok := rm.Body.(*icmp.Echo)
			if rm.Type != ipv4.ICMPTypeEchoReply || !ok || echo.ID != id || echo.Seq != seq {
				continue
			}
			rtts = append(rtts, float64(time.Since(sent).Microseconds())/1000.0)
			break
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

// sameIPv4 reports whether peer (the source of a received ICMP packet) is the
// expected target IP, so a raw socket's replies for other hosts are not counted
// as this check's reply.
func sameIPv4(peer net.Addr, want net.IP) bool {
	if peer == nil {
		return false
	}
	if ip, ok := peer.(*net.IPAddr); ok {
		return ip.IP.Equal(want)
	}
	return false
}
