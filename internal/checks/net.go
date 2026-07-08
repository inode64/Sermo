package checks

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Link-state values reported and expected by net/icmp state checks. Exported so
// config validation checks the same expect vocabulary the check evaluates.
const (
	NetStateUp      = "up"
	NetStateDown    = "down"
	NetStateUnknown = "unknown"
)

// Address-presence expect values for a net address check. Exported for the same
// reason as the link-state values.
const (
	NetAddrPresent = "present"
	NetAddrAbsent  = "absent"
	netAddrNone    = "none"
)

// Network statistics counter names used by the default net error metric.
const (
	NetCounterRXErrors = "rx_errors"
	NetCounterTXErrors = "tx_errors"
)

const (
	// SysfsIfaceFlagUp is Linux IFF_UP from /sys/class/net/<iface>/flags.
	SysfsIfaceFlagUp uint64 = 0x1
	// SysfsIfaceFlagLoopback is Linux IFF_LOOPBACK from /sys/class/net/<iface>/flags.
	SysfsIfaceFlagLoopback uint64 = 0x8
	// SysfsIfaceFlagRunning is Linux IFF_RUNNING from /sys/class/net/<iface>/flags.
	SysfsIfaceFlagRunning uint64 = 0x40

	sysfsNetClassPath        = "/sys/class/net"
	sysfsIfaceFlagsFile      = "flags"
	sysfsIfaceOperstateFile  = "operstate"
	sysfsIfaceSpeedFile      = "speed"
	sysfsIfaceStatisticsDir  = "statistics"
	sysfsIfaceHexValuePrefix = "0x"
	sysfsIfaceFlagsBase      = 16
	sysfsIfaceFlagsBits      = 64
)

// NetSample is one observation of a network interface.
type NetSample struct {
	State      string // "up" | "down"
	SpeedMbps  int64
	SpeedKnown bool
	Counters   map[string]uint64 // statistics counters by name
	// Addrs are the interface's non-link-local addresses (IPv4 + global IPv6),
	// sorted. Link-local IPv6 is excluded: it exists on any up interface, so it
	// would mask both "no address assigned" and a provider-forced renumbering.
	Addrs []string
}

// NetSamplerFunc observes an interface. Injected for tests; the default reads
// net.Interfaces() flags and /sys/class/net/<iface>.
type NetSamplerFunc func(iface string) (NetSample, error)

// netCheck watches one metric (state|speed|errors|address) of one interface. It is
// stateful across cycles (remembers the previous sample) and therefore a pointer
// type; this is safe because a watch ticks sequentially on its own goroutine.
// OK==true means "fire".
type netCheck struct {
	base
	iface    string
	metric   string
	expect   string // state: "up"|"down"; address: "present"|"absent"; "" means on-change
	onChange bool   // state/speed/address change detection
	counters []string
	op       string
	value    float64
	sampler  NetSamplerFunc

	primed       bool
	lastState    string
	lastSpeed    int64
	lastErrTotal uint64
	lastAddrs    string
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
	data := map[string]any{DataKeyInterface: c.iface, fieldMetric: c.metric}

	switch c.metric {
	case NetMetricState:
		if c.expect != "" {
			data[fieldValue] = s.State
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
		data[fieldOld], data[fieldNew], data[fieldValue] = c.lastState, s.State, s.State
		msg := fmt.Sprintf("%s state %s->%s", c.iface, c.lastState, s.State)
		c.lastState = s.State
		res := c.result(changed, msg, start)
		res.Data = data
		return res

	case NetMetricSpeed:
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
		data[fieldOld], data[fieldNew], data[fieldValue] = c.lastSpeed, s.SpeedMbps, s.SpeedMbps
		msg := fmt.Sprintf("%s speed %d->%d", c.iface, c.lastSpeed, s.SpeedMbps)
		c.lastSpeed = s.SpeedMbps
		res := c.result(changed, msg, start)
		res.Data = data
		return res

	case NetMetricErrors:
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
		data[fieldValue], data[fieldTotal] = delta, total
		met := compareFloat(float64(delta), c.op, c.value)
		res := c.result(met, fmt.Sprintf("%s errors +%d (total %d)", c.iface, delta, total), start)
		res.Data = data
		return res

	case NetMetricAddress:
		joined := strings.Join(s.Addrs, ",")
		display := joined
		if display == "" {
			display = netAddrNone
		}
		data[DataKeyAddresses] = s.Addrs
		if c.expect != "" {
			present := len(s.Addrs) > 0
			data[fieldValue] = len(s.Addrs)
			ok := (c.expect == NetAddrPresent) == present
			res := c.result(ok, fmt.Sprintf("%s address %s (want %s)", c.iface, display, c.expect), start)
			res.Data = data
			return res
		}
		if !c.primed {
			c.primed, c.lastAddrs = true, joined
			res := c.result(false, fmt.Sprintf("%s address baseline %s", c.iface, display), start)
			res.Data = data
			return res
		}
		changed := joined != c.lastAddrs
		data[fieldOld], data[fieldNew], data[fieldValue] = c.lastAddrs, joined, joined
		msg := fmt.Sprintf("%s address %s->%s", c.iface, c.lastAddrs, joined)
		c.lastAddrs = joined
		res := c.result(changed, msg, start)
		res.Data = data
		return res

	default:
		res := c.result(false, "unknown net metric "+c.metric, start)
		res.Data = data
		return res
	}
}

// SampleNet returns one live network-interface observation using the default
// net.Interfaces + /sys/class/net reader. Exposed so callers like the web
// backend can render interface state without running a stateful net check.
func SampleNet(iface string) (NetSample, error) { return defaultNetSampler(iface) }

// defaultNetSampler reads interface flags and /sys/class/net/<iface>.
func defaultNetSampler(iface string) (NetSample, error) {
	return sampleNetFromSysfs(iface, sysfsNetClassPath)
}

// InterfaceExists reports whether an interface is visible through netlink or
// sysfs. The sysfs fallback keeps diagnostics useful in restricted containers
// where net.InterfaceByName cannot query netlink but /sys/class/net is mounted.
func InterfaceExists(iface string) bool {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		_, statErr := os.Stat(sysfsIfaceDir(sysfsNetClassPath, iface))
		return statErr == nil
	}
	return ifi != nil
}

func sampleNetFromSysfs(iface, root string) (NetSample, error) {
	ifi, err := net.InterfaceByName(iface)
	dir := sysfsIfaceDir(root, iface)
	if err != nil {
		if _, statErr := os.Stat(dir); statErr != nil {
			return NetSample{}, err
		}
	}
	state := NetStateDown
	if err == nil && ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagRunning != 0 {
		state = NetStateUp
	}
	if err != nil && sysfsIfaceUp(dir) {
		state = NetStateUp
	}
	sample := NetSample{State: state, Counters: map[string]uint64{}}

	if raw, err := os.ReadFile(filepath.Join(dir, sysfsIfaceSpeedFile)); err == nil {
		if v, err := strconv.ParseInt(strings.TrimSpace(string(raw)), numericBaseDecimal, numericBits64); err == nil && v >= 0 {
			sample.SpeedMbps, sample.SpeedKnown = v, true
		}
	}

	if err == nil {
		addNetInterfaceAddrs(&sample, ifi)
	}

	statDir := filepath.Join(dir, sysfsIfaceStatisticsDir)
	if entries, err := os.ReadDir(statDir); err == nil {
		for _, e := range entries {
			if v, err := readProcUint(filepath.Join(statDir, e.Name())); err == nil {
				sample.Counters[e.Name()] = v
			}
		}
	}
	return sample, nil
}

func addNetInterfaceAddrs(sample *NetSample, ifi *net.Interface) {
	if addrs, err := ifi.Addrs(); err == nil {
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			sample.Addrs = append(sample.Addrs, ipn.IP.String())
		}
	}
	slices.Sort(sample.Addrs)
}

func sysfsIfaceDir(root, iface string) string {
	return filepath.Join(root, filepath.Base(iface))
}

func sysfsIfaceUp(dir string) bool {
	flags := sysfsIfaceFlagBits(filepath.Join(dir, sysfsIfaceFlagsFile))
	operstate := strings.TrimSpace(readTextFile(filepath.Join(dir, sysfsIfaceOperstateFile)))
	return flags&SysfsIfaceFlagUp != 0 && (flags&SysfsIfaceFlagRunning != 0 || operstate == NetStateUp || operstate == NetStateUnknown)
}

func sysfsIfaceFlagBits(path string) uint64 {
	raw := strings.TrimSpace(readTextFile(path))
	raw = strings.TrimPrefix(raw, sysfsIfaceHexValuePrefix)
	flags, _ := strconv.ParseUint(raw, sysfsIfaceFlagsBase, sysfsIfaceFlagsBits)
	return flags
}

func readTextFile(path string) string {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return ""
	}
	return string(data)
}
