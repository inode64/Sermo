package checks

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func TestICMPCheckPassesBoundedContext(t *testing.T) {
	for _, parentCancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "parent cancellation"}[parentCancel], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c := &icmpCheck{name: "p", timeout: 20 * time.Millisecond, host: "h", metric: NetMetricState,
				primed: true, lastState: NetStateUp,
				sampler: func(ctx context.Context, _, _ string, _ int, _ time.Duration) (PingSample, error) {
					if _, ok := ctx.Deadline(); !ok {
						t.Fatal("sampler context has no deadline")
					}
					if parentCancel {
						cancel()
					}
					<-ctx.Done()
					return PingSample{}, ctx.Err()
				}}
			res := c.Run(ctx)
			if !res.Unavailable || c.lastState != NetStateUp {
				t.Fatalf("cancelled sample = %+v, baseline=%s", res, c.lastState)
			}
		})
	}
}

func TestResolvePingTargetCancellation(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "parent cancellation", true: "deadline"}[timeout], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
				if !timeout {
					cancel()
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Second):
					return nil, errors.New("lookup context did not stop")
				}
			}}
			started := time.Now()
			if _, err := resolvePingTarget(ctx, resolver, "ping.example.invalid."); err == nil {
				t.Fatal("cancelled DNS lookup succeeded")
			}
			if time.Since(started) > time.Second {
				t.Fatal("DNS lookup outlived its context")
			}
		})
	}
}

type blockingPingConn struct {
	pingPacketConn
	closed chan struct{}
	once   sync.Once
}

func (c *blockingPingConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func TestPingPacketsCancellationClosesSocket(t *testing.T) {
	for _, blockWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "write"}[blockWrite], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c := &blockingPingConn{closed: make(chan struct{})}
			c.write = func(b []byte, _ net.Addr) (int, error) {
				if blockWrite {
					cancel()
					waitPingClose(t, c.closed)
					return 0, net.ErrClosed
				}
				return len(b), nil
			}
			c.read = func([]byte) (int, net.Addr, error) {
				cancel()
				waitPingClose(t, c.closed)
				return 0, nil, net.ErrClosed
			}
			addr := &net.IPAddr{IP: net.ParseIP("192.0.2.10")}
			if _, err := pingPackets(ctx, c, addr, 3, time.Hour); !errors.Is(err, context.Canceled) {
				t.Fatalf("ping = %v, want cancellation", err)
			}
		})
	}
}

func TestICMPInterfacesShareTimeout(t *testing.T) {
	c := &icmpCheck{host: "h", timeout: time.Second, ifaces: []string{"a", "b"}}
	var deadlines []time.Time
	_, err := c.sample(t.Context(), func(ctx context.Context, _, _ string, _ int, _ time.Duration) (PingSample, error) {
		deadline, _ := ctx.Deadline()
		deadlines = append(deadlines, deadline)
		return PingSample{}, nil
	})
	if err != nil || len(deadlines) != 2 || !deadlines[0].Before(deadlines[1]) {
		t.Fatalf("sample = %v, interface deadlines = %v", err, deadlines)
	}
}

func waitPingClose(t *testing.T, closed <-chan struct{}) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close the ICMP socket")
	}
}

func TestPingPacketsFinalTimeoutIsPacketLoss(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	c := &pingPacketConn{
		write: func(b []byte, _ net.Addr) (int, error) { return len(b), nil },
		read: func([]byte) (int, net.Addr, error) {
			<-ctx.Done()
			return 0, nil, context.DeadlineExceeded
		},
	}
	addr := &net.IPAddr{IP: net.ParseIP("192.0.2.10")}
	if sample, err := pingPackets(ctx, c, addr, 1, time.Second); err != nil || sample.Reachable || sample.RTTKnown {
		t.Fatalf("lost final echo = %+v, %v", sample, err)
	}
}

func TestPingPacketsSharesRemainingBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	end, _ := ctx.Deadline()
	var deadlines []time.Time
	c := &pingPacketConn{
		write: func(b []byte, _ net.Addr) (int, error) { return len(b), nil },
		read:  func([]byte) (int, net.Addr, error) { return 0, nil, context.DeadlineExceeded },
		readDeadline: func(deadline time.Time) error {
			deadlines = append(deadlines, deadline)
			return nil
		},
	}
	addr := &net.IPAddr{IP: net.ParseIP("192.0.2.10")}
	if _, err := pingPackets(ctx, c, addr, 3, time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(deadlines) != 3 || !deadlines[0].Before(end.Add(-500*time.Millisecond)) || deadlines[2].After(end) {
		t.Fatalf("packet deadlines = %v, want a share of the remaining budget for each echo", deadlines)
	}
}

func TestICMPCancelledLastInterfaceCannotPublishEarlierSample(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := &icmpCheck{host: "h", ifaces: []string{"a", "b"}}
	_, err := c.sample(ctx, func(_ context.Context, _, iface string, _ int, _ time.Duration) (PingSample, error) {
		if iface == "b" {
			cancel()
			return PingSample{}, context.Canceled
		}
		return PingSample{Reachable: true}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sample = %v, want cancellation instead of an earlier valid observation", err)
	}
}
