package checks

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sermo/internal/hostfs"
	"slices"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
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

	// Identity sources under /sys/class/net/<iface>. `device` is a symlink into
	// the bus tree, so its target's base name is the bus address and its
	// `driver` link names the module bound to it; a virtual interface has no
	// such link and says what it is in `uevent` instead.
	sysfsIfaceAddressFile = "address"
	sysfsIfaceMTUFile     = "mtu"
	sysfsIfaceDuplexFile  = "duplex"
	sysfsIfaceCarrierFile = "carrier_changes"
	sysfsIfaceDeviceLink  = "device"
	sysfsIfaceDriverLink  = "driver"
	sysfsIfaceUeventFile  = "uevent"
	sysfsIfaceDevtypeKey  = "DEVTYPE="

	// netDuplexUnknown is what the kernel reports for an interface whose duplex
	// it cannot know — a bridge, or a physical port with no carrier.
	netDuplexUnknown = "unknown"
)

// NetIdentity is what a network interface *is*, as opposed to what it last
// measured: the fields that say which piece of hardware — or which virtual
// device — an operator is looking at. Every field is optional, because a
// physical port and a bridge publish disjoint subsets.
type NetIdentity struct {
	// MAC is the interface's current hardware address. It is the current one,
	// not the permanent one burned into the card: an enslaved bond member
	// reports its bond's address, which is what the wire actually carries.
	MAC string
	// Driver is the kernel module bound to the device (`ice`, `virtio_net`).
	// Empty for a virtual interface, which has no device to bind.
	Driver string
	// Bus is the address of the device in its bus tree (`0000:0a:00.0`), which
	// is what identifies the physical port on a multi-port card.
	Bus string
	// Kind is what a virtual interface says it is in uevent — `bridge`, `vlan`,
	// `bond`, `veth`. Empty for a physical port, whose kind is its driver.
	Kind   string
	MTU    int64
	Duplex string
}

// empty reports whether sysfs published nothing at all, which is how a caller
// tells "no such interface" from "an interface that answers nothing useful".
func (id NetIdentity) empty() bool {
	return id == NetIdentity{}
}

// NetIdentityFunc reports the identity sysfs holds for an interface. Injected
// for tests; the default reads /sys/class/net/<iface>.
type NetIdentityFunc func(iface string) NetIdentity

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
	// CarrierChanges counts every link transition the kernel has seen since the
	// interface appeared. A link that is up now but has flapped hundreds of
	// times is a different situation from one that has been up since boot, and
	// a state check sampling every cycle cannot see the flaps between samples.
	CarrierChanges      uint64
	CarrierChangesKnown bool
	// Identity is what the interface is. It is read on the same pass as the
	// sample so a result never has to go back to sysfs twice.
	Identity NetIdentity
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
	counters []string
	op       string
	value    float64
	sampler  NetSamplerFunc

	identity NetIdentityFunc

	primed       bool
	lastState    string
	lastSpeed    int64
	lastErrTotal uint64
	lastAddrs    string
	// lastIdentity is what the interface was the last time it existed. Unlike a
	// disk, which keeps its /dev node and its sysfs identity after it stops
	// answering, an interface that is removed or renamed takes its whole sysfs
	// directory with it — so the only way a failed sample can still name the
	// card is to remember it. Per-check memory, so it starts empty after a
	// restart: Sermo reports only what it actually observed.
	lastIdentity NetIdentity
}

func (c *netCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := keyedSamplerOr(c.sampler, defaultNetSampler)
	s, err := sampler(c.iface)
	if err != nil {
		// The interface is gone, so sysfs can no longer say what it was. Report
		// the identity it had while it existed: "eth1 is missing" and "the
		// 34:5a:60:00:1c:93 port on 0000:0a:00.1 is missing" are different
		// amounts of help at three in the morning.
		res := c.unavailableResult(fmt.Sprintf("net %s: %v", c.iface, err), start)
		res.Data = withIdentity(map[string]any{DataKeyInterface: c.iface, DataKeyMetric: c.metric}, c.lastIdentity)
		return res
	}
	identity := s.Identity
	if identity.empty() {
		identity = resolveNetIdentity(c.identity, c.iface)
	}
	if !identity.empty() {
		c.lastIdentity = identity
	}
	data := withIdentity(map[string]any{DataKeyInterface: c.iface, DataKeyMetric: c.metric}, identity)
	if s.CarrierChangesKnown {
		data[DataKeyCarrierChanges] = s.CarrierChanges
	}

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
	met := cfgval.CompareFloat(float64(delta), c.op, c.value)
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

// defaultNetIdentity reads what sysfs publishes about one interface. A physical
// port answers driver and bus through its `device` symlink; a virtual one has
// no such link and names itself in `uevent` instead. Reading both covers every
// interface type in one pass with no device-type branching.
func defaultNetIdentity(iface string) NetIdentity {
	return netIdentityFromSysfs(iface, SysfsNetClassPath)
}

func netIdentityFromSysfs(iface, root string) NetIdentity {
	dir := sysfsIfaceDir(root, iface)
	if _, err := os.Stat(dir); err != nil {
		return NetIdentity{}
	}
	id := NetIdentity{
		MAC:    readTrim(filepath.Join(dir, sysfsIfaceAddressFile)),
		Driver: sysfsLinkBase(filepath.Join(dir, sysfsIfaceDeviceLink, sysfsIfaceDriverLink)),
		Bus:    sysfsLinkBase(filepath.Join(dir, sysfsIfaceDeviceLink)),
		Kind:   sysfsIfaceDevtype(dir),
	}
	// The kernel reports `unknown` rather than omitting the file for anything
	// without a negotiated duplex — a bridge, or a port with no carrier — and
	// publishing that word as a fact would be noise.
	if duplex := readTrim(filepath.Join(dir, sysfsIfaceDuplexFile)); duplex != "" && duplex != netDuplexUnknown {
		id.Duplex = duplex
	}
	if mtu, err := strconv.ParseInt(readTrim(filepath.Join(dir, sysfsIfaceMTUFile)), numericBaseDecimal, numericBits64); err == nil && mtu > 0 {
		id.MTU = mtu
	}
	return id
}

// sysfsLinkBase resolves a sysfs symlink and returns its target's base name,
// which is how sysfs spells both a bus address and a driver name. It returns ""
// when the link is absent, which is the normal case for a virtual interface.
func sysfsLinkBase(path string) string {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// sysfsIfaceDevtype reads DEVTYPE from an interface's uevent, which is how a
// virtual interface says whether it is a bridge, a vlan, a bond or a veth.
func sysfsIfaceDevtype(dir string) string {
	for line := range strings.SplitSeq(ReadTextFile(filepath.Join(dir, sysfsIfaceUeventFile)), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), sysfsIfaceDevtypeKey); ok {
			return rest
		}
	}
	return ""
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
			return NetSample{}, fmt.Errorf("interface %s: %w", iface, err)
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

	if raw, err := hostfs.ReadFile(filepath.Join(dir, sysfsIfaceSpeedFile)); err == nil {
		if v, err := strconv.ParseInt(strings.TrimSpace(string(raw)), numericBaseDecimal, numericBits64); err == nil && v >= 0 {
			sample.SpeedMbps, sample.SpeedKnown = v, true
		}
	}

	if err == nil {
		addNetInterfaceAddrs(&sample, ifi)
	}

	sample.Identity = netIdentityFromSysfs(iface, root)
	if changes, err := strconv.ParseUint(readTrim(filepath.Join(dir, sysfsIfaceCarrierFile)), numericBaseDecimal, numericBits64); err == nil {
		sample.CarrierChanges, sample.CarrierChangesKnown = changes, true
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
	operstate := readTrim(filepath.Join(dir, SysfsIfaceOperstateFile))
	return flags&SysfsIfaceFlagUp != 0 && (flags&SysfsIfaceFlagRunning != 0 || operstate == NetStateUp || operstate == NetStateUnknown)
}

func sysfsIfaceFlagBits(path string) uint64 {
	raw := readTrim(path)
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

// resolveNetIdentity reads an interface's identity through the injected reader,
// falling back to sysfs. A sampler supplied by a test may leave Identity unset,
// and identity is not the sampler's job to invent.
func resolveNetIdentity(fn NetIdentityFunc, iface string) NetIdentity {
	if fn != nil {
		return fn(iface)
	}
	return defaultNetIdentity(iface)
}

// identityData maps an interface's identity onto its result keys.
func (id NetIdentity) identityData() ([]identityField, []identityNumber) {
	return []identityField{
		{DataKeyMAC, id.MAC},
		{DataKeyDriver, id.Driver},
		{DataKeyBus, id.Bus},
		{DataKeyKind, id.Kind},
		{DataKeyDuplex, id.Duplex},
	}, []identityNumber{
		{DataKeyMTU, uint64(max(id.MTU, 0))},
	}
}
