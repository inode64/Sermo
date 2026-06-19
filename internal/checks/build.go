package checks

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quic-go/quic-go/http3"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/servicemgr"
)

// MetricReader returns a sampled metric for a scope (section 12). The daemon
// supplies the per-cycle sample; nil means no metric source (metric checks then
// report unavailable).
type MetricReader func(scope, name string) (metrics.Reading, bool)

// Samplers groups host probes that can be injected for checks. It is a narrow
// dependency bundle: service-specific capabilities such as Status, Metrics,
// Processes and pidfile fallback PIDs stay on Deps.
type Samplers struct {
	DiskUsage            DiskUsageFunc
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
}

// ApplyTo returns deps with every sampler from s copied into it.
func (s Samplers) ApplyTo(deps Deps) Deps {
	deps.DiskUsage = s.DiskUsage
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
	// PidfileFallbackPIDs reports backend-native service PIDs when the active
	// init system does not publish a PIDFile. It lets legacy catalog pidfile
	// checks accept systemd's MainPID/cgroup process set instead of failing on an
	// intentionally absent pidfile.
	PidfileFallbackPIDs func() []int
	// DiskUsage reports filesystem usage for `storage` checks. Nil uses statfs.
	DiskUsage DiskUsageFunc
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
// postflight like any other required check failure.
func (w BuildWarning) Result() Result {
	return Result{Service: w.Service, Check: w.Check, OK: false, Optional: w.Optional, Message: w.Text}
}

// BuildWarningResults converts build warnings into check results.
func BuildWarningResults(warnings []BuildWarning) []Result {
	if len(warnings) == 0 {
		return nil
	}
	results := make([]Result, 0, len(warnings))
	for _, w := range warnings {
		results = append(results, w.Result())
	}
	return results
}

// Build turns a checks/preflight/postflight section (a map keyed by check name)
// into runnable checks, skipping `enabled: false` entries and reporting unusable
// ones as warnings. Entries are built in name order for stable output.
func Build(section map[string]any, deps Deps) ([]Built, []string) {
	built, warnings := BuildWithWarnings(section, deps)
	return built, BuildWarningStrings(warnings)
}

// BuildWarningStrings renders build warnings as operator-facing strings.
func BuildWarningStrings(warnings []BuildWarning) []string {
	if len(warnings) == 0 {
		return nil
	}
	out := make([]string, 0, len(warnings))
	for _, w := range warnings {
		out = append(out, w.String())
	}
	return out
}

// BuildWithWarnings is Build's structured form. Use it where build warnings
// must participate in check outcomes, not only be printed.
func BuildWithWarnings(section map[string]any, deps Deps) ([]Built, []BuildWarning) {
	if section == nil {
		return nil, nil
	}

	runner := deps.Runner
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	client := deps.HTTPClient
	if client == nil {
		client = &http.Client{}
	}

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

		typ := cfgval.AsString(entry["type"])
		b := base{
			name:      name,
			service:   deps.Service,
			timeout:   cfgval.DurationOr(entry["timeout"], deps.DefaultTimeout),
			condition: !IsHealthType(typ),
		}

		check, warn := buildCheck(typ, b, entry, runner, client, deps)
		if warn != "" {
			warnings = append(warnings, BuildWarning{
				Service:  deps.Service,
				Check:    name,
				Text:     fmt.Sprintf("check %q: %s", name, warn),
				Optional: cfgval.Bool(entry["optional"]),
			})
			continue
		}
		built = append(built, Built{Check: check, Optional: cfgval.Bool(entry["optional"])})
	}
	return built, warnings
}

func buildCheck(typ string, b base, entry map[string]any, runner execx.Runner, client *http.Client, deps Deps) (Check, string) {
	switch typ {
	case "tcp":
		return buildTCPCheck(b, entry)
	case "ports":
		return buildPortsCheck(b, entry)
	case "http":
		return buildHTTPCheck(b, entry, client)
	case "command":
		return buildCommandCheck(b, entry, runner)
	case "service":
		return buildServiceCheck(b, entry, deps)
	case "file_exists":
		return buildFileExistsCheck(b, entry)
	case "file":
		return buildFileCheck(b, entry)
	case "binary":
		return buildBinaryCheck(b, entry)
	case "pidfile":
		return buildPidfileCheck(b, entry, deps)
	case "socket":
		return buildSocketCheck(b, entry)
	case "libraries":
		return buildLibrariesCheck(b, entry)
	case "metric":
		return buildMetricCheck(b, entry, deps)
	case "process":
		return buildProcessCheck(b, entry, deps)
	case "count":
		return buildCountCheck(b, entry)
	case "disk", "storage":
		return buildDiskCheck(b, entry, deps)
	case "autofs":
		return buildAutofsCheck(b, entry, deps)
	case "net":
		return buildNetCheck(b, entry, deps)
	case "load":
		return buildLoadCheck(b, entry, deps)
	case "hdparm":
		return buildHdparmCheck(b, entry, runner)
	case "sensors":
		return buildSensorsCheck(b, entry, deps)
	case "smart":
		return buildSmartCheck(b, entry, runner)
	case "raid":
		return buildRaidCheck(b, entry, deps)
	case "edac":
		return buildEdacCheck(b, entry, deps)
	case "config":
		return buildConfigCheck(b, entry, runner)
	case "fds":
		return buildFdsCheck(b, entry, deps)
	case "memory":
		return buildMemoryCheck(b, entry, deps)
	case "pressure":
		return buildPressureCheck(b, entry, deps)
	case "pids":
		return buildPidsCheck(b, entry, deps)
	case "diskio":
		return buildDiskIOCheck(b, entry, deps)
	case "conntrack":
		return buildConntrackCheck(b, entry, deps)
	case "firewall_rules":
		return buildFirewallRulesCheck(b, entry, runner, deps)
	case "entropy":
		return buildEntropyCheck(b, entry, deps)
	case "zombies":
		return buildZombieCheck(b, entry, deps)
	case "oom":
		return buildOomCheck(b, entry, deps)
	case "cert":
		return buildCertCheck(b, entry, deps)
	case "sqlite", "sqlite3":
		return buildSqliteCheck(b, entry)
	case "swap":
		return buildSwapCheck(b, entry, deps)
	case "icmp":
		return buildICMPCheck(b, entry, deps)
	case "route":
		return buildRouteCheck(b, entry, deps)
	case "sql":
		return buildSQLCheck(b, entry)
	case "mongodb-query":
		return buildMongoCheck(b, entry)
	case "influxdb-query":
		return buildInfluxCheck(b, entry)
	case "websocket", "ws":
		return buildWebsocketCheck(b, entry)
	case "size":
		return buildSizeCheck(b, entry, deps)
	case "":
		return nil, "missing type"
	default:
		// A connection-protocol check (mysql, …): the type names a protocol in
		// the conn registry. New protocols register themselves and need no case
		// here.
		if proto, ok := conn.Lookup(typ); ok {
			return buildConnCheck(b, proto, entry)
		}
		return nil, fmt.Sprintf("unsupported type %q", typ)
	}
}

// buildTCPCheck builds a tcp connectivity check.
func buildTCPCheck(b base, entry map[string]any) (Check, string) {
	port, ok := cfgval.Int(entry["port"])
	if !ok {
		return nil, "tcp check requires a numeric port"
	}
	host := cfgval.AsString(entry["host"])
	if host == "" {
		host = "127.0.0.1"
	}
	all, iwarn := parseInterfaceMatch(entry)
	if iwarn != "" {
		return nil, "tcp check: " + iwarn
	}
	return tcpCheck{base: b, host: host, ifaces: parseInterfaces(entry["interface"]), ifaceAll: all, port: port}, ""
}

// buildPortsCheck builds a multi-port open/closed check.
func buildPortsCheck(b base, entry map[string]any) (Check, string) {
	host := cfgval.AsString(entry["host"])
	if host == "" {
		host = "127.0.0.1"
	}
	ports, err := parsePortSpec(cfgval.AsString(entry["ports"]))
	if err != nil {
		return nil, "ports check: " + err.Error()
	}
	expect := cfgval.AsString(entry["expect"])
	if expect == "" {
		expect = "open"
	}
	if expect != "open" && expect != "closed" && expect != "any" {
		return nil, "ports check: expect must be open, closed or any"
	}
	match := cfgval.AsString(entry["match"])
	if match == "" {
		match = "all"
	}
	if match != "all" && match != "any" && match != "none" {
		return nil, "ports check: match must be all, any or none"
	}
	allIf, iwarn := parseInterfaceMatch(entry)
	if iwarn != "" {
		return nil, "ports check: " + iwarn
	}
	return &portsCheck{
		base:           b,
		host:           host,
		ifaces:         parseInterfaces(entry["interface"]),
		ifaceAll:       allIf,
		ports:          ports,
		expect:         expect,
		match:          match,
		onChange:       cfgval.Bool(entry["on_change"]),
		connectTimeout: cfgval.DurationOr(entry["connect_timeout"], 0),
	}, ""
}

// buildHTTPCheck builds an http(s) check, configuring proxy, http3 and interface
// egress on a per-check client when requested.
func buildHTTPCheck(b base, entry map[string]any, client *http.Client) (Check, string) {
	rawURL := cfgval.AsString(entry["url"])
	if rawURL == "" {
		return nil, "http check requires a url"
	}
	method := strings.ToUpper(cfgval.AsString(entry["method"]))
	if method == "" {
		method = http.MethodGet
	}
	expect, err := parseStatusMatcher(entry["expect_status"])
	if err != nil {
		return nil, "http check: " + err.Error()
	}
	var body []byte
	contentType := ""
	if j, ok := entry["json"]; ok && j != nil {
		raw, err := json.Marshal(j)
		if err != nil {
			return nil, "http check: invalid json body: " + err.Error()
		}
		body, contentType = raw, "application/json"
	} else if s := cfgval.AsString(entry["body"]); s != "" {
		body = []byte(s)
	}
	reqClient := client
	proxyURL, pwarn := parseProxyURL(entry)
	if pwarn != "" {
		return nil, pwarn
	}
	if cfgval.Bool(entry["http3"]) {
		// HTTP/3 runs over QUIC (always TLS 1.3) and cannot use an HTTP
		// forward proxy. The transport never falls back to TCP, so a failure
		// to reach the endpoint over QUIC fails (alerts) the check.
		if u, err := url.Parse(rawURL); err != nil || u.Scheme != "https" {
			return nil, "http check: http3 requires an https url"
		}
		if proxyURL != nil {
			return nil, "http check: http3 and proxy are mutually exclusive"
		}
		reqClient = &http.Client{Transport: &http3.Transport{}}
	} else if proxyURL != nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = http.ProxyURL(proxyURL)
		reqClient = &http.Client{Transport: tr}
	}
	// interface: egress the HTTP request (and any proxy connection) through a
	// specific interface by binding the transport's dialer. The http client has
	// one fixed transport, so it honors a single interface (the first listed).
	if ifaces := parseInterfaces(entry["interface"]); len(ifaces) > 0 {
		if cfgval.Bool(entry["http3"]) {
			return nil, "http check: http3 and interface are mutually exclusive"
		}
		tr := http.DefaultTransport.(*http.Transport).Clone()
		if proxyURL != nil {
			tr.Proxy = http.ProxyURL(proxyURL)
		}
		tr.DialContext = conn.BindDialer(ifaces[0]).DialContext
		reqClient = &http.Client{Transport: tr}
	}
	hc := &httpCheck{
		base:        b,
		client:      reqClient,
		url:         rawURL,
		method:      method,
		headers:     cfgval.StringMap(entry["headers"]),
		body:        body,
		contentType: contentType,
		expect:      expect,
		expectJSON:  parseJSONAssertions(entry["expect_json"]),
	}
	// expect_body is either a substring (string form) or an {op, value}
	// operator comparison against the trimmed body.
	switch eb := entry["expect_body"].(type) {
	case string:
		hc.expectBody = eb
	case map[string]any:
		op := cfgval.AsString(eb["op"])
		if !validCompareOp(op) {
			return nil, "http expect_body op must be one of ==, !=, >, >=, <, <=, =~"
		}
		hc.bodyOp, hc.bodyValue = op, cfgval.String(eb["value"])
	}
	lop, lval, lwarn := parseExpectLatency(entry)
	if lwarn != "" {
		return nil, "http " + lwarn
	}
	hc.latencyOp, hc.latencyValue = lop, lval
	if warn := configureHTTPCert(hc, entry, rawURL); warn != "" {
		return nil, warn
	}
	return hc, ""
}

// buildCommandCheck builds a check that runs a command and asserts its exit code.
func buildCommandCheck(b base, entry map[string]any, runner execx.Runner) (Check, string) {
	argv := cfgval.StringArray(entry["command"])
	if len(argv) == 0 {
		return nil, "command check requires a non-empty command array"
	}
	expect := 0
	if v, ok := cfgval.Int(entry["expect_exit"]); ok {
		expect = v
	}
	stdout, warn := ParseOutputMatcher(entry["expect_stdout"])
	if warn != "" {
		return nil, "command check expect_stdout " + warn
	}
	stderr, warn := ParseOutputMatcher(entry["expect_stderr"])
	if warn != "" {
		return nil, "command check expect_stderr " + warn
	}
	analyzer, warn := parseAnalyzer(entry["analyze"])
	if warn != "" {
		return nil, "command check " + warn
	}
	exports, warn := parseCommandExports(b.name, entry["export"])
	if warn != "" {
		return nil, "command check " + warn
	}
	c := commandCheck{base: b, runner: runner, argv: argv, expectExit: expect, stdout: stdout, stderr: stderr, exports: exports, analyzer: analyzer}
	if c.onChange = cfgval.Bool(entry["on_change"]); c.onChange {
		c.state = &cmdState{}
	}
	return c, ""
}

type commandExport struct {
	name         string
	from         string
	trim         bool
	defaultValue string
	regex        *regexp.Regexp
	shortVersion bool
}

func parseCommandExports(checkName string, raw any) ([]commandExport, string) {
	exports := map[string]commandExport{}
	switch checkName {
	case "version":
		exports["version"] = defaultCommandExport("version")
		short := defaultCommandExport("version_short")
		short.shortVersion = true
		exports["version_short"] = short
	case "version_short":
		exports[checkName] = defaultCommandExport(checkName)
	}
	if raw == nil {
		return sortedCommandExports(exports), ""
	}
	specs, ok := raw.(map[string]any)
	if !ok {
		return nil, "export must be a mapping of variable name -> export rule"
	}
	for _, name := range slices.Sorted(maps.Keys(specs)) {
		spec, ok := specs[name].(map[string]any)
		if !ok {
			return nil, "export." + name + " must be a mapping"
		}
		e := defaultCommandExport(name)
		if from := cfgval.String(spec["from"]); from != "" {
			e.from = from
		}
		switch e.from {
		case "stdout", "stderr":
		default:
			return nil, "export." + name + ".from must be stdout or stderr"
		}
		if rawTrim, present := spec["trim"]; present {
			v, ok := rawTrim.(bool)
			if !ok {
				return nil, "export." + name + ".trim must be a boolean"
			}
			e.trim = v
		}
		if rawDefault, present := spec["default"]; present {
			e.defaultValue = cfgval.String(rawDefault)
		}
		if rawRegex, present := spec["regex"]; present {
			re, err := regexp.Compile(cfgval.String(rawRegex))
			if err != nil {
				return nil, "export." + name + ".regex is invalid: " + err.Error()
			}
			e.regex = re
		}
		exports[name] = e
	}
	return sortedCommandExports(exports), ""
}

func defaultCommandExport(name string) commandExport {
	return commandExport{name: name, from: "stdout", trim: true}
}

var commandShortVersionRE = regexp.MustCompile(`[0-9]+\.[0-9]+(?:\.[0-9]+)?`)
var commandShortIntegerVersionRE = regexp.MustCompile(`(?i)\b(?:version|v)\s*:?\s*([0-9]+)\b`)

func commandShortVersion(s string) string {
	if dotted := commandShortVersionRE.FindString(s); dotted != "" {
		return dotted
	}
	if match := commandShortIntegerVersionRE.FindStringSubmatch(s); len(match) > 1 {
		return match[1]
	}
	return ""
}

func sortedCommandExports(exports map[string]commandExport) []commandExport {
	if len(exports) == 0 {
		return nil
	}
	out := make([]commandExport, 0, len(exports))
	for _, name := range slices.Sorted(maps.Keys(exports)) {
		out = append(out, exports[name])
	}
	return out
}

func (e commandExport) value(stdout, stderr string) string {
	source := stdout
	if e.from == "stderr" {
		source = stderr
	}
	value := source
	if e.regex != nil {
		match := e.regex.FindStringSubmatch(source)
		if match == nil {
			value = e.defaultValue
		} else if len(match) > 1 {
			value = match[1]
		} else {
			value = match[0]
		}
	} else if e.shortVersion {
		value = commandShortVersion(source)
		if value == "" {
			value = e.defaultValue
		}
	}
	if e.trim {
		value = strings.TrimSpace(value)
	}
	return value
}

// buildServiceCheck builds a check on a service-manager unit's expected state.
func buildServiceCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	expect := cfgval.AsString(entry["expect"])
	if expect == "" {
		return nil, "service check requires expect"
	}
	if deps.Status == nil {
		return nil, "service check needs backend detection, unavailable here"
	}
	return serviceCheck{base: b, expect: expect, status: deps.Status}, ""
}

// buildFileExistsCheck builds a check that a path exists.
func buildFileExistsCheck(b base, entry map[string]any) (Check, string) {
	path := cfgval.AsString(entry["path"])
	if path == "" {
		return nil, "file_exists check requires a path"
	}
	return fileExistsCheck{base: b, path: path}, ""
}

// buildFileCheck builds a check that a path exists and is a regular file.
func buildFileCheck(b base, entry map[string]any) (Check, string) {
	path := cfgval.AsString(entry["path"])
	if path == "" {
		return nil, "file check requires a path"
	}
	return fileCheck{base: b, path: path}, ""
}

// buildPidfileCheck builds a check that a pidfile exists and references a running
// process. Gate it with `requires: [service]` so it only errors while the service
// is active.
func buildPidfileCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	paths := cfgval.StringList(entry["path"])
	if len(paths) == 0 {
		return nil, "pidfile check requires a path"
	}
	return pidfileCheck{base: b, paths: paths, fallbackPIDs: deps.PidfileFallbackPIDs}, ""
}

// buildSocketCheck builds a check that one Unix socket candidate exists.
func buildSocketCheck(b base, entry map[string]any) (Check, string) {
	paths := cfgval.StringList(entry["path"])
	if len(paths) == 0 {
		return nil, "socket check requires a path"
	}
	return socketCheck{base: b, paths: paths}, ""
}

// buildBinaryCheck builds a check on a binary's fingerprint.
func buildBinaryCheck(b base, entry map[string]any) (Check, string) {
	path := cfgval.AsString(entry["path"])
	if path == "" {
		return nil, "binary check requires a path"
	}
	return binaryCheck{base: b, path: path}, ""
}

// buildLibrariesCheck builds a check on a binary's shared-library dependencies.
// Implemented natively with debug/elf (no ldd).
func buildLibrariesCheck(b base, entry map[string]any) (Check, string) {
	binary := cfgval.AsString(entry["binary"])
	if binary == "" {
		return nil, "libraries check requires a binary"
	}
	return librariesCheck{base: b, binary: binary}, ""
}

// buildMetricCheck builds a check comparing a sampled metric to a threshold.
func buildMetricCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	name := cfgval.AsString(entry["name"])
	if name == "" {
		return nil, "metric check requires a name"
	}
	scope := cfgval.AsString(entry["scope"])
	if scope == "" {
		scope = "service"
	}
	op := cfgval.AsString(entry["op"])
	if op == "" {
		return nil, "metric check requires an op"
	}
	if deps.Metrics == nil {
		return nil, "metric check needs a metric source, unavailable here"
	}
	return metricCheck{base: b, scope: scope, metric: name, op: op, value: cfgval.String(entry["value"]), source: deps.Metrics}, ""
}

// buildProcessCheck builds a check on processes matching an exe/user selector.
func buildProcessCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	exe := cfgval.AsString(entry["exe"])
	user := cfgval.AsString(entry["user"])
	if exe == "" {
		return nil, "process check requires exe"
	}
	if deps.Processes == nil {
		return nil, "process check needs process discovery, unavailable here"
	}
	expect := cfgval.AsString(entry["state"])
	if expect == "" {
		expect = "running"
	}
	return processCheck{base: b, exe: exe, user: user, expect: expect, observe: deps.Processes}, ""
}

// buildCountCheck builds a check on the number of entries under a path.
func buildCountCheck(b base, entry map[string]any) (Check, string) {
	path := cfgval.AsString(entry["path"])
	if path == "" {
		return nil, "count check requires a path"
	}
	kind := cfgval.AsString(entry["of"])
	if kind == "" {
		kind = countAny
	}
	if !validCountKind(kind) {
		return nil, "count check `of` must be file, dir, symlink or any"
	}
	// The threshold may sit at the top level (op/value) or be nested under
	// `count: {op, value}` like every other named predicate.
	threshold := entry
	if m, ok := entry["count"].(map[string]any); ok {
		threshold = m
	}
	op := cfgval.AsString(threshold["op"])
	if !cfgval.IsCompareOp(op) {
		return nil, "count check requires a valid op (>=, >, <=, <, ==, !=)"
	}
	val, err := strconv.ParseFloat(cfgval.String(threshold["value"]), 64)
	if err != nil {
		return nil, "count check value must be numeric"
	}
	return countCheck{base: b, path: path, kind: kind, recursive: cfgval.Bool(entry["recursive"]), op: op, value: val}, ""
}

// buildDiskCheck builds a storage space/inode and/or mount check.
func buildDiskCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	path := cfgval.AsString(entry["path"])
	if path == "" {
		return nil, "storage check requires a path"
	}
	preds, err := parseLevelPreds(entry, DiskPredFields)
	if err != nil {
		return nil, "storage check: " + err.Error()
	}
	mount := parseMountCond(entry)
	if len(preds) == 0 && !mount.active {
		return nil, "storage check requires a space/inode predicate (used_pct/free_pct/used_bytes/free_bytes/inodes_*) and/or a mount condition (mounted)"
	}
	return diskCheck{base: b, path: path, preds: preds, usage: deps.DiskUsage, mount: mount, mountSampler: deps.MountSampler}, ""
}

// buildAutofsCheck builds an autofs automounter check.
func buildAutofsCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	path := cfgval.AsString(entry["path"])
	op, value := "", 0.0
	if m, ok := entry["count"].(map[string]any); ok {
		op = cfgval.AsString(m["op"])
		if !cfgval.IsCompareOp(op) {
			return nil, "autofs check count has an invalid op (>=, >, <=, <, ==, !=)"
		}
		v, err := strconv.ParseFloat(cfgval.String(m["value"]), 64)
		if err != nil {
			return nil, "autofs check count value must be numeric"
		}
		value = v
	}
	if path != "" && op != "" {
		return nil, "autofs check: path and count are mutually exclusive"
	}
	return autofsCheck{base: b, path: path, op: op, value: value, sampler: deps.MountSampler}, ""
}

// buildNetCheck builds a network-interface state/speed/errors check.
func buildNetCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	iface := cfgval.AsString(entry["interface"])
	if iface == "" {
		return nil, "net check requires an interface"
	}
	metric := cfgval.AsString(entry["metric"])
	c := &netCheck{base: b, iface: iface, metric: metric, sampler: deps.NetSampler}
	switch metric {
	case "state":
		expect := cfgval.AsString(entry["expect"])
		onChange := cfgval.AsString(entry["on"]) == "change"
		if expect == "" && !onChange {
			return nil, "net state requires expect: up|down or on: change"
		}
		if expect != "" {
			if expect != "up" && expect != "down" {
				return nil, "net state expect must be up or down"
			}
			c.expect = expect
		} else if onChange {
			c.onChange = true
		}
	case "speed":
		if cfgval.AsString(entry["on"]) != "change" {
			return nil, "net speed requires on: change"
		}
		c.onChange = true
	case "errors":
		c.counters = cfgval.StringArray(entry["counters"])
		if len(c.counters) == 0 {
			c.counters = []string{"rx_errors", "tx_errors"}
		}
		op, v, errs := parseDeltaThreshold(entry["delta"], "net errors")
		if errs != "" {
			return nil, errs
		}
		c.op, c.value = op, v
	case "address":
		expect := cfgval.AsString(entry["expect"])
		onChange := cfgval.AsString(entry["on"]) == "change"
		if expect == "" && !onChange {
			return nil, "net address requires expect: present|absent or on: change"
		}
		if expect != "" {
			if expect != "present" && expect != "absent" {
				return nil, "net address expect must be present or absent"
			}
			c.expect = expect
		} else if onChange {
			c.onChange = true
		}
	default:
		return nil, "net check metric must be state, speed, errors or address"
	}
	return c, ""
}

// buildLoadCheck builds a system load-average check.
func buildLoadCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, LoadPredFields, "load check")
	if errs != "" {
		return nil, errs
	}
	return loadCheck{base: b, preds: preds, perCPU: cfgval.Bool(entry["per_cpu"]), sampler: deps.LoadSampler}, ""
}

// buildHdparmCheck builds a disk-throughput check (hdparm -t/-T).
func buildHdparmCheck(b base, entry map[string]any, runner execx.Runner) (Check, string) {
	device := cfgval.AsString(entry["device"])
	if device == "" {
		return nil, "hdparm check requires a device"
	}
	preds, errs := requireLevelPreds(entry, HdparmPredFields, "hdparm check")
	if errs != "" {
		return nil, errs
	}
	return hdparmCheck{base: b, runner: runner, device: device, preds: preds}, ""
}

// buildSensorsCheck builds a hardware-sensor check (hwmon temp/fan/voltage).
func buildSensorsCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, SensorPredFields, "sensors check")
	if errs != "" {
		return nil, errs
	}
	return sensorsCheck{base: b, chip: cfgval.AsString(entry["chip"]), label: cfgval.AsString(entry["label"]), preds: preds, sampler: deps.SensorSampler}, ""
}

// buildSmartCheck builds a drive SMART-health check (smartctl).
func buildSmartCheck(b base, entry map[string]any, runner execx.Runner) (Check, string) {
	device := cfgval.AsString(entry["device"])
	if device == "" {
		return nil, "smart check requires a device"
	}
	preds, err := parseLevelPreds(entry, SmartPredFields)
	if err != nil {
		return nil, "smart check: " + err.Error()
	}
	return smartCheck{base: b, runner: runner, device: device, preds: preds}, ""
}

// buildRaidCheck builds a Linux md software-RAID health check.
func buildRaidCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, err := parseLevelPreds(entry, RaidPredFields)
	if err != nil {
		return nil, "raid check: " + err.Error()
	}
	return raidCheck{base: b, preds: preds, sampler: deps.RaidSampler}, ""
}

// buildEdacCheck builds an ECC memory-error (EDAC) check.
func buildEdacCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, err := parseLevelPreds(entry, EdacPredFields)
	if err != nil {
		return nil, "edac check: " + err.Error()
	}
	return edacCheck{base: b, preds: preds, sampler: deps.EdacSampler}, ""
}

// buildConfigCheck builds a configuration validity/change check.
func buildConfigCheck(b base, entry map[string]any, runner execx.Runner) (Check, string) {
	argv := cfgval.StringArray(entry["command"])
	paths := cfgval.StringList(entry["path"])
	if len(argv) == 0 && len(paths) == 0 {
		return nil, "config check requires a command and/or path"
	}
	c := configCheck{base: b, runner: runner, argv: argv, paths: paths}
	if c.onChange = cfgval.Bool(entry["on_change"]); c.onChange {
		c.state = &cmdState{}
	}
	return c, ""
}

// buildFdsCheck builds an open file-descriptors check.
func buildFdsCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, FdsPredFields, "fds check")
	if errs != "" {
		return nil, errs
	}
	return fdsCheck{base: b, preds: preds, sampler: deps.FdsSampler}, ""
}

// buildMemoryCheck builds a system RAM check.
func buildMemoryCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, MemoryPredFields, "memory check")
	if errs != "" {
		return nil, errs
	}
	return memoryCheck{base: b, preds: preds, sampler: deps.MemorySampler}, ""
}

// buildPidsCheck builds a kernel PID-table check.
func buildPidsCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, PidsPredFields, "pids check")
	if errs != "" {
		return nil, errs
	}
	return pidsCheck{base: b, preds: preds, sampler: deps.PidsSampler}, ""
}

// buildDiskIOCheck builds a block-device I/O rate check.
func buildDiskIOCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	device := cfgval.AsString(entry["device"])
	if device == "" {
		return nil, "diskio check requires a device (e.g. sda, nvme0n1)"
	}
	preds, errs := requireLevelPreds(entry, DiskIOPredFields, "diskio check")
	if errs != "" {
		return nil, errs
	}
	return &diskIOCheck{base: b, device: device, preds: preds, sampler: deps.DiskIOSampler, state: &diskIOState{}}, ""
}

// buildPressureCheck builds a kernel PSI stall check.
func buildPressureCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	resource := cfgval.AsString(entry["resource"])
	switch resource {
	case "cpu", "memory", "io":
	default:
		return nil, "pressure check requires resource: cpu, memory or io"
	}
	preds, errs := requireLevelPreds(entry, PressurePredFields, "pressure check")
	if errs != "" {
		return nil, errs
	}
	return pressureCheck{base: b, resource: resource, preds: preds, sampler: deps.PressureSampler}, ""
}

// buildConntrackCheck builds a netfilter conntrack-table check.
func buildConntrackCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, ConntrackPredFields, "conntrack check")
	if errs != "" {
		return nil, errs
	}
	return conntrackCheck{base: b, preds: preds, sampler: deps.ConntrackSampler}, ""
}

// buildEntropyCheck builds an available-entropy check.
func buildEntropyCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, EntropyPredFields, "entropy check")
	if errs != "" {
		return nil, errs
	}
	return entropyCheck{base: b, op: preds[0].op, value: preds[0].value, sampler: deps.EntropySampler}, ""
}

// buildZombieCheck builds a zombie-process count check.
func buildZombieCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, ZombiePredFields, "zombies check")
	if errs != "" {
		return nil, errs
	}
	return zombieCheck{base: b, op: preds[0].op, value: preds[0].value, sampler: deps.ZombieSampler}, ""
}

// buildOomCheck builds an OOM-kill delta check (defaults to firing on any kill).
func buildOomCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	// delta is optional; the default fires on any OOM kill (> 0).
	op, value := ">", 0.0
	if raw, present := entry["delta"]; present {
		var errs string
		if op, value, errs = parseDeltaThreshold(raw, "oom"); errs != "" {
			return nil, errs
		}
	}
	return &oomCheck{base: b, op: op, value: value, sampler: deps.OomSampler}, ""
}

// buildCertCheck builds a TLS/PEM certificate check (host or path).
func buildCertCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	host := cfgval.AsString(entry["host"])
	path := cfgval.AsString(entry["path"])
	switch {
	case host == "" && path == "":
		return nil, "cert check requires a host or a path"
	case host != "" && path != "":
		return nil, "cert check: host and path are mutually exclusive"
	}
	port := "443"
	if p, ok := cfgval.Int(entry["port"]); ok {
		port = strconv.Itoa(p)
	}
	serverName := cfgval.AsString(entry["server_name"])
	if serverName == "" {
		serverName = host
	}
	days := 0
	if v, ok := cfgval.Int(entry["expires_in_days"]); ok {
		days = v
	}
	verify := true
	if v, ok := entry["verify"].(bool); ok {
		verify = v
	}
	return &certCheck{
		base:           b,
		host:           host,
		port:           port,
		serverName:     serverName,
		path:           path,
		expiresInDays:  days,
		onAlgoChange:   cfgval.Bool(entry["on_algorithm_change"]),
		onIssuerChange: cfgval.Bool(entry["on_issuer_change"]),
		onChange:       cfgval.Bool(entry["on_change"]),
		verify:         verify,
		sampler:        deps.CertSampler,
	}, ""
}

// buildSqliteCheck builds a SQLite integrity check.
func buildSqliteCheck(b base, entry map[string]any) (Check, string) {
	path := cfgval.AsString(entry["path"])
	if path == "" {
		return nil, "sqlite check requires a path"
	}
	return sqliteCheck{base: b, path: path, quick: cfgval.Bool(entry["quick"])}, ""
}

// buildSwapCheck builds a swap usage or io check.
func buildSwapCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	metric := cfgval.AsString(entry["metric"])
	c := &swapCheck{base: b, metric: metric, sampler: deps.SwapSampler}
	switch metric {
	case "usage":
		preds, errs := requireLevelPreds(entry, SwapUsageFields, "swap usage")
		if errs != "" {
			return nil, errs
		}
		c.preds = preds
	case "io":
		op, v, errs := parseDeltaThreshold(entry["delta"], "swap io")
		if errs != "" {
			return nil, errs
		}
		c.op, c.value = op, v
	default:
		return nil, "swap check metric must be usage or io"
	}
	return c, ""
}

// buildICMPCheck builds an ICMP ping state/latency check.
func buildICMPCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	host := cfgval.AsString(entry["host"])
	if host == "" {
		return nil, "icmp check requires a host"
	}
	count := 3
	if v, ok := cfgval.Int(entry["count"]); ok {
		if v <= 0 {
			return nil, "icmp count must be a positive integer"
		}
		count = v
	}
	metric := cfgval.AsString(entry["metric"])
	allIf, iwarn := parseInterfaceMatch(entry)
	if iwarn != "" {
		return nil, "icmp check: " + iwarn
	}
	c := &icmpCheck{base: b, host: host, ifaces: parseInterfaces(entry["interface"]), ifaceAll: allIf, count: count, metric: metric, sampler: deps.PingSampler}
	switch metric {
	case "state":
		expect := cfgval.AsString(entry["expect"])
		onChange := cfgval.AsString(entry["on"]) == "change"
		if expect == "" && !onChange {
			return nil, "icmp state requires expect: up|down or on: change"
		}
		if expect != "" {
			if expect != "up" && expect != "down" {
				return nil, "icmp state expect must be up or down"
			}
			c.expect = expect
		} else if onChange {
			c.onChange = true
		}
	case "latency":
		th, hasTh := entry["threshold"].(map[string]any)
		ch, hasCh := entry["change"].(map[string]any)
		if !hasTh && !hasCh {
			return nil, "icmp latency requires threshold {op, value} or change {delta}"
		}
		if hasTh {
			op := cfgval.AsString(th["op"])
			if !cfgval.IsCompareOp(op) {
				return nil, "icmp latency threshold has an invalid op"
			}
			v, err := strconv.ParseFloat(cfgval.String(th["value"]), 64)
			if err != nil {
				return nil, "icmp latency threshold value must be numeric"
			}
			c.hasThreshold, c.op, c.value = true, op, v
		} else if hasCh {
			d, err := strconv.ParseFloat(cfgval.String(ch["delta"]), 64)
			if err != nil {
				return nil, "icmp latency change delta must be numeric"
			}
			c.hasChange, c.delta = true, d
		}
	default:
		return nil, "icmp check metric must be state or latency"
	}
	return c, ""
}

// buildRouteCheck builds a default-route presence check.
func buildRouteCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	family := cfgval.AsString(entry["family"])
	switch family {
	case "":
		family = "ipv4"
	case "ipv4", "ipv6":
	default:
		return nil, "route family must be ipv4 or ipv6"
	}
	return routeCheck{base: b, family: family, iface: cfgval.AsString(entry["interface"]), sampler: deps.RouteSampler}, ""
}

// buildSizeCheck builds a path-growth check over a time window.
func buildSizeCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	path := cfgval.AsString(entry["path"])
	if path == "" {
		return nil, "size check requires a path"
	}
	growBy, err := parseSize(cfgval.String(entry["grow_by"]))
	if err != nil || growBy <= 0 {
		return nil, "size check requires a positive grow_by with a K/M/G/T suffix (e.g. 1G)"
	}
	window := cfgval.DurationOr(entry["within"], 0)
	if window <= 0 {
		return nil, "size check requires a positive within (e.g. 1h)"
	}
	return &sizeCheck{base: b, path: path, growBy: growBy, window: window, sampler: deps.SizeSampler, clock: time.Now, state: &sizeState{}}, ""
}

// parseProxyURL reads the optional `proxy` field of an http check (e.g. a Squid
// proxy, "http://[user:pass@]squid:3128"). It returns the parsed URL, or a
// warning when the value is malformed. A nil URL with no warning means no proxy.
func parseProxyURL(entry map[string]any) (*url.URL, string) {
	s := cfgval.AsString(entry["proxy"])
	if s == "" {
		return nil, ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil, "http check: invalid proxy url " + strconv.Quote(s)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return u, ""
	default:
		return nil, "http check: proxy scheme must be http, https or socks5"
	}
}

// httpCertKeys are the optional certificate-inspection keys on the http check.
var httpCertKeys = []string{
	"cert_expires_in_days", "cert_verify",
	"cert_on_change", "cert_on_issuer_change", "cert_on_algorithm_change",
}

// configureHTTPCert enables certificate inspection on hc when any cert_* key is
// present. It requires an https url and returns a warning string on a config
// error (empty when there is nothing to configure or configuration succeeded).
func configureHTTPCert(hc *httpCheck, entry map[string]any, rawURL string) string {
	active := false
	for _, k := range httpCertKeys {
		if _, ok := entry[k]; ok {
			active = true
			break
		}
	}
	if !active {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "http check: invalid url: " + err.Error()
	}
	if u.Scheme != "https" {
		return "http check: cert_* options require an https url"
	}
	verify := true
	if v, ok := entry["cert_verify"].(bool); ok {
		verify = v
	}
	days := 0
	if v, ok := cfgval.Int(entry["cert_expires_in_days"]); ok {
		days = v
	}
	hc.certHost = u.Hostname()
	hc.certOpts = certOptions{
		expiresInDays:  days,
		verify:         verify,
		onAlgoChange:   cfgval.Bool(entry["cert_on_algorithm_change"]),
		onIssuerChange: cfgval.Bool(entry["cert_on_issuer_change"]),
		onChange:       cfgval.Bool(entry["cert_on_change"]),
	}
	if cfgval.Bool(entry["http3"]) {
		// Read the leaf over QUIC too; http3 populates resp.TLS so the same
		// certificate logic applies. TLS 1.3 is enforced by QUIC.
		hc.certClient = &http.Client{Transport: &http3.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}, //nolint:gosec // leaf inspected and verified manually via verifyCertChain
		}}
		return ""
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // leaf inspected and verified manually via verifyCertChain
	if pu, _ := parseProxyURL(entry); pu != nil {
		tr.Proxy = http.ProxyURL(pu) // cert inspection also goes through the proxy (CONNECT for https)
	}
	hc.certClient = &http.Client{Transport: tr}
	return ""
}

// BuildInline builds a single check from an inline entry (type + fields), used
// by inline rule conditions (section 14). It returns an error rather than a
// warning so the caller can surface a malformed inline probe.
func BuildInline(name string, entry map[string]any, deps Deps) (Check, error) {
	runner := deps.Runner
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	client := deps.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	typ := cfgval.AsString(entry["type"])
	b := base{
		name:      name,
		service:   deps.Service,
		timeout:   cfgval.DurationOr(entry["timeout"], deps.DefaultTimeout),
		condition: !IsHealthType(typ),
	}
	check, warn := buildCheck(typ, b, entry, runner, client, deps)
	if warn != "" {
		return nil, errors.New(warn)
	}
	return check, nil
}

// Outcome summarizes a preflight/postflight run.
type Outcome struct {
	OK      bool // every required check passed
	Results []Result
}

// Evaluate computes the outcome: a required (non-optional) failure makes it not
// OK; optional failures are warnings only (section 19).
func Evaluate(results []Result) Outcome {
	ok := true
	for _, r := range results {
		if !r.OK && !r.Optional {
			ok = false
		}
	}
	return Outcome{OK: ok, Results: results}
}

// parseJSONAssertions reads the expect_json mapping into ordered assertions: a
// scalar value is an equality check; a {op, value} mapping is an operator check.
func parseJSONAssertions(v any) []jsonAssertion {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return nil
	}
	out := make([]jsonAssertion, 0, len(m))
	for _, path := range slices.Sorted(maps.Keys(m)) {
		raw := m[path]
		if cond, ok := raw.(map[string]any); ok {
			op := cfgval.AsString(cond["op"])
			if op == "" {
				op = "=="
			}
			out = append(out, jsonAssertion{path: path, op: op, value: cfgval.String(cond["value"])})
		} else {
			out = append(out, jsonAssertion{path: path, op: "==", value: cfgval.String(raw)})
		}
	}
	return out
}

// parseStatusMatcher parses an expect_status field: a single code, a class
// ("2xx"), or a list of either. Empty defaults to 200 (section 12).
func parseStatusMatcher(v any) (statusMatcher, error) {
	if v == nil {
		return statusMatcher{codes: []int{200}}, nil
	}
	// Operator form: {op, value} (e.g. status < 500).
	if cond, ok := v.(map[string]any); ok {
		op := cfgval.AsString(cond["op"])
		if !validCompareOp(op) {
			return statusMatcher{}, fmt.Errorf("expect_status op must be one of ==, !=, >, >=, <, <=, =~")
		}
		return statusMatcher{op: op, value: cfgval.String(cond["value"])}, nil
	}
	var m statusMatcher
	var items []any
	if list, ok := v.([]any); ok {
		items = list
	} else {
		items = []any{v}
	}
	for _, item := range items {
		if n, ok := cfgval.Int(item); ok {
			m.codes = append(m.codes, n)
			continue
		}
		s := strings.TrimSpace(cfgval.AsString(item))
		if len(s) == 3 && (s[1] == 'x' || s[1] == 'X') && (s[2] == 'x' || s[2] == 'X') && s[0] >= '1' && s[0] <= '5' {
			m.classes = append(m.classes, int(s[0]-'0'))
			continue
		}
		return statusMatcher{}, fmt.Errorf("invalid expect_status %q", s)
	}
	return m, nil
}
