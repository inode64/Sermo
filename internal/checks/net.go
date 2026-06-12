package checks

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// NetSample is one observation of a network interface.
type NetSample struct {
	State      string // "up" | "down"
	SpeedMbps  int64
	SpeedKnown bool
	Counters   map[string]uint64 // statistics counters by name
}

// NetSamplerFunc observes an interface. Injected for tests; the default reads
// net.Interfaces() flags and /sys/class/net/<iface>.
type NetSamplerFunc func(iface string) (NetSample, error)

// netCheck watches one metric (state|speed|errors) of one interface. It is
// stateful across cycles (remembers the previous sample) and therefore a pointer
// type; this is safe because a watch ticks sequentially on its own goroutine.
// OK==true means "fire".
type netCheck struct {
	base
	iface    string
	metric   string
	expect   string // state: "up"|"down"; "" means on-change
	onChange bool   // state/speed change detection
	counters []string
	op       string
	value    float64
	sampler  NetSamplerFunc

	primed       bool
	lastState    string
	lastSpeed    int64
	lastErrTotal uint64
}

func (c *netCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultNetSampler
	}
	s, err := sampler(c.iface)
	if err != nil {
		return c.result(false, fmt.Sprintf("net %s: %v", c.iface, err), start)
	}
	data := map[string]any{"interface": c.iface, "metric": c.metric}

	switch c.metric {
	case "state":
		if c.expect != "" {
			data["value"] = s.State
			res := c.result(s.State == c.expect, fmt.Sprintf("%s state %s (want %s)", c.iface, s.State, c.expect), start)
			res.Data = data
			return res
		}
		if !c.primed {
			c.primed, c.lastState = true, s.State
			res := c.result(false, fmt.Sprintf("%s state baseline %s", c.iface, s.State), start)
			res.Data = data
			return res
		}
		changed := s.State != c.lastState
		data["old"], data["new"], data["value"] = c.lastState, s.State, s.State
		msg := fmt.Sprintf("%s state %s->%s", c.iface, c.lastState, s.State)
		c.lastState = s.State
		res := c.result(changed, msg, start)
		res.Data = data
		return res

	case "speed":
		if !s.SpeedKnown {
			res := c.result(false, fmt.Sprintf("%s speed unknown", c.iface), start)
			res.Data = data
			return res
		}
		if !c.primed {
			c.primed, c.lastSpeed = true, s.SpeedMbps
			res := c.result(false, fmt.Sprintf("%s speed baseline %d", c.iface, s.SpeedMbps), start)
			res.Data = data
			return res
		}
		changed := s.SpeedMbps != c.lastSpeed
		data["old"], data["new"], data["value"] = c.lastSpeed, s.SpeedMbps, s.SpeedMbps
		msg := fmt.Sprintf("%s speed %d->%d", c.iface, c.lastSpeed, s.SpeedMbps)
		c.lastSpeed = s.SpeedMbps
		res := c.result(changed, msg, start)
		res.Data = data
		return res

	case "errors":
		var total uint64
		for _, name := range c.counters {
			total += s.Counters[name]
		}
		if !c.primed {
			c.primed, c.lastErrTotal = true, total
			res := c.result(false, fmt.Sprintf("%s errors baseline %d", c.iface, total), start)
			res.Data = data
			return res
		}
		delta := deltaOrZero(total, c.lastErrTotal)
		c.lastErrTotal = total
		data["value"], data["total"] = delta, total
		met := compareFloat(float64(delta), c.op, c.value)
		res := c.result(met, fmt.Sprintf("%s errors +%d (total %d)", c.iface, delta, total), start)
		res.Data = data
		return res

	default:
		res := c.result(false, "unknown net metric "+c.metric, start)
		res.Data = data
		return res
	}
}

// defaultNetSampler reads interface flags and /sys/class/net/<iface>.
func defaultNetSampler(iface string) (NetSample, error) {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return NetSample{}, err
	}
	state := "down"
	if ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagRunning != 0 {
		state = "up"
	}
	sample := NetSample{State: state, Counters: map[string]uint64{}}

	if raw, err := os.ReadFile("/sys/class/net/" + iface + "/speed"); err == nil {
		if v, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil && v >= 0 {
			sample.SpeedMbps, sample.SpeedKnown = v, true
		}
	}

	statDir := "/sys/class/net/" + iface + "/statistics"
	if entries, err := os.ReadDir(statDir); err == nil {
		for _, e := range entries {
			if raw, err := os.ReadFile(statDir + "/" + e.Name()); err == nil {
				if v, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64); err == nil {
					sample.Counters[e.Name()] = v
				}
			}
		}
	}
	return sample, nil
}
