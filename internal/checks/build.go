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
	"sermo/internal/httpx"
	"sermo/internal/metrics"
	"sermo/internal/process"
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

const (
	defaultHTTPStatusCode       = http.StatusOK
	httpHeaderAccept            = httpx.HeaderAccept
	httpHeaderContentType       = httpx.HeaderContentType
	httpContentTypeJSON         = httpx.ContentTypeJSON
	httpStatusClassPatternLen   = 3
	httpStatusClassDigitIndex   = 0
	httpStatusClassWildcard1    = 1
	httpStatusClassWildcard2    = 2
	httpStatusClassMinDigit     = '1'
	httpStatusClassMaxDigit     = '5'
	httpStatusClassWildcard     = 'x'
	httpStatusClassWildcardCaps = 'X'
	httpStatusClassDigitBase    = '0'
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
	if len(warnings) == 0 {
		return nil
	}
	results := make([]Result, 0, len(warnings))
	for _, w := range warnings {
		results = append(results, w.Result())
	}
	return results
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

func buildCheck(typ string, b base, entry map[string]any, runner execx.Runner, client *http.Client, deps Deps) (Check, string) {
	switch typ {
	case CheckTypeTCP:
		return buildTCPCheck(b, entry)
	case CheckTypePorts:
		return buildPortsCheck(b, entry)
	case CheckTypeHTTP:
		return buildHTTPCheck(b, entry, client)
	case CheckTypeCommand:
		return buildCommandCheck(b, entry, runner)
	case CheckTypeClock:
		return buildClockCheck(b, entry)
	case CheckTypeService:
		return buildServiceCheck(b, entry, deps)
	case CheckTypeFileExists:
		return buildFileExistsCheck(b, entry)
	case CheckTypeFile:
		return buildFileCheck(b, entry)
	case CheckTypeLockfile:
		return buildLockfileCheck(b, entry)
	case CheckTypeBinary:
		return buildBinaryCheck(b, entry)
	case CheckTypePidfile:
		return buildPidfileCheck(b, entry, deps)
	case CheckTypeSocket:
		return buildSocketCheck(b, entry)
	case CheckTypeLibraries:
		return buildLibrariesCheck(b, entry)
	case CheckTypeMetric:
		return buildMetricCheck(b, entry, deps)
	case CheckTypeProcess:
		return buildProcessCheck(b, entry, deps)
	case CheckTypeCount:
		return buildCountCheck(b, entry)
	case CheckTypeStorage:
		return buildStorageCheck(b, entry, deps)
	case CheckTypeAutofs:
		return buildAutofsCheck(b, entry, deps)
	case CheckTypeNet:
		return buildNetCheck(b, entry, deps)
	case CheckTypeLoad:
		return buildLoadCheck(b, entry, deps)
	case CheckTypeUsers:
		return buildUsersCheck(b, entry, deps)
	case CheckTypeProcessCount:
		return buildProcessCountCheck(b, entry, deps)
	case CheckTypeHdparm:
		return buildHdparmCheck(b, entry, runner)
	case CheckTypeSensors:
		return buildSensorsCheck(b, entry, deps)
	case CheckTypeSmart:
		return buildSmartCheck(b, entry, runner)
	case CheckTypeRAID:
		return buildRaidCheck(b, entry, deps)
	case CheckTypeLVM:
		return buildLVMCheck(b, entry, runner)
	case CheckTypeEDAC:
		return buildEdacCheck(b, entry, deps)
	case CheckTypeConfig:
		return buildConfigCheck(b, entry, runner)
	case CheckTypeFDS:
		return buildFdsCheck(b, entry, deps)
	case CheckTypeMemory:
		return buildMemoryCheck(b, entry, deps)
	case CheckTypePressure:
		return buildPressureCheck(b, entry, deps)
	case CheckTypePIDs:
		return buildPidsCheck(b, entry, deps)
	case CheckTypeDiskIO:
		return buildDiskIOCheck(b, entry, deps)
	case CheckTypeConntrack:
		return buildConntrackCheck(b, entry, deps)
	case CheckTypeFirewallRules:
		return buildFirewallRulesCheck(b, entry, runner, deps)
	case CheckTypeEntropy:
		return buildEntropyCheck(b, entry, deps)
	case CheckTypeZombies:
		return buildZombieCheck(b, entry, deps)
	case CheckTypeOOM:
		return buildOomCheck(b, entry, deps)
	case CheckTypeCert:
		return buildCertCheck(b, entry, deps)
	case CheckTypeSQLite, CheckTypeSQLite3:
		return buildSqliteCheck(b, entry)
	case CheckTypeSwap:
		return buildSwapCheck(b, entry, deps)
	case CheckTypeICMP:
		return buildICMPCheck(b, entry, deps)
	case CheckTypeRoute:
		return buildRouteCheck(b, entry, deps)
	case CheckTypeSQL:
		return buildSQLCheck(b, entry)
	case CheckTypeMongoDBQuery:
		return buildMongoCheck(b, entry)
	case CheckTypeInfluxDBQuery:
		return buildInfluxCheck(b, entry)
	case CheckTypeWebsocket:
		return buildWebsocketCheck(b, entry)
	case CheckTypeSize:
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
	port, ok := cfgval.Int(entry[CheckKeyPort])
	if !ok {
		return nil, "tcp check requires a numeric port"
	}
	host := cfgval.AsString(entry[CheckKeyHost])
	if host == "" {
		host = conn.DefaultHost
	}
	all, iwarn := parseInterfaceMatch(entry)
	if iwarn != "" {
		return nil, "tcp check: " + iwarn
	}
	return tcpCheck{base: b, host: host, ifaces: parseInterfaces(entry[CheckKeyInterface]), ifaceAll: all, port: port}, ""
}

// buildPortsCheck builds a multi-port open/closed check.
func buildPortsCheck(b base, entry map[string]any) (Check, string) {
	host := cfgval.AsString(entry[CheckKeyHost])
	if host == "" {
		host = conn.DefaultHost
	}
	ports, err := ParsePortSpec(cfgval.AsString(entry[CheckKeyPorts]))
	if err != nil {
		return nil, "ports check: " + err.Error()
	}
	expect := cfgval.AsString(entry[CheckKeyExpect])
	if expect == "" {
		expect = PortStateOpen
	}
	if expect != PortStateOpen && expect != PortStateClosed && expect != PortExpectAny {
		return nil, "ports check: expect must be " + PortExpectSummary
	}
	match := cfgval.AsString(entry[CheckKeyMatch])
	if match == "" {
		match = PortMatchAll
	}
	if match != PortMatchAll && match != PortMatchAny && match != PortMatchNone {
		return nil, "ports check: match must be " + PortMatchSummary
	}
	connectTimeout := time.Duration(0)
	if raw, present := entry[CheckKeyConnectTimeout]; present {
		connectTimeout = cfgval.Duration(raw)
		if connectTimeout <= 0 {
			return nil, "ports check: connect_timeout must be a valid positive duration"
		}
	}
	allIf, iwarn := parseInterfaceMatch(entry)
	if iwarn != "" {
		return nil, "ports check: " + iwarn
	}
	return &portsCheck{
		base:           b,
		host:           host,
		ifaces:         parseInterfaces(entry[CheckKeyInterface]),
		ifaceAll:       allIf,
		ports:          ports,
		expect:         expect,
		match:          match,
		onChange:       cfgval.Bool(entry[CheckKeyOnChange]),
		connectTimeout: connectTimeout,
	}, ""
}

// buildHTTPCheck builds an http(s) check, configuring proxy, http3 and interface
// egress on a per-check client when requested.
func buildHTTPCheck(b base, entry map[string]any, client *http.Client) (Check, string) {
	rawURL := cfgval.AsString(entry[CheckKeyURL])
	if rawURL == "" {
		return nil, "http check requires a url"
	}
	method, warn := ParseHTTPMethod(entry[CheckKeyMethod])
	if warn != "" {
		return nil, "http check: " + warn
	}
	expect, err := parseStatusMatcher(entry[CheckKeyExpectStatus])
	if err != nil {
		return nil, "http check: " + err.Error()
	}
	body, contentType, warn := httpRequestBody(entry)
	if warn != "" {
		return nil, warn
	}
	reqClient, warn := httpRequestClient(rawURL, entry, client)
	if warn != "" {
		return nil, warn
	}
	expectJSON, warn := parseAssertionMap(entry[CheckKeyExpectJSON], CheckKeyExpectJSON)
	if warn != "" {
		return nil, "http check: " + warn
	}
	hc := &httpCheck{
		base:        b,
		client:      httpClientWithRedirectPolicy(reqClient, boolWithDefault(entry[CheckKeyFollowRedirects], true)),
		url:         rawURL,
		method:      method,
		headers:     cfgval.StringMap(entry[CheckKeyHeaders]),
		body:        body,
		contentType: contentType,
		expect:      expect,
		expectJSON:  expectJSON,
	}
	if warn := configureHTTPBodyAssertion(hc, entry); warn != "" {
		return nil, warn
	}
	if warn := configureHTTPLatency(hc, entry); warn != "" {
		return nil, warn
	}
	if warn := configureHTTPCert(hc, entry, rawURL); warn != "" {
		return nil, warn
	}
	if hc.certClient != nil {
		hc.certClient = httpClientWithRedirectPolicy(hc.certClient, boolWithDefault(entry[CheckKeyFollowRedirects], true))
	}
	return hc, ""
}

func httpRequestBody(entry map[string]any) ([]byte, string, string) {
	jsonBody, hasJSON := entry[CheckKeyJSON]
	if hasJSON && jsonBody != nil {
		if _, hasBody := entry[CheckKeyBody]; hasBody {
			return nil, "", "http check: body and json are mutually exclusive"
		}
		raw, err := json.Marshal(jsonBody)
		if err != nil {
			return nil, "", "http check: invalid json body: " + err.Error()
		}
		return raw, httpContentTypeJSON, ""
	}
	if body := cfgval.AsString(entry[CheckKeyBody]); body != "" {
		return []byte(body), "", ""
	}
	return nil, "", ""
}

// httpRequestClient configures the per-check transport. HTTP/3 always uses
// QUIC, while proxy and interface routing use an HTTP transport dialer.
func httpRequestClient(rawURL string, entry map[string]any, client *http.Client) (*http.Client, string) {
	proxyURL, warn := parseProxyURL(entry)
	if warn != "" {
		return nil, warn
	}
	http3Enabled := cfgval.Bool(entry[CheckKeyHTTP3])
	if http3Enabled {
		if u, err := url.Parse(rawURL); err != nil || u.Scheme != URLSchemeHTTPS {
			return nil, "http check: http3 requires an https url"
		}
		if proxyURL != nil {
			return nil, "http check: http3 and proxy are mutually exclusive"
		}
		client = &http.Client{Transport: &http3.Transport{}}
	} else if proxyURL != nil {
		client = httpClientWithTransport(proxyURL, "")
	}
	// interface: egress the HTTP request (and any proxy connection) through a
	// specific interface by binding the transport's dialer. The http client has
	// one fixed transport, so it honors a single interface (the first listed).
	if ifaces := parseInterfaces(entry[CheckKeyInterface]); len(ifaces) > 0 {
		if http3Enabled {
			return nil, "http check: http3 and interface are mutually exclusive"
		}
		return httpClientWithTransport(proxyURL, ifaces[0]), ""
	}
	return client, ""
}

func httpClientWithTransport(proxyURL *url.URL, iface string) *http.Client {
	tr := httpx.CloneDefaultTransport()
	if proxyURL != nil {
		tr.Proxy = http.ProxyURL(proxyURL)
	}
	if iface != "" {
		tr.DialContext = conn.BindDialer(iface).DialContext
	}
	return &http.Client{Transport: tr}
}

func configureHTTPBodyAssertion(check *httpCheck, entry map[string]any) string {
	// expect_body is an {op, value} operator comparison against the trimmed body.
	bodyMatch, present := entry[CheckKeyExpectBody]
	if !present {
		return ""
	}
	fields, ok := bodyMatch.(map[string]any)
	if !ok {
		return "http expect_body must be an {op, value} mapping"
	}
	op := cfgval.AsString(fields[CheckKeyOp])
	if !validCompareOp(op) {
		return "http expect_body op must be one of " + cfgval.AssertOpSummary
	}
	value := cfgval.String(fields[CheckKeyValue])
	if err := ValidateAssertionValue(CheckKeyExpectBody, op, value); err != nil {
		return "http " + err.Error()
	}
	check.bodyOp, check.bodyValue = op, value
	return ""
}

func configureHTTPLatency(check *httpCheck, entry map[string]any) string {
	op, value, warn := parseExpectLatency(entry)
	if warn != "" {
		return "http " + warn
	}
	check.latencyOp, check.latencyValue = op, value
	return ""
}

func boolWithDefault(v any, def bool) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

func httpClientWithRedirectPolicy(client *http.Client, follow bool) *http.Client {
	if follow {
		return client
	}
	if client == nil {
		client = &http.Client{}
	}
	copied := *client
	copied.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &copied
}

// HTTPMethodList is the user-facing list of standard HTTP methods accepted by
// HTTP checks.
const HTTPMethodList = "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS, TRACE, CONNECT"

var standardHTTPMethods = map[string]struct{}{
	http.MethodGet:     {},
	http.MethodHead:    {},
	http.MethodPost:    {},
	http.MethodPut:     {},
	http.MethodPatch:   {},
	http.MethodDelete:  {},
	http.MethodOptions: {},
	http.MethodTrace:   {},
	http.MethodConnect: {},
}

// ParseHTTPMethod returns the normalized standard HTTP method for a check
// config value.
func ParseHTTPMethod(raw any) (string, string) {
	if raw == nil {
		return http.MethodGet, ""
	}
	s, ok := raw.(string)
	if !ok {
		return "", "method must be a string"
	}
	method := strings.ToUpper(strings.TrimSpace(s))
	if _, known := standardHTTPMethods[method]; !known {
		return "", fmt.Sprintf("method %q is not a standard HTTP method (%s)", s, HTTPMethodList)
	}
	return method, ""
}

// buildCommandCheck builds a check that runs a command and asserts its exit code.
func buildCommandCheck(b base, entry map[string]any, runner execx.Runner) (Check, string) {
	argv := cfgval.StringArray(entry[CheckKeyCommand])
	if len(argv) == 0 {
		return nil, "command check requires a non-empty command array"
	}
	expect := []int{CommandDefaultExpectedExit}
	if v, ok := cfgval.IntList(entry[CheckKeyExpectExit]); ok {
		expect = v
	}
	stdout, warn := ParseOutputMatcher(entry[CheckKeyExpectStdout])
	if warn != "" {
		return nil, "command check expect_stdout " + warn
	}
	stderr, warn := ParseOutputMatcher(entry[CheckKeyExpectStderr])
	if warn != "" {
		return nil, "command check expect_stderr " + warn
	}
	version, warn := ParseVersionMatcher(entry[CheckKeyVersionMatch])
	if warn != "" {
		return nil, "command check version_match " + warn
	}
	analyzer, warn := parseAnalyzer(entry[CheckKeyAnalyze])
	if warn != "" {
		return nil, "command check " + warn
	}
	exports, warn := parseCommandExports(b.name, entry[CheckKeyExport])
	if warn != "" {
		return nil, "command check " + warn
	}
	c := commandCheck{base: b, runner: runner, argv: argv, user: cfgval.String(entry[CheckKeyUser]), expectExit: expect, stdout: stdout, stderr: stderr, version: version, exports: exports, analyzer: analyzer}
	if c.onChange = cfgval.Bool(entry[CheckKeyOnChange]); c.onChange {
		c.changeLevel, _ = cfgval.Int(entry[CheckKeyChangeLevel])
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
	case DataKeyVersion:
		exports[DataKeyVersion] = defaultCommandExport(DataKeyVersion)
		short := defaultCommandExport(DataKeyVersionShort)
		short.shortVersion = true
		exports[DataKeyVersionShort] = short
	case DataKeyVersionShort:
		exports[checkName] = defaultCommandExport(checkName)
	}
	if raw == nil {
		return sortedCommandExports(exports), ""
	}
	specs, ok := raw.(map[string]any)
	if !ok {
		return nil, CheckKeyExport + " must be a mapping of variable name -> export rule"
	}
	for _, name := range slices.Sorted(maps.Keys(specs)) {
		spec, ok := specs[name].(map[string]any)
		if !ok {
			return nil, commandExportPath(name) + " must be a mapping"
		}
		e := defaultCommandExport(name)
		if from := cfgval.String(spec[CheckKeyFrom]); from != "" {
			e.from = from
		}
		switch e.from {
		case AnalyzeStreamStdout, AnalyzeStreamStderr:
		default:
			return nil, commandExportPath(name, CheckKeyFrom) + " must be " + AnalyzeExportStreamSummary
		}
		if rawTrim, present := spec[CheckKeyTrim]; present {
			v, ok := rawTrim.(bool)
			if !ok {
				return nil, commandExportPath(name, CheckKeyTrim) + " must be a boolean"
			}
			e.trim = v
		}
		if rawDefault, present := spec[CheckKeyDefault]; present {
			e.defaultValue = cfgval.String(rawDefault)
		}
		if rawRegex, present := spec[CheckKeyRegex]; present {
			pattern := cfgval.String(rawRegex)
			if pattern == "" {
				return nil, commandExportPath(name, CheckKeyRegex) + " must be non-empty"
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, commandExportPath(name, CheckKeyRegex) + " is invalid: " + err.Error()
			}
			e.regex = re
		}
		exports[name] = e
	}
	return sortedCommandExports(exports), ""
}

func commandExportPath(name string, fields ...string) string {
	parts := make([]string, 0, len(fields)+2)
	parts = append(parts, CheckKeyExport, name)
	parts = append(parts, fields...)
	return strings.Join(parts, ".")
}

func defaultCommandExport(name string) commandExport {
	return commandExport{name: name, from: AnalyzeStreamStdout, trim: true}
}

var commandShortVersionRE = regexp.MustCompile(`[0-9]+\.[0-9]+(?:\.[0-9]+)?`)
var commandShortIntegerVersionRE = regexp.MustCompile(`(?i)\b(?:version|v)\s*:?\s*([0-9]+)\b`)

const (
	commandRegexFullMatchGroup     = 0
	commandRegexFirstCaptureGroup  = 1
	commandRegexMinCapturedMatches = 2
)

func commandShortVersion(s string) string {
	if dotted := commandShortVersionRE.FindString(s); dotted != "" {
		return dotted
	}
	if match := commandShortIntegerVersionRE.FindStringSubmatch(s); len(match) >= commandRegexMinCapturedMatches {
		return match[commandRegexFirstCaptureGroup]
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
	if e.from == AnalyzeStreamStderr {
		source = stderr
	}
	value := source
	if e.regex != nil {
		match := e.regex.FindStringSubmatch(source)
		if match == nil {
			value = e.defaultValue
		} else if len(match) >= commandRegexMinCapturedMatches {
			value = match[commandRegexFirstCaptureGroup]
		} else {
			value = match[commandRegexFullMatchGroup]
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
	expect := cfgval.AsString(entry[CheckKeyExpect])
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
	path := cfgval.AsString(entry[CheckKeyPath])
	if path == "" {
		return nil, "file_exists check requires a path"
	}
	return fileExistsCheck{base: b, path: path}, ""
}

// buildFileCheck builds a check that a path exists and is a regular file.
func buildFileCheck(b base, entry map[string]any) (Check, string) {
	path := cfgval.AsString(entry[CheckKeyPath])
	if path == "" {
		return nil, "file check requires a path"
	}
	return fileCheck{base: b, path: path, nonEmpty: cfgval.Bool(entry[CheckKeyNonEmpty])}, ""
}

// buildLockfileCheck builds a check that one service-owned lockfile candidate
// exists and is a regular file.
func buildLockfileCheck(b base, entry map[string]any) (Check, string) {
	paths := cfgval.StringList(entry[CheckKeyPath])
	if len(paths) == 0 {
		return nil, "lockfile check requires a path"
	}
	return lockfileCheck{base: b, paths: paths}, ""
}

// buildPidfileCheck builds a check that a pidfile exists and references a running
// process. Gate it with `requires: [service]` so it only errors while the service
// is active.
func buildPidfileCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	paths := cfgval.StringList(entry[CheckKeyPath])
	if len(paths) == 0 {
		return nil, "pidfile check requires a path"
	}
	return pidfileCheck{base: b, paths: paths, fallbackPIDs: deps.PidfileFallbackPIDs}, ""
}

// buildSocketCheck builds a check that one Unix socket candidate exists.
func buildSocketCheck(b base, entry map[string]any) (Check, string) {
	paths := cfgval.StringList(entry[CheckKeyPath])
	if len(paths) == 0 {
		return nil, "socket check requires a path"
	}
	return socketCheck{base: b, paths: paths}, ""
}

// buildBinaryCheck builds a check on a binary's fingerprint.
func buildBinaryCheck(b base, entry map[string]any) (Check, string) {
	path := cfgval.AsString(entry[CheckKeyPath])
	if path == "" {
		return nil, "binary check requires a path"
	}
	return binaryCheck{base: b, path: path}, ""
}

// buildLibrariesCheck builds a check on a binary's shared-library dependencies.
// Implemented natively with debug/elf (no ldd).
func buildLibrariesCheck(b base, entry map[string]any) (Check, string) {
	binary := cfgval.AsString(entry[CheckKeyBinary])
	if binary == "" {
		return nil, "libraries check requires a binary"
	}
	return librariesCheck{base: b, binary: binary}, ""
}

// buildMetricCheck builds a check comparing a sampled metric to a threshold.
// Metric-check scopes (the `scope:` selector of a metric check). Exported so
// config validation checks the same scope vocabulary the builder accepts.
const (
	MetricScopeService = "service"
	MetricScopeSystem  = "system"
)

func buildMetricCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	name := cfgval.AsString(entry[CheckKeyName])
	if name == "" {
		return nil, "metric check requires a name"
	}
	scope := cfgval.AsString(entry[CheckKeyScope])
	if scope == "" {
		scope = MetricScopeService
	}
	op := cfgval.AsString(entry[CheckKeyOp])
	if op == "" {
		return nil, "metric check requires an op"
	}
	if deps.Metrics == nil {
		return nil, "metric check needs a metric source, unavailable here"
	}
	return metricCheck{base: b, scope: scope, metric: name, op: op, value: cfgval.String(entry[CheckKeyValue]), source: deps.Metrics}, ""
}

// buildProcessCheck builds a check on processes matching an exe/user selector.
func buildProcessCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	exe := cfgval.AsString(entry[CheckKeyExe])
	exes := cfgval.StringList(entry[CheckKeyExeAny])
	if exe != "" {
		exes = []string{exe}
	}
	user := cfgval.AsString(entry[CheckKeyUser])
	if len(exes) == 0 {
		return nil, "process check requires exe or exe_any"
	}
	if deps.Processes == nil && deps.ProcessesAny == nil {
		return nil, "process check needs process discovery, unavailable here"
	}
	expect := cfgval.AsString(entry[CheckKeyState])
	if expect == "" {
		expect = process.StateRunning
	}
	return processCheck{base: b, exes: exes, user: user, expect: expect, observe: deps.Processes, observeAny: deps.ProcessesAny}, ""
}

// buildCountCheck builds a check on the number of entries under a path.
func buildCountCheck(b base, entry map[string]any) (Check, string) {
	path := cfgval.AsString(entry[CheckKeyPath])
	if path == "" {
		return nil, "count check requires a path"
	}
	kind := cfgval.AsString(entry[CheckKeyOf])
	if kind == "" {
		kind = CountKindAny
	}
	if !validCountKind(kind) {
		return nil, "count check `of` must be " + CountKindSummary
	}
	if _, hasDelta := entry[CheckKeyDelta]; hasDelta {
		if _, hasCount := entry[CheckKeyCount]; hasCount {
			return nil, "count check must not mix a count threshold with delta"
		}
		_, hasOp := entry[CheckKeyOp]
		_, hasValue := entry[CheckKeyValue]
		if hasOp || hasValue {
			return nil, "count check must not mix top-level op/value with delta"
		}
		op, val, errs := parseDeltaThreshold(entry[CheckKeyDelta], "count check")
		if errs != "" {
			return nil, errs
		}
		window := cfgval.DurationOr(entry[CheckKeyWithin], 0)
		if window <= 0 {
			return nil, "count check delta requires a positive within (e.g. 2m)"
		}
		return countCheck{
			base:          b,
			path:          path,
			kind:          kind,
			recursive:     cfgval.Bool(entry[CheckKeyRecursive]),
			includeHidden: cfgval.Bool(entry[CheckKeyIncludeHidden]),
			deltaOp:       op,
			deltaValue:    val,
			window:        window,
			clock:         time.Now,
			state:         &countState{},
		}, ""
	}
	if cfgval.String(entry[CheckKeyWithin]) != "" {
		return nil, "count check within requires delta {op, value}"
	}
	// The threshold may sit at the top level (op/value) or be nested under
	// `count: {op, value}` like every other named predicate.
	threshold := entry
	if m, ok := entry[CheckKeyCount].(map[string]any); ok {
		threshold = m
	}
	op := cfgval.AsString(threshold[CheckKeyOp])
	if !cfgval.IsCompareOp(op) {
		return nil, "count check requires a valid op (>=, >, <=, <, ==, !=)"
	}
	val, err := strconv.ParseFloat(cfgval.String(threshold[CheckKeyValue]), numericBits64)
	if err != nil {
		return nil, "count check value must be numeric"
	}
	return countCheck{base: b, path: path, kind: kind, recursive: cfgval.Bool(entry[CheckKeyRecursive]), includeHidden: cfgval.Bool(entry[CheckKeyIncludeHidden]), op: op, value: val}, ""
}

// buildStorageCheck builds a storage space/inode and/or mount check.
func buildStorageCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	path := cfgval.AsString(entry[CheckKeyPath])
	if path == "" {
		return nil, "storage check requires a path"
	}
	preds, err := parseLevelPreds(entry, StoragePredFields)
	if err != nil {
		return nil, "storage check: " + err.Error()
	}
	mount := parseMountCond(entry)
	if len(preds) == 0 && !mount.active {
		return nil, "storage check requires a space/inode predicate (used_pct/free_pct/used_bytes/free_bytes/inodes_*) and/or a mount condition (mounted)"
	}
	return storageCheck{base: b, path: path, preds: preds, usage: deps.StorageUsage, mount: mount, mountSampler: deps.MountSampler}, ""
}

// buildAutofsCheck builds an autofs automounter check.
func buildAutofsCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	path := cfgval.AsString(entry[CheckKeyPath])
	op, value := "", 0.0
	if m, ok := entry[CheckKeyCount].(map[string]any); ok {
		op = cfgval.AsString(m[CheckKeyOp])
		if !cfgval.IsCompareOp(op) {
			return nil, "autofs check count has an invalid op (>=, >, <=, <, ==, !=)"
		}
		v, err := strconv.ParseFloat(cfgval.String(m[CheckKeyValue]), numericBits64)
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
	iface := cfgval.AsString(entry[CheckKeyInterface])
	if iface == "" {
		return nil, "net check requires an interface"
	}
	metric := cfgval.AsString(entry[CheckKeyMetric])
	c := &netCheck{base: b, iface: iface, metric: metric, sampler: deps.NetSampler}
	switch metric {
	case NetMetricState:
		expect := cfgval.AsString(entry[CheckKeyExpect])
		onChange := cfgval.AsString(entry[CheckKeyOn]) == OnModeChange
		if expect == "" && !onChange {
			return nil, "net state requires expect: up|down or on: change"
		}
		if expect != "" {
			if expect != NetStateUp && expect != NetStateDown {
				return nil, "net state expect must be " + NetStateSummary
			}
			c.expect = expect
		} else if onChange {
			c.onChange = true
		}
	case NetMetricSpeed:
		if cfgval.AsString(entry[CheckKeyOn]) != OnModeChange {
			return nil, "net speed requires on: change"
		}
		c.onChange = true
	case NetMetricErrors:
		c.counters = cfgval.StringArray(entry[CheckKeyCounters])
		if len(c.counters) == 0 {
			c.counters = []string{NetCounterRXErrors, NetCounterTXErrors}
		}
		op, v, errs := parseDeltaThreshold(entry[CheckKeyDelta], "net errors")
		if errs != "" {
			return nil, errs
		}
		c.op, c.value = op, v
	case NetMetricAddress:
		expect := cfgval.AsString(entry[CheckKeyExpect])
		onChange := cfgval.AsString(entry[CheckKeyOn]) == OnModeChange
		if expect == "" && !onChange {
			return nil, "net address requires expect: present|absent or on: change"
		}
		if expect != "" {
			if expect != NetAddrPresent && expect != NetAddrAbsent {
				return nil, "net address expect must be " + NetAddrSummary
			}
			c.expect = expect
		} else if onChange {
			c.onChange = true
		}
	default:
		return nil, "net check metric must be " + NetMetricSummary
	}
	return c, ""
}

// buildLoadCheck builds a system load-average check.
func buildLoadCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, LoadPredFields, "load check")
	if errs != "" {
		return nil, errs
	}
	return loadCheck{base: b, preds: preds, perCPU: cfgval.Bool(entry[CheckKeyPerCPU]), sampler: deps.LoadSampler}, ""
}

// buildUsersCheck builds a logged-in-user count check.
func buildUsersCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, UsersPredFields, "users check")
	if errs != "" {
		return nil, errs
	}
	return usersCheck{base: b, preds: preds, sampler: deps.UsersSampler}, ""
}

// buildProcessCountCheck builds a check on the number of processes matching an
// optional user/exe/exe_dir filter.
func buildProcessCountCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, ProcessCountPredFields, "process_count check")
	if errs != "" {
		return nil, errs
	}
	return processCountCheck{
		base:   b,
		preds:  preds,
		user:   cfgval.AsString(entry[CheckKeyUser]),
		exe:    cfgval.AsString(entry[CheckKeyExe]),
		exeDir: cfgval.AsString(entry[CheckKeyExeDir]),
		count:  deps.ProcessCount,
	}, ""
}

// buildHdparmCheck builds a disk-throughput check (hdparm -t/-T).
func buildHdparmCheck(b base, entry map[string]any, runner execx.Runner) (Check, string) {
	device := cfgval.AsString(entry[CheckKeyDevice])
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
	return sensorsCheck{base: b, chip: cfgval.AsString(entry[CheckKeyChip]), label: cfgval.AsString(entry[CheckKeyLabel]), preds: preds, sampler: deps.SensorSampler}, ""
}

// buildSmartCheck builds a drive SMART-health check (smartctl).
func buildSmartCheck(b base, entry map[string]any, runner execx.Runner) (Check, string) {
	device := cfgval.AsString(entry[CheckKeyDevice])
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
	return &raidCheck{
		base:         b,
		preds:        preds,
		sampler:      deps.RaidSampler,
		array:        cfgval.String(entry[CheckKeyArray]),
		sysfsChanges: cfgval.Bool(entry[CheckKeySysfsChanges]),
	}, ""
}

func buildLVMCheck(b base, entry map[string]any, runner execx.Runner) (Check, string) {
	preds, err := parseLevelPreds(entry, LVMPredFields)
	if err != nil {
		return nil, "lvm check: " + err.Error()
	}
	vg := cfgval.String(entry[CheckKeyVolumeGroup])
	lv := cfgval.String(entry[CheckKeyLogicalVolume])
	if lv != "" && vg == "" {
		return nil, "lvm check logical_volume requires volume_group"
	}
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	return &lvmCheck{base: b, runner: runner, volumeGroup: vg, logicalVolume: lv, preds: preds}, ""
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
	device := cfgval.AsString(entry[CheckKeyDevice])
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
	resource := cfgval.AsString(entry[CheckKeyResource])
	switch resource {
	case PressureResourceCPU, PressureResourceMemory, PressureResourceIO:
	default:
		return nil, "pressure check requires resource: " + PressureResourceSummary
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
	op, value := cfgval.CompareOpGreater, 0.0
	if raw, present := entry[CheckKeyDelta]; present {
		var errs string
		if op, value, errs = parseDeltaThreshold(raw, "oom"); errs != "" {
			return nil, errs
		}
	}
	return &oomCheck{base: b, op: op, value: value, sampler: deps.OomSampler}, ""
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
	verify := true
	if v, ok := entry[CheckKeyCertVerify].(bool); ok {
		verify = v
	}
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
	path := cfgval.AsString(entry[CheckKeyPath])
	if path == "" {
		return nil, "sqlite check requires a path"
	}
	return sqliteCheck{base: b, path: path, quick: cfgval.Bool(entry[CheckKeyQuick])}, ""
}

// buildSwapCheck builds a swap usage or io check.
func buildSwapCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	metric := cfgval.AsString(entry[CheckKeyMetric])
	c := &swapCheck{base: b, metric: metric, sampler: deps.SwapSampler}
	switch metric {
	case SwapMetricUsage:
		preds, errs := requireLevelPreds(entry, SwapUsageFields, "swap usage")
		if errs != "" {
			return nil, errs
		}
		c.preds = preds
	case SwapMetricIO:
		op, v, errs := parseDeltaThreshold(entry[CheckKeyDelta], "swap io")
		if errs != "" {
			return nil, errs
		}
		c.op, c.value = op, v
	default:
		return nil, "swap check metric must be " + SwapMetricSummary
	}
	return c, ""
}

// buildICMPCheck builds an ICMP ping state/latency check.
func buildICMPCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	host := cfgval.AsString(entry[CheckKeyHost])
	if host == "" {
		return nil, "icmp check requires a host"
	}
	count := DefaultPingCount
	if v, ok := cfgval.Int(entry[CheckKeyCount]); ok {
		if v <= 0 {
			return nil, "icmp count must be a positive integer"
		}
		count = v
	}
	metric := cfgval.AsString(entry[CheckKeyMetric])
	allIf, iwarn := parseInterfaceMatch(entry)
	if iwarn != "" {
		return nil, "icmp check: " + iwarn
	}
	c := &icmpCheck{base: b, host: host, ifaces: parseInterfaces(entry[CheckKeyInterface]), ifaceAll: allIf, count: count, metric: metric, sampler: deps.PingSampler}
	if warn := configureICMPMetric(c, entry); warn != "" {
		return nil, warn
	}
	return c, ""
}

func configureICMPMetric(check *icmpCheck, entry map[string]any) string {
	switch check.metric {
	case NetMetricState:
		return configureICMPState(check, entry)
	case IcmpMetricLatency:
		return configureICMPLatency(check, entry)
	default:
		return "icmp check metric must be " + ICMPMetricSummary
	}
}

func configureICMPState(check *icmpCheck, entry map[string]any) string {
	expect := cfgval.AsString(entry[CheckKeyExpect])
	onChange := cfgval.AsString(entry[CheckKeyOn]) == OnModeChange
	if expect == "" && !onChange {
		return "icmp state requires expect: up|down or on: change"
	}
	if expect != "" {
		if expect != NetStateUp && expect != NetStateDown {
			return "icmp state expect must be " + NetStateSummary
		}
		check.expect = expect
		return ""
	}
	check.onChange = true
	return ""
}

func configureICMPLatency(check *icmpCheck, entry map[string]any) string {
	threshold, hasThreshold := entry[CheckKeyThreshold].(map[string]any)
	change, hasChange := entry[CheckKeyChange].(map[string]any)
	if !hasThreshold && !hasChange {
		return "icmp latency requires threshold {op, value} or change {delta}"
	}
	if hasThreshold {
		op := cfgval.AsString(threshold[CheckKeyOp])
		if !cfgval.IsCompareOp(op) {
			return "icmp latency threshold has an invalid op"
		}
		value, err := strconv.ParseFloat(cfgval.String(threshold[CheckKeyValue]), numericBits64)
		if err != nil {
			return "icmp latency threshold value must be numeric"
		}
		check.hasThreshold, check.op, check.value = true, op, value
		return ""
	}
	delta, err := strconv.ParseFloat(cfgval.String(change[CheckKeyDelta]), numericBits64)
	if err != nil {
		return "icmp latency change delta must be numeric"
	}
	check.hasChange, check.delta = true, delta
	return ""
}

// buildRouteCheck builds a default-route presence check.
func buildRouteCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	family := cfgval.AsString(entry[CheckKeyFamily])
	switch family {
	case "":
		family = FamilyIPv4
	case FamilyIPv4, FamilyIPv6:
	default:
		return nil, "route family must be " + RouteFamilySummary
	}
	return routeCheck{base: b, family: family, iface: cfgval.AsString(entry[CheckKeyInterface]), sampler: deps.RouteSampler}, ""
}

// buildSizeCheck builds a path-growth check over a time window.
func buildSizeCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	path := cfgval.AsString(entry[CheckKeyPath])
	if path == "" {
		return nil, "size check requires a path"
	}
	// parseSize already rejects zero, negative and unitless values, so a nil
	// error guarantees growBy > 0 — no redundant positivity guard needed.
	growBy, err := parseSize(cfgval.String(entry[CheckKeyGrowBy]))
	if err != nil {
		return nil, "size check requires a positive grow_by with a K/M/G/T suffix (e.g. 1G)"
	}
	window := cfgval.DurationOr(entry[CheckKeyWithin], 0)
	if window <= 0 {
		return nil, "size check requires a positive within (e.g. 1h)"
	}
	return &sizeCheck{base: b, path: path, growBy: growBy, window: window, includeHidden: cfgval.Bool(entry[CheckKeyIncludeHidden]), sampler: deps.SizeSampler, clock: time.Now, state: &sizeState{}}, ""
}

// HTTPProxySchemeList is the user-facing list of accepted HTTP check proxy
// schemes.
const HTTPProxySchemeList = URLSchemeHTTP + ", " + URLSchemeHTTPS + ", " + URLSchemeSOCKS5 + " or " + URLSchemeSOCKS5H

// IsHTTPProxyScheme reports whether scheme is accepted for an HTTP check proxy.
func IsHTTPProxyScheme(scheme string) bool {
	switch scheme {
	case URLSchemeHTTP, URLSchemeHTTPS, URLSchemeSOCKS5, URLSchemeSOCKS5H:
		return true
	default:
		return false
	}
}

// parseProxyURL reads the optional `proxy` field of an http check (e.g. a Squid
// proxy, "http://[user:pass@]squid:3128"). It returns the parsed URL, or a
// warning when the value is malformed. A nil URL with no warning means no proxy.
func parseProxyURL(entry map[string]any) (*url.URL, string) {
	s := cfgval.AsString(entry[CheckKeyProxy])
	if s == "" {
		return nil, ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil, "http check: invalid proxy url " + strconv.Quote(s)
	}
	if IsHTTPProxyScheme(u.Scheme) {
		return u, ""
	}
	return nil, "http check: proxy scheme must be " + HTTPProxySchemeList
}

// httpCertKeys are the optional certificate-inspection keys on the http check.
var httpCertKeys = []string{
	CheckKeyCertExpiresInDays,
	CheckKeyCertVerify,
	CheckKeyCertOnChange,
	CheckKeyCertOnIssuerChange,
	CheckKeyCertOnAlgorithmChange,
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
	if u.Scheme != URLSchemeHTTPS {
		return "http check: cert_* options require an https url"
	}
	verify := true
	if v, ok := entry[CheckKeyCertVerify].(bool); ok {
		verify = v
	}
	days := 0
	if v, ok := cfgval.Int(entry[CheckKeyCertExpiresInDays]); ok {
		days = v
	}
	hc.certHost = u.Hostname()
	hc.certOpts = certOptions{
		expiresInDays:  days,
		verify:         verify,
		onAlgoChange:   cfgval.Bool(entry[CheckKeyCertOnAlgorithmChange]),
		onIssuerChange: cfgval.Bool(entry[CheckKeyCertOnIssuerChange]),
		onChange:       cfgval.Bool(entry[CheckKeyCertOnChange]),
	}
	if cfgval.Bool(entry[CheckKeyHTTP3]) {
		// Read the leaf over QUIC too; http3 populates resp.TLS so the same
		// certificate logic applies. TLS 1.3 is enforced by QUIC.
		hc.certClient = &http.Client{Transport: &http3.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}, //nolint:gosec // leaf inspected and verified manually via verifyCertChain
		}}
		return ""
	}
	tr := httpx.CloneDefaultTransport()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // leaf inspected and verified manually via verifyCertChain
	if pu, _ := parseProxyURL(entry); pu != nil {
		tr.Proxy = http.ProxyURL(pu) // cert inspection also goes through the proxy (CONNECT for https)
	}
	hc.certClient = &http.Client{Transport: tr}
	return ""
}

// BuildInline builds a single check from an inline entry (type + fields), used
// by inline rule conditions. It returns an error rather than a
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

// parseAssertionMap reads a field -> value/{op,value} mapping into ordered
// assertions.
func parseAssertionMap(v any, field string) ([]jsonAssertion, string) {
	if v == nil {
		return nil, ""
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, field + " must be a mapping"
	}
	if len(m) == 0 {
		return nil, ""
	}
	out := make([]jsonAssertion, 0, len(m))
	for _, path := range slices.Sorted(maps.Keys(m)) {
		raw := m[path]
		if cond, ok := raw.(map[string]any); ok {
			op := cfgval.AsString(cond[CheckKeyOp])
			if op == "" {
				op = cfgval.CompareOpEqual
			}
			if !validCompareOp(op) {
				return nil, fmt.Sprintf("%s.%s op must be one of %s", field, path, cfgval.AssertOpSummary)
			}
			value := cfgval.String(cond[CheckKeyValue])
			if err := ValidateAssertionValue(field+"."+path, op, value); err != nil {
				return nil, err.Error()
			}
			out = append(out, jsonAssertion{path: path, op: op, value: value})
		} else {
			out = append(out, jsonAssertion{path: path, op: cfgval.CompareOpEqual, value: cfgval.String(raw)})
		}
	}
	return out, ""
}

// parseStatusMatcher parses an expect_status field: a single code, a class
// ("2xx"), or a list of either. Empty defaults to 200.
func parseStatusMatcher(v any) (statusMatcher, error) {
	if v == nil {
		return statusMatcher{codes: []int{defaultHTTPStatusCode}}, nil
	}
	// Operator form: {op, value} (e.g. status < 500).
	if cond, ok := v.(map[string]any); ok {
		op := cfgval.AsString(cond[CheckKeyOp])
		if !validCompareOp(op) {
			return statusMatcher{}, fmt.Errorf("expect_status op must be one of %s", cfgval.AssertOpSummary)
		}
		value := cfgval.String(cond[CheckKeyValue])
		if err := ValidateAssertionValue(CheckKeyExpectStatus, op, value); err != nil {
			return statusMatcher{}, err
		}
		return statusMatcher{op: op, value: value}, nil
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
		if isHTTPStatusClassPattern(s) {
			m.classes = append(m.classes, int(s[httpStatusClassDigitIndex]-httpStatusClassDigitBase))
			continue
		}
		return statusMatcher{}, fmt.Errorf("invalid expect_status %q", s)
	}
	return m, nil
}

func isHTTPStatusClassPattern(s string) bool {
	return len(s) == httpStatusClassPatternLen &&
		(s[httpStatusClassWildcard1] == httpStatusClassWildcard || s[httpStatusClassWildcard1] == httpStatusClassWildcardCaps) &&
		(s[httpStatusClassWildcard2] == httpStatusClassWildcard || s[httpStatusClassWildcard2] == httpStatusClassWildcardCaps) &&
		s[httpStatusClassDigitIndex] >= httpStatusClassMinDigit &&
		s[httpStatusClassDigitIndex] <= httpStatusClassMaxDigit
}
