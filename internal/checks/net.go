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
	// NetStateSummary is the user-facing list of expected link states.
	NetStateSummary = NetStateUp + " or " + NetStateDown
)

// Address-presence expect values for a net address check. Exported for the same
// reason as the link-state values.
const (
	NetAddrPresent = "present"
	NetAddrAbsent  = "absent"
	// NetAddrSummary is the user-facing list of expected address states.
	NetAddrSummary = NetAddrPresent + " or " + NetAddrAbsent
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

	// SysfsNetClassPath is Linux's sysfs network-interface root.
	SysfsNetClassPath = "/sys/class/net"
	// SysfsIfaceFlagsFile is the sysfs file containing interface flag bits.
	SysfsIfaceFlagsFile = "flags"
	// SysfsIfaceOperstateFile is the sysfs file containing interface state.
	SysfsIfaceOperstateFile = "operstate"
	// SysfsIfaceHexValuePrefix prefixes hexadecimal sysfs flag values.
	SysfsIfaceHexValuePrefix = "0x"
	// SysfsIfaceFlagsBase is the integer base for sysfs flag parsing.
	SysfsIfaceFlagsBase = 16
	// SysfsIfaceFlagsBits is the bit width for sysfs flag parsing.
	SysfsIfaceFlagsBits = 64

	sysfsIfaceSpeedFile     = "speed"
	sysfsIfaceStatisticsDir = "statistics"
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
	data := map[string]any{DataKeyInterface: c.iface, DataKeyMetric: c.metric}

	switch c.metric {
	case NetMetricState:
		return c.runState(s, data, start)
	case NetMetricSpeed:
		return c.runSpeed(s, data, start)
	case NetMetricErrors:
		return c.runErrors(s, data, start)
	case NetMetricAddress:
		return c.runAddress(s, data, start)
	default:
		res := c.result(false, "unknown net metric "+c.metric, start)
		res.Data = data
		return res
	}
}

func (c *netCheck) runState(sample NetSample, data map[string]any, start time.Time) Result {
	ok, message := evaluateStateTransition(stateTransitionSpec{
		target: c.iface, current: sample.State, expected: c.expect, expectedLabel: NetMetricState,
		data: data, primed: &c.primed, previous: &c.lastState,
	})
	return c.netResult(ok, message, data, start)
}

func (c *netCheck) runSpeed(sample NetSample, data map[string]any, start time.Time) Result {
	if !sample.SpeedKnown {
		return c.netResult(false, c.iface+" speed unknown", data, start)
	}
	if !c.primed {
		c.primed, c.lastSpeed = true, sample.SpeedMbps
		return c.netResult(false, fmt.Sprintf("%s speed baseline %d", c.iface, sample.SpeedMbps), data, start)
	}
	changed := sample.SpeedMbps != c.lastSpeed
	data[DataKeyOld], data[DataKeyNew], data[DataKeyValue] = c.lastSpeed, sample.SpeedMbps, sample.SpeedMbps
	message := fmt.Sprintf("%s speed %d->%d", c.iface, c.lastSpeed, sample.SpeedMbps)
	c.lastSpeed = sample.SpeedMbps
	return c.netResult(changed, message, data, start)
}

func (c *netCheck) runErrors(sample NetSample, data map[string]any, start time.Time) Result {
	var total uint64
	for _, name := range c.counters {
		total += sample.Counters[name]
	}
	if !c.primed {
		c.primed, c.lastErrTotal = true, total
		return c.netResult(false, fmt.Sprintf("%s errors baseline %d", c.iface, total), data, start)
	}
	delta := deltaOrZero(total, c.lastErrTotal)
	c.lastErrTotal = total
	data[DataKeyValue], data[DataKeyTotal] = delta, total
	met := compareFloat(float64(delta), c.op, c.value)
	return c.netResult(met, fmt.Sprintf("%s errors +%d (total %d)", c.iface, delta, total), data, start)
}

func (c *netCheck) runAddress(sample NetSample, data map[string]any, start time.Time) Result {
	joined := strings.Join(sample.Addrs, ",")
	display := joined
	if display == "" {
		display = netAddrNone
	}
	data[DataKeyAddresses] = sample.Addrs
	if c.expect != "" {
		present := len(sample.Addrs) > 0
		data[DataKeyValue] = len(sample.Addrs)
		ok := (c.expect == NetAddrPresent) == present
		return c.netResult(ok, fmt.Sprintf("%s address %s (want %s)", c.iface, display, c.expect), data, start)
	}
	if !c.primed {
		c.primed, c.lastAddrs = true, joined
		return c.netResult(false, fmt.Sprintf("%s address baseline %s", c.iface, display), data, start)
	}
	changed := joined != c.lastAddrs
	data[DataKeyOld], data[DataKeyNew], data[DataKeyValue] = c.lastAddrs, joined, joined
	message := fmt.Sprintf("%s address %s->%s", c.iface, c.lastAddrs, joined)
	c.lastAddrs = joined
	return c.netResult(changed, message, data, start)
}

func (c *netCheck) netResult(ok bool, message string, data map[string]any, start time.Time) Result {
	result := c.result(ok, message, start)
	result.Data = data
	return result
}

// defaultNetSampler reads interface flags and /sys/class/net/<iface>.
func defaultNetSampler(iface string) (NetSample, error) {
	return sampleNetFromSysfs(iface, SysfsNetClassPath)
}

// InterfaceExists reports whether an interface is visible through netlink or
// sysfs. The sysfs fallback keeps diagnostics useful in restricted containers
// where net.InterfaceByName cannot query netlink but /sys/class/net is mounted.
func InterfaceExists(iface string) bool {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		_, statErr := os.Stat(sysfsIfaceDir(SysfsNetClassPath, iface))
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
	flags := sysfsIfaceFlagBits(filepath.Join(dir, SysfsIfaceFlagsFile))
	operstate := strings.TrimSpace(ReadTextFile(filepath.Join(dir, SysfsIfaceOperstateFile)))
	return flags&SysfsIfaceFlagUp != 0 && (flags&SysfsIfaceFlagRunning != 0 || operstate == NetStateUp || operstate == NetStateUnknown)
}

func sysfsIfaceFlagBits(path string) uint64 {
	raw := strings.TrimSpace(ReadTextFile(path))
	raw = strings.TrimPrefix(raw, SysfsIfaceHexValuePrefix)
	flags, _ := strconv.ParseUint(raw, SysfsIfaceFlagsBase, SysfsIfaceFlagsBits)
	return flags
}

// ReadTextFile reads a small text file (typically sysfs), returning "" on any
// error.
func ReadTextFile(path string) string {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return ""
	}
	return string(data)
}
