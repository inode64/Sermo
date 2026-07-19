package checks

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/servicemgr"
)

const (
	defaultTLSPort = "443"

	// OnModeChange is the `on: change` metric/field mode: fire when the
	// observed value changes between cycles instead of comparing to a threshold.
	OnModeChange = "change"
	// OnModeDelete is the `on: delete` file existence mode.
	OnModeDelete = "delete"
)

// net/icmp check metric names (the `metric:` selector of a net or icmp check).
// The net ones are exported so the web backend labels its net-watch readings
// with the same names the check evaluates.
const (
	NetMetricState    = "state"
	NetMetricSpeed    = "speed"
	NetMetricErrors   = "errors"
	NetMetricAddress  = "address"
	IcmpMetricLatency = "latency" // exported for the web backend's icmp-watch readings
	// NetMetricSummary is the user-facing list of net check metrics.
	NetMetricSummary = NetMetricState + ", " + NetMetricSpeed + ", " + NetMetricErrors + " or " + NetMetricAddress
	// ICMPMetricSummary is the user-facing list of icmp check metrics.
	ICMPMetricSummary = NetMetricState + " or " + IcmpMetricLatency
)

// MetricReader returns a sampled metric for a scope. The daemon
// supplies the per-cycle sample; nil means no metric source (metric checks then
// report unavailable).
type MetricReader func(scope, name string) (metrics.Reading, bool)

// Samplers groups host probes that can be injected for checks. It is a narrow
// dependency bundle: service-specific capabilities such as Status, Metrics,
// Processes and pidfile fallback PIDs stay on Deps.
type Samplers struct {
	StorageUsage         StorageUsageFunc
	NetSampler           NetSamplerFunc
	PingSampler          PingSamplerFunc
	SwapSampler          SwapSamplerFunc
	RouteSampler         RouteSamplerFunc
	LoadSampler          LoadSamplerFunc
	OomSampler           OomSamplerFunc
	FdsSampler           FdsSamplerFunc
	MemorySampler        MemorySamplerFunc
	PressureSampler      PressureSamplerFunc
	PidsSampler          PidsSamplerFunc
	DiskIOSampler        DiskIOSamplerFunc
	SensorSampler        SensorSamplerFunc
	RaidSampler          RaidSamplerFunc
	EdacSampler          EdacSamplerFunc
	MountSampler         MountSamplerFunc
	ConntrackSampler     ConntrackSamplerFunc
	FirewallRulesSampler FirewallRulesSamplerFunc
	EntropySampler       EntropySamplerFunc
	ZombieSampler        ZombieSamplerFunc
	UsersSampler         UsersSamplerFunc
}

// ApplyTo returns deps with every sampler from s copied into it.
func (s Samplers) ApplyTo(deps Deps) Deps {
	deps.StorageUsage = s.StorageUsage
	deps.NetSampler = s.NetSampler
	deps.PingSampler = s.PingSampler
	deps.SwapSampler = s.SwapSampler
	deps.RouteSampler = s.RouteSampler
	deps.LoadSampler = s.LoadSampler
	deps.OomSampler = s.OomSampler
	deps.FdsSampler = s.FdsSampler
	deps.MemorySampler = s.MemorySampler
	deps.PressureSampler = s.PressureSampler
	deps.PidsSampler = s.PidsSampler
	deps.DiskIOSampler = s.DiskIOSampler
	deps.SensorSampler = s.SensorSampler
	deps.RaidSampler = s.RaidSampler
	deps.EdacSampler = s.EdacSampler
	deps.MountSampler = s.MountSampler
	deps.ConntrackSampler = s.ConntrackSampler
	deps.FirewallRulesSampler = s.FirewallRulesSampler
	deps.EntropySampler = s.EntropySampler
	deps.ZombieSampler = s.ZombieSampler
	deps.UsersSampler = s.UsersSampler
	return deps
}

// Deps are the host capabilities a built check set may need.
type Deps struct {
	Service        string
	DefaultTimeout time.Duration
	Runner         execx.Runner
	HTTPClient     *http.Client
	// Status queries the service's backend status, for `service` checks. When
	// nil, service checks are skipped with a warning.
	Status func(context.Context) (servicemgr.Status, error)
	// Metrics reads a sampled metric value, for `metric` checks.
	Metrics MetricReader
	// Processes reports the observed state (running/zombie/absent) of processes
	// matching an exe/user selector, for `process` checks.
	Processes func(exe, user string) string
	// ProcessesAny reports the observed state of processes matching any exact
	// resolved executable in exes with the same user. Nil falls back to Processes.
	ProcessesAny func(exes []string, user string) string
	// ProcessCount counts processes matching an optional user/exe/exe_dir filter,
	// for `process_count` checks. Nil makes the check do a self-contained scan.
	ProcessCount ProcessCountFunc
	// PidfileFallbackPIDs reports backend-native service PIDs when the active
	// init system does not publish a PIDFile. It lets catalog pidfile checks
	// accept systemd's MainPID/cgroup process set instead of failing on an
	// intentionally absent pidfile.
	PidfileFallbackPIDs func() []int
	// StorageUsage reports filesystem usage for `storage` checks. Nil uses statfs.
	StorageUsage StorageUsageFunc
	// NetSampler observes a network interface for `net` checks. Nil uses /sys.
	NetSampler NetSamplerFunc
	// PingSampler probes a host via ICMP for `icmp` checks. Nil uses native ICMP.
	PingSampler PingSamplerFunc
	// SwapSampler reads system swap for `swap` checks. Nil reads /proc.
	SwapSampler SwapSamplerFunc
	// RouteSampler lists the up default routes for `route` checks. Nil reads
	// /proc/net/route and /proc/net/ipv6_route.
	RouteSampler RouteSamplerFunc
	// LoadSampler reads load averages for `load` checks. Nil reads /proc.
	LoadSampler LoadSamplerFunc
	// UsersSampler counts logged-in users for `users` checks. Nil reads utmp.
	UsersSampler UsersSamplerFunc
	// OomSampler reads the cumulative OOM-kill counter for `oom` checks. Nil reads
	// /proc/vmstat.
	OomSampler OomSamplerFunc
	// FdsSampler reads system file-descriptor usage for `fds` checks. Nil reads
	// /proc/sys/fs/file-nr.
	FdsSampler FdsSamplerFunc
	// MemorySampler reads system RAM for `memory` checks. Nil reads /proc/meminfo.
	MemorySampler MemorySamplerFunc
	// PressureSampler reads kernel PSI for `pressure` checks. Nil reads
	// /proc/pressure/<resource>.
	PressureSampler PressureSamplerFunc
	// PidsSampler reads the kernel PID table for `pids` checks. Nil reads
	// /proc/loadavg and kernel.pid_max.
	PidsSampler PidsSamplerFunc
	// DiskIOSampler reads a block device's counters for `diskio` checks. Nil
	// reads /proc/diskstats.
	DiskIOSampler DiskIOSamplerFunc
	// SensorSampler reads hardware sensors for `sensors` checks. Nil reads hwmon.
	SensorSampler SensorSamplerFunc
	// RaidSampler reads Linux md RAID state for `raid` checks. Nil reads
	// /proc/mdstat.
	RaidSampler RaidSamplerFunc
	// EdacSampler reads EDAC memory-error counters for `edac` checks. Nil reads
	// sysfs.
	EdacSampler EdacSamplerFunc
	// MountSampler reads the mount table for `storage` mount predicates and
	// `autofs` checks. Nil reads /proc/mounts.
	MountSampler MountSamplerFunc
	// ConntrackSampler reads the netfilter conntrack table for `conntrack` checks.
	// Nil reads /proc/sys/net/netfilter.
	ConntrackSampler ConntrackSamplerFunc
	// FirewallRulesSampler reads loaded nftables/iptables rules for
	// `firewall_rules` checks. Nil runs nft/iptables-save through Runner.
	FirewallRulesSampler FirewallRulesSamplerFunc
	// EntropySampler reads the kernel entropy pool for `entropy` checks. Nil reads
	// /proc/sys/kernel/random/entropy_avail.
	EntropySampler EntropySamplerFunc
	// ZombieSampler counts zombie processes for `zombies` checks. Nil scans /proc.
	ZombieSampler ZombieSamplerFunc
	// CertSampler fetches a TLS endpoint's certificate for `cert` checks. Nil dials
	// the host.
	CertSampler CertSamplerFunc
	// SizeSampler measures the byte size of a file or directory for `size` checks.
	// Nil uses os.Stat (file) / a recursive walk (directory).
	SizeSampler SizeSamplerFunc
}

// BuildWarning is an unusable check entry reported during construction. It can be
// rendered as operator-facing text or folded into an Outcome as a failed Result.
type BuildWarning struct {
	Service  string
	Check    string
	Text     string
	Optional bool
}

// String returns the warning text historically returned by Build.
func (w BuildWarning) String() string { return w.Text }

// Result returns a failed check result for this build warning. Optional malformed
// checks remain optional warnings; required malformed checks block preflight and
// start-verification like any other required check failure.
func (w BuildWarning) Result() Result {
	return Result{Service: w.Service, Check: w.Check, OK: false, Optional: w.Optional, Message: w.Text}
}

// BuildWarningResults converts build warnings into check results.
func BuildWarningResults(warnings []BuildWarning) []Result {
	return mapWarnings(warnings, BuildWarning.Result)
}

// mapWarnings converts warnings element-by-element via conv, preserving the
// nil-in/nil-out convention of the warning views.
func mapWarnings[T any](warnings []BuildWarning, conv func(BuildWarning) T) []T {
	if len(warnings) == 0 {
		return nil
	}
	out := make([]T, 0, len(warnings))
	for _, w := range warnings {
		out = append(out, conv(w))
	}
	return out
}

// Build turns a checks/preflight section (a map keyed by check name)
// into runnable checks, skipping `enabled: false` entries and reporting unusable
// ones as warnings. Entries are built in name order for stable output.
func Build(section map[string]any, deps Deps) ([]Built, []string) {
	built, warnings := BuildWithWarnings(section, deps)
	return built, BuildWarningStrings(warnings)
}

// BuildWarningStrings renders build warnings as operator-facing strings.
func BuildWarningStrings(warnings []BuildWarning) []string {
	return mapWarnings(warnings, BuildWarning.String)
}

// BuildWithWarnings is Build's structured form. Use it where build warnings
// must participate in check outcomes, not only be printed.
func BuildWithWarnings(section map[string]any, deps Deps) ([]Built, []BuildWarning) {
	if section == nil {
		return nil, nil
	}

	runner, client := buildDependencies(deps)

	var built []Built
	var warnings []BuildWarning
	for _, name := range slices.Sorted(maps.Keys(section)) {
		entry, ok := section[name].(map[string]any)
		if !ok {
			warnings = append(warnings, BuildWarning{
				Service: deps.Service,
				Check:   name,
				Text:    fmt.Sprintf("check %q is not a mapping", name),
			})
			continue
		}
		if cfgval.Disabled(entry) {
			continue
		}

		typ := cfgval.AsString(entry[CheckKeyType])
		timeout := deps.DefaultTimeout
		if raw, present := entry[CheckKeyTimeout]; present {
			timeout = cfgval.Duration(raw)
			if timeout <= 0 {
				warnings = append(warnings, BuildWarning{
					Service:  deps.Service,
					Check:    name,
					Text:     fmt.Sprintf("check %q: timeout must be a valid positive duration", name),
					Optional: cfgval.Bool(entry[CheckKeyOptional]),
				})
				continue
			}
		}
		b := base{
			name:      name,
			service:   deps.Service,
			timeout:   timeout,
			condition: !IsHealthType(typ),
		}

		check, warn := buildCheck(typ, b, entry, runner, client, deps)
		if warn != "" {
			warnings = append(warnings, BuildWarning{
				Service:  deps.Service,
				Check:    name,
				Text:     fmt.Sprintf("check %q: %s", name, warn),
				Optional: cfgval.Bool(entry[CheckKeyOptional]),
			})
			continue
		}
		built = append(built, Built{Check: withSummary(check, entry), Optional: cfgval.Bool(entry[CheckKeyOptional])})
	}
	return built, warnings
}

type checkBuildInput struct {
	base   base
	entry  map[string]any
	runner execx.Runner
	client *http.Client
	deps   Deps
}

type checkBuilder func(checkBuildInput) (Check, string)

// checkBuilders is the central registry for built-in checks. Connection
// protocols remain in conn's own registry because their types are extensible.
var checkBuilders = map[string]checkBuilder{
	CheckTypeTCP:          func(in checkBuildInput) (Check, string) { return buildTCPCheck(in.base, in.entry) },
	CheckTypePorts:        func(in checkBuildInput) (Check, string) { return buildPortsCheck(in.base, in.entry) },
	CheckTypeHTTP:         func(in checkBuildInput) (Check, string) { return buildHTTPCheck(in.base, in.entry, in.client) },
	CheckTypeCommand:      func(in checkBuildInput) (Check, string) { return buildCommandCheck(in.base, in.entry, in.runner) },
	CheckTypeClock:        func(in checkBuildInput) (Check, string) { return buildClockCheck(in.base, in.entry) },
	CheckTypeService:      func(in checkBuildInput) (Check, string) { return buildServiceCheck(in.base, in.entry, in.deps) },
	CheckTypeFileExists:   func(in checkBuildInput) (Check, string) { return buildFileExistsCheck(in.base, in.entry) },
	CheckTypeFile:         func(in checkBuildInput) (Check, string) { return buildFileCheck(in.base, in.entry) },
	CheckTypeLockfile:     func(in checkBuildInput) (Check, string) { return buildLockfileCheck(in.base, in.entry) },
	CheckTypeBinary:       func(in checkBuildInput) (Check, string) { return buildBinaryCheck(in.base, in.entry) },
	CheckTypePidfile:      func(in checkBuildInput) (Check, string) { return buildPidfileCheck(in.base, in.entry, in.deps) },
	CheckTypeSocket:       func(in checkBuildInput) (Check, string) { return buildSocketCheck(in.base, in.entry) },
	CheckTypeLibraries:    func(in checkBuildInput) (Check, string) { return buildLibrariesCheck(in.base, in.entry) },
	CheckTypeMetric:       func(in checkBuildInput) (Check, string) { return buildMetricCheck(in.base, in.entry, in.deps) },
	CheckTypeProcess:      func(in checkBuildInput) (Check, string) { return buildProcessCheck(in.base, in.entry, in.deps) },
	CheckTypeCount:        func(in checkBuildInput) (Check, string) { return buildCountCheck(in.base, in.entry) },
	CheckTypeStorage:      func(in checkBuildInput) (Check, string) { return buildStorageCheck(in.base, in.entry, in.deps) },
	CheckTypeAutofs:       func(in checkBuildInput) (Check, string) { return buildAutofsCheck(in.base, in.entry, in.deps) },
	CheckTypeNet:          func(in checkBuildInput) (Check, string) { return buildNetCheck(in.base, in.entry, in.deps) },
	CheckTypeLoad:         func(in checkBuildInput) (Check, string) { return buildLoadCheck(in.base, in.entry, in.deps) },
	CheckTypeUsers:        func(in checkBuildInput) (Check, string) { return buildUsersCheck(in.base, in.entry, in.deps) },
	CheckTypeProcessCount: func(in checkBuildInput) (Check, string) { return buildProcessCountCheck(in.base, in.entry, in.deps) },
	CheckTypeHdparm:       func(in checkBuildInput) (Check, string) { return buildHdparmCheck(in.base, in.entry, in.runner) },
	CheckTypeSensors:      func(in checkBuildInput) (Check, string) { return buildSensorsCheck(in.base, in.entry, in.deps) },
	CheckTypeSmart:        func(in checkBuildInput) (Check, string) { return buildSmartCheck(in.base, in.entry, in.runner) },
	CheckTypeRAID:         func(in checkBuildInput) (Check, string) { return buildRaidCheck(in.base, in.entry, in.deps) },
	CheckTypeLVM:          func(in checkBuildInput) (Check, string) { return buildLVMCheck(in.base, in.entry, in.runner) },
	CheckTypeEDAC:         func(in checkBuildInput) (Check, string) { return buildEdacCheck(in.base, in.entry, in.deps) },
	CheckTypeConfig:       func(in checkBuildInput) (Check, string) { return buildConfigCheck(in.base, in.entry, in.runner) },
	CheckTypeFDS:          func(in checkBuildInput) (Check, string) { return buildFdsCheck(in.base, in.entry, in.deps) },
	CheckTypeMemory:       func(in checkBuildInput) (Check, string) { return buildMemoryCheck(in.base, in.entry, in.deps) },
	CheckTypePressure:     func(in checkBuildInput) (Check, string) { return buildPressureCheck(in.base, in.entry, in.deps) },
	CheckTypePIDs:         func(in checkBuildInput) (Check, string) { return buildPidsCheck(in.base, in.entry, in.deps) },
	CheckTypeDiskIO:       func(in checkBuildInput) (Check, string) { return buildDiskIOCheck(in.base, in.entry, in.deps) },
	CheckTypeConntrack:    func(in checkBuildInput) (Check, string) { return buildConntrackCheck(in.base, in.entry, in.deps) },
	CheckTypeFirewallRules: func(in checkBuildInput) (Check, string) {
		return buildFirewallRulesCheck(in.base, in.entry, in.runner, in.deps)
	},
	CheckTypeEntropy:       func(in checkBuildInput) (Check, string) { return buildEntropyCheck(in.base, in.entry, in.deps) },
	CheckTypeZombies:       func(in checkBuildInput) (Check, string) { return buildZombieCheck(in.base, in.entry, in.deps) },
	CheckTypeOOM:           func(in checkBuildInput) (Check, string) { return buildOomCheck(in.base, in.entry, in.deps) },
	CheckTypeCert:          func(in checkBuildInput) (Check, string) { return buildCertCheck(in.base, in.entry, in.deps) },
	CheckTypeSQLite:        func(in checkBuildInput) (Check, string) { return buildSqliteCheck(in.base, in.entry) },
	CheckTypeSQLite3:       func(in checkBuildInput) (Check, string) { return buildSqliteCheck(in.base, in.entry) },
	CheckTypeSwap:          func(in checkBuildInput) (Check, string) { return buildSwapCheck(in.base, in.entry, in.deps) },
	CheckTypeICMP:          func(in checkBuildInput) (Check, string) { return buildICMPCheck(in.base, in.entry, in.deps) },
	CheckTypeRoute:         func(in checkBuildInput) (Check, string) { return buildRouteCheck(in.base, in.entry, in.deps) },
	CheckTypeSQL:           func(in checkBuildInput) (Check, string) { return buildSQLCheck(in.base, in.entry) },
	CheckTypeMongoDBQuery:  func(in checkBuildInput) (Check, string) { return buildMongoCheck(in.base, in.entry) },
	CheckTypeInfluxDBQuery: func(in checkBuildInput) (Check, string) { return buildInfluxCheck(in.base, in.entry) },
	CheckTypeWebsocket:     func(in checkBuildInput) (Check, string) { return buildWebsocketCheck(in.base, in.entry) },
	CheckTypeSize:          func(in checkBuildInput) (Check, string) { return buildSizeCheck(in.base, in.entry, in.deps) },
}

func buildCheck(typ string, b base, entry map[string]any, runner execx.Runner, client *http.Client, deps Deps) (Check, string) {
	if typ == "" {
		return nil, "missing type"
	}
	if builder, ok := checkBuilders[typ]; ok {
		return builder(checkBuildInput{base: b, entry: entry, runner: runner, client: client, deps: deps})
	}
	// A connection-protocol check (mysql, …) is owned by conn's extensible
	// registry, so new protocols need no change in this builder.
	if proto, ok := conn.Lookup(typ); ok {
		return buildConnCheck(b, proto, entry)
	}
	return nil, fmt.Sprintf("unsupported type %q", typ)
}

// buildConfigCheck builds a configuration validity/change check.
func buildConfigCheck(b base, entry map[string]any, runner execx.Runner) (Check, string) {
	argv := cfgval.StringArray(entry[CheckKeyCommand])
	paths := cfgval.StringList(entry[CheckKeyPath])
	if len(argv) == 0 && len(paths) == 0 {
		return nil, "config check requires a command and/or path"
	}
	c := configCheck{base: b, runner: runner, argv: argv, user: cfgval.String(entry[CheckKeyUser]), paths: paths}
	if c.onChange = cfgval.Bool(entry[CheckKeyOnChange]); c.onChange {
		c.state = &cmdState{}
	}
	return c, ""
}

// buildCertCheck builds a TLS/PEM certificate check (host or path).
func buildCertCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	host := cfgval.AsString(entry[CheckKeyHost])
	path := cfgval.AsString(entry[CheckKeyPath])
	switch {
	case host == "" && path == "":
		return nil, "cert check requires a host or a path"
	case host != "" && path != "":
		return nil, "cert check: host and path are mutually exclusive"
	}
	port := defaultTLSPort
	if p, ok := cfgval.Int(entry[CheckKeyPort]); ok {
		port = strconv.Itoa(p)
	}
	serverName := cfgval.AsString(entry[CheckKeyServerName])
	if serverName == "" {
		serverName = host
	}
	days := 0
	if v, ok := cfgval.Int(entry[CheckKeyExpiresInDays]); ok {
		days = v
	}
	verify := boolDefaultTrue(entry[CheckKeyCertVerify])
	return &certCheck{
		base:           b,
		host:           host,
		port:           port,
		serverName:     serverName,
		path:           path,
		expiresInDays:  days,
		onAlgoChange:   cfgval.Bool(entry[CheckKeyOnAlgorithmChange]),
		onIssuerChange: cfgval.Bool(entry[CheckKeyOnIssuerChange]),
		onChange:       cfgval.Bool(entry[CheckKeyOnChange]),
		verify:         verify,
		sampler:        deps.CertSampler,
	}, ""
}

// buildSqliteCheck builds a SQLite integrity check.
func buildSqliteCheck(b base, entry map[string]any) (Check, string) {
	path, errs := requireCheckPath(entry, CheckTypeSQLite)
	if errs != "" {
		return nil, errs
	}
	return sqliteCheck{base: b, path: path, quick: cfgval.Bool(entry[CheckKeyQuick])}, ""
}

// BuildInline builds a single check from an inline entry (type + fields), used
// by inline rule conditions. It returns an error rather than a
// warning so the caller can surface a malformed inline probe.
func BuildInline(name string, entry map[string]any, deps Deps) (Check, error) {
	runner, client := buildDependencies(deps)
	typ := cfgval.AsString(entry[CheckKeyType])
	b := base{
		name:      name,
		service:   deps.Service,
		timeout:   cfgval.DurationOr(entry[CheckKeyTimeout], deps.DefaultTimeout),
		condition: !IsHealthType(typ),
	}
	check, warn := buildCheck(typ, b, entry, runner, client, deps)
	if warn != "" {
		return nil, errors.New(warn)
	}
	return withSummary(check, entry), nil
}

func buildDependencies(deps Deps) (execx.Runner, *http.Client) {
	runner := deps.Runner
	runner = execx.RunnerOrDefault(runner)
	client := deps.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	return runner, client
}

// Outcome summarizes a preflight or verification run.
type Outcome struct {
	OK      bool // every required check passed
	Results []Result
}

// Evaluate computes the outcome: a required (non-optional) failure makes it not
// OK; optional failures are warnings only.
func Evaluate(results []Result) Outcome {
	ok := true
	for _, r := range results {
		if !r.OK && !r.Optional {
			ok = false
		}
	}
	return Outcome{OK: ok, Results: results}
}

// pruneWindow drops the samples older than cutoff in place (keeping the
// backing array), preserving order; the sliding-window trim shared by the
// growth-delta checks (count, size).
func pruneWindow[S any](samples []S, cutoff time.Time, at func(S) time.Time) []S {
	kept := samples[:0]
	for _, s := range samples {
		if !at(s).Before(cutoff) {
			kept = append(kept, s)
		}
	}
	return kept
}
