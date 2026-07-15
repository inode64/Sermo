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
	const commandExportPathPrefixParts = 2

	parts := make([]string, 0, len(fields)+commandExportPathPrefixParts)
	parts = append(parts, CheckKeyExport, name)
	parts = append(parts, fields...)
	return strings.Join(parts, ".")
}

func defaultCommandExport(name string) commandExport {
	return commandExport{name: name, from: AnalyzeStreamStdout, trim: true}
}

var commandShortVersionRE = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)
var commandShortIntegerVersionRE = regexp.MustCompile(`(?i)\b(?:version|v)\s*:?\s*(\d+)\b`)

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
		switch {
		case match == nil:
			value = e.defaultValue
		case len(match) >= commandRegexMinCapturedMatches:
			value = match[commandRegexFirstCaptureGroup]
		default:
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
