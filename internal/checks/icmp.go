package checks

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
)

const (
	defaultPingTimeout          = 5 * time.Second
	defaultPingPerPacketTimeout = time.Second
	icmpEchoCode                = 0
	icmpIDMask                  = 0xffff
	icmpListenAnyIPv4           = "0.0.0.0"
	icmpReplyBufferSize         = 1500
	icmpV4ProtocolNumber        = 1
	microsecondsPerMillisecond  = 1000.0
	networkIP4                  = "ip4"
	networkIP4ICMP              = "ip4:icmp"
)

// defaultPingCount is the default number of ICMP echo requests for ping checks.
const defaultPingCount = 3

// PingSample is one ICMP observation of a host.
type PingSample struct {
	Reachable bool
	RTTms     float64
	RTTKnown  bool
}

// PingSamplerFunc probes a host with count echo requests bounded by timeout,
// egressing through iface when non-empty. Injected for tests; the default uses
// native ICMP via golang.org/x/net.
type PingSamplerFunc func(ctx context.Context, host, iface string, count int, timeout time.Duration) (PingSample, error)

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
	hasThreshold bool
	op           string
	value        float64
	delta        float64
	sampler      PingSamplerFunc

	primed    bool
	lastState string
	lastRTT   float64
}

func (c *icmpCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultPingSampler
	}
	s, err := c.sample(ctx, sampler)
	if err != nil {
		return c.unavailableResult(fmt.Sprintf("icmp %s: %v", c.host, err), start)
	}
	data := map[string]any{DataKeyHost: c.host, DataKeyMetric: c.metric}

	switch c.metric {
	case NetMetricState:
		state := NetStateDown
		if s.Reachable {
			state = NetStateUp
		}
		ok, message := evaluateStateTransition(stateTransitionSpec{
			target: c.host, current: state, expected: c.expect,
			data: data, primed: &c.primed, previous: &c.lastState,
		})
		res := c.result(ok, message, start)
		res.Data = data
		return res

	case IcmpMetricLatency:
		if !s.RTTKnown {
			res := c.result(false, c.host+" unreachable (no rtt)", start)
			res.Data = data
			return res
		}
		// Both forms publish the round trip: the latency graph is offered for
		// every metric: latency watch, threshold or change.
		data[DataKeyLatencyMS] = s.RTTms
		if c.hasThreshold {
			data[DataKeyValue] = s.RTTms
			met := cfgval.CompareFloat(s.RTTms, c.op, c.value)
			res := c.gradeValue(c.result(met, fmt.Sprintf("%s rtt %.1fms %s %.1f", c.host, s.RTTms, c.op, c.value), start), CheckKeyThreshold, s.RTTms)
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
		diff := math.Abs(s.RTTms - c.lastRTT)
		changed := diff > c.delta
		data[DataKeyOld], data[DataKeyNew], data[DataKeyValue] = c.lastRTT, s.RTTms, s.RTTms
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

// sample runs the ping sampler over the configured interface set and combines
// reachability per interface_match (any: reachable if one interface reaches; all:
// reachable only if every one reaches, reporting the worst RTT). With no
// interfaces it samples once with default routing.
func (c *icmpCheck) sample(ctx context.Context, sampler PingSamplerFunc) (PingSample, error) {
	timeout := c.timeout
	if timeout <= 0 {
		timeout = defaultPingTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if len(c.ifaces) == 0 {
		return sampler(ctx, c.host, "", c.count, timeout)
	}
	var combined PingSample
	var lastErr error
	anyValid := false
	reachable := 0
	for i, ifc := range c.ifaces {
		if err := ctx.Err(); err != nil {
			return PingSample{}, fmt.Errorf("sample interfaces: %w", err)
		}
		attemptCtx, cancel := conn.AttemptContext(ctx, len(c.ifaces)-i)
		s, err := sampler(attemptCtx, c.host, ifc, c.count, timeout)
		cancel()
		if ctx.Err() == context.Canceled {
			return PingSample{}, fmt.Errorf("sample interfaces: %w", ctx.Err())
		}
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
func defaultPingSampler(ctx context.Context, host, iface string, count int, timeout time.Duration) (PingSample, error) {
	if timeout <= 0 {
		timeout = defaultPingTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addr, err := resolvePingTarget(ctx, net.DefaultResolver, host)
	if err != nil {
		return PingSample{}, fmt.Errorf("resolve %s: %w", host, err)
	}
	listen := icmpListenAnyIPv4
	if iface != "" {
		ip, err := conn.ResolveInterfaceIPv4(iface)
		if err != nil {
			return PingSample{}, fmt.Errorf("resolve interface %s: %w", iface, err)
		}
		listen = ip
	}
	icmpConn, err := icmp.ListenPacket(networkIP4ICMP, listen)
	if err != nil {
		return PingSample{}, fmt.Errorf("listen icmp: %w", err)
	}
	defer func() { _ = icmpConn.Close() }()

	return pingPackets(ctx, icmpConn, addr, count, timeout)
}

func pingPackets(ctx context.Context, icmpConn net.PacketConn, addr *net.IPAddr, count int, timeout time.Duration) (PingSample, error) {
	stop := context.AfterFunc(ctx, func() { _ = icmpConn.Close() })
	defer stop()
	perPacket := timeout / time.Duration(count)
	if perPacket <= 0 {
		perPacket = defaultPingPerPacketTimeout
	}
	id := os.Getpid() & icmpIDMask
	payload := []byte(rand.Text())
	reply := make([]byte, icmpReplyBufferSize)
	var rtts []float64
	for seq := range count {
		if err := ctx.Err(); err != nil {
			return PingSample{}, fmt.Errorf("send echo: %w", err)
		}
		msg := icmp.Message{
			Type: ipv4.ICMPTypeEcho, Code: icmpEchoCode,
			Body: &icmp.Echo{ID: id, Seq: seq, Data: payload},
		}
		b, err := msg.Marshal(nil)
		if err != nil {
			continue
		}
		sent := time.Now()
		deadline := sent.Add(perPacket)
		if end, ok := ctx.Deadline(); ok {
			// DNS and earlier interfaces consume this same budget. Leave
			// each remaining echo a share, even when the target is silent.
			deadline = sent.Add(min(perPacket, end.Sub(sent)/time.Duration(count-seq)))
		}
		_ = icmpConn.SetWriteDeadline(deadline)
		if _, err := icmpConn.WriteTo(b, addr); err != nil {
			continue
		}
		// Raw sockets receive every echo reply on the host. ID and sequence
		// can coincide across samples, so only our random payload proves the
		// reply belongs to this sample (including delayed replies from a cycle).
		_ = icmpConn.SetReadDeadline(deadline)
		for {
			n, peer, err := icmpConn.ReadFrom(reply)
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
			if rm.Type != ipv4.ICMPTypeEchoReply || !ok || echo.ID != id || echo.Seq != seq || !bytes.Equal(echo.Data, payload) {
				continue
			}
			rtts = append(rtts, float64(time.Since(sent).Microseconds())/microsecondsPerMillisecond)
			break
		}
	}
	// The final echo timing out completes a loss observation; cancellation
	// must never publish a new reachability or RTT baseline.
	if err := ctx.Err(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return PingSample{}, fmt.Errorf("receive echo: %w", err)
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
	if ip, ok := peer.(*net.IPAddr); ok {
		return ip.IP.Equal(want)
	}
	return false
}

// resolvePingTarget keeps DNS inside the same budget as the echo exchange.
func resolvePingTarget(ctx context.Context, resolver *net.Resolver, host string) (*net.IPAddr, error) {
	ips, err := resolver.LookupIP(ctx, networkIP4, host)
	if err != nil {
		return nil, fmt.Errorf("lookup IPv4: %w", err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no IPv4 address for %s", host)
	}
	return &net.IPAddr{IP: ips[0]}, nil
}
