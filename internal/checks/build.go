package checks

import (
	"context"
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

	positiveDurationMessageSuffix = " must be a valid positive duration"

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
	SSHIdleSampler       SSHIdleSamplerFunc
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
	// StaleBinaries reports this service's processes whose binary was replaced
	// or removed on disk, for `stale_binary` checks. It is read-only: such a
	// process resolves no exe, so it is still never signalled.
	StaleBinaries StaleBinariesFunc
	Samplers
	// CertSampler fetches a TLS endpoint's certificate for `cert` checks. Nil dials
	// the host.
	CertSampler CertSamplerFunc
	// SizeSampler measures the byte size of a file or directory for `size` checks.
	// Nil uses os.Stat (file) / a recursive walk (directory).
	SizeSampler SizeSamplerFunc
}

// BuildIssueKind identifies why an unusable check entry could not be built.
type BuildIssueKind string

// Supported build issue kinds.
const (
	BuildIssueInvalidEntry         BuildIssueKind = "invalid_entry"
	BuildIssueInvalidConfiguration BuildIssueKind = "invalid_configuration"
	BuildIssueUnsupportedType      BuildIssueKind = "unsupported_type"
	BuildIssueBuilderInvariant     BuildIssueKind = "builder_invariant"
)

// BuildIssue is an unusable check entry reported during construction. Required
// issues block the outcome; optional issues remain warnings.
type BuildIssue struct {
	Service  string
	Check    string
	Type     string
	Kind     BuildIssueKind
	Detail   string
	Optional bool
}

// String renders the operator-facing issue with its check identity.
func (i BuildIssue) String() string { return checkBuildMessage(i.Check, i.Detail) }

// BuildError adapts a structured issue to error for inline check callers.
type BuildError struct {
	Issue BuildIssue
}

// Error renders the underlying issue.
func (e *BuildError) Error() string { return e.Issue.String() }

// Result returns an unavailable result for this build issue. Optional malformed
// checks remain optional warnings; required malformed checks block preflight and
// start-verification like any other required check failure.
func (i BuildIssue) Result() Result {
	return Result{
		Service: i.Service, Check: i.Check, OK: false, Optional: i.Optional,
		Unavailable: true, Message: i.String(),
	}
}

// BuildIssueResults converts build issues into check results.
func BuildIssueResults(issues []BuildIssue) []Result {
	return mapBuildIssues(issues, BuildIssue.Result)
}

// mapBuildIssues converts issues element-by-element via conv, preserving the
// nil-in/nil-out convention of the issue views.
func mapBuildIssues[T any](issues []BuildIssue, conv func(BuildIssue) T) []T {
	if len(issues) == 0 {
		return nil
	}
	out := make([]T, 0, len(issues))
	for _, issue := range issues {
		out = append(out, conv(issue))
	}
	return out
}

// Build turns a checks/preflight section (a map keyed by check name)
// into runnable checks, skipping `enabled: false` entries and reporting unusable
// ones as issues. Entries are built in name order for stable output.
func Build(section map[string]any, deps Deps) ([]Built, []string) {
	built, issues := BuildWithIssues(section, deps)
	return built, BuildIssueStrings(issues)
}

// BuildIssueStrings renders build issues as operator-facing strings.
func BuildIssueStrings(issues []BuildIssue) []string {
	return mapBuildIssues(issues, BuildIssue.String)
}

// BuildWithIssues is Build's structured form. Use it where build issues
// must participate in check outcomes, not only be printed.
func BuildWithIssues(section map[string]any, deps Deps) ([]Built, []BuildIssue) {
	if section == nil {
		return nil, nil
	}

	runner, client := buildDependencies(deps)

	var built []Built
	var issues []BuildIssue
	for _, name := range slices.Sorted(maps.Keys(section)) {
		entry, ok := section[name].(map[string]any)
		if !ok {
			issues = append(issues, BuildIssue{
				Service: deps.Service, Check: name, Kind: BuildIssueInvalidEntry,
				Detail: "entry is not a mapping",
			})
			continue
		}
		if cfgval.Disabled(entry) {
			continue
		}

		builtCheck, issue := buildOne(name, entry, deps, runner, client)
		if issue != nil {
			issues = append(issues, *issue)
			continue
		}
		built = append(built, builtCheck)
	}
	return built, issues
}

func buildOne(name string, entry map[string]any, deps Deps, runner execx.Runner, client *http.Client) (Built, *BuildIssue) {
	typ, b, failure := buildCheckBase(name, entry, deps)
	if failure == nil {
		var check Check
		check, failure = buildCheck(typ, b, entry, runner, client, deps)
		if failure == nil {
			return Built{Check: withSummary(check, entry), Optional: cfgval.Bool(entry[CheckKeyOptional])}, nil
		}
	}
	return Built{}, &BuildIssue{
		Service: deps.Service, Check: name, Type: typ, Kind: failure.kind,
		Detail: failure.detail, Optional: cfgval.Bool(entry[CheckKeyOptional]),
	}
}

type checkBuildInput struct {
	base   base
	entry  map[string]any
	runner execx.Runner
	client *http.Client
	deps   Deps
}

type checkBuilder func(checkBuildInput) (Check, string)

type buildFailure struct {
	kind   BuildIssueKind
	detail string
}

// builtinCheckSpecs is the central registry for built-in checks. It keeps
// construction and static type capabilities together. Connection protocols
// remain in conn's own registry because conn owns their catalog and aliases.
var builtinCheckSpecs = []checkSpec{
	{info: healthTypeInfo(CheckTypeTCP), build: func(in checkBuildInput) (Check, string) { return buildTCPCheck(in.base, in.entry) }},
	{info: conditionTypeInfo(CheckTypeTCPConnections), build: func(in checkBuildInput) (Check, string) { return buildTCPConnectionsCheck(in.base, in.entry) }},
	{info: healthTypeInfo(CheckTypePorts), build: func(in checkBuildInput) (Check, string) { return buildPortsCheck(in.base, in.entry) }},
	{info: healthTypeInfo(CheckTypeHTTP), build: func(in checkBuildInput) (Check, string) { return buildHTTPCheck(in.base, in.entry, in.client) }},
	{info: healthTypeInfo(CheckTypeCommand), build: func(in checkBuildInput) (Check, string) { return buildCommandCheck(in.base, in.entry, in.runner) }},
	{info: healthTypeInfo(CheckTypeClock), build: func(in checkBuildInput) (Check, string) { return buildClockCheck(in.base, in.entry) }},
	{info: serviceHealthTypeInfo(CheckTypeService), build: func(in checkBuildInput) (Check, string) { return buildServiceCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeFileExists), build: func(in checkBuildInput) (Check, string) { return buildFileExistsCheck(in.base, in.entry) }},
	{info: healthTypeInfo(CheckTypeFile), build: func(in checkBuildInput) (Check, string) { return buildFileCheck(in.base, in.entry) }},
	{info: healthTypeInfo(CheckTypeLockfile), build: func(in checkBuildInput) (Check, string) { return buildLockfileCheck(in.base, in.entry) }},
	{info: healthTypeInfo(CheckTypeBinary), build: func(in checkBuildInput) (Check, string) { return buildBinaryCheck(in.base, in.entry) }},
	{info: healthTypeInfo(CheckTypePidfile), build: func(in checkBuildInput) (Check, string) { return buildPidfileCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeSocket), build: func(in checkBuildInput) (Check, string) { return buildSocketCheck(in.base, in.entry) }},
	{info: serviceHealthTypeInfo(CheckTypeProcess), build: func(in checkBuildInput) (Check, string) { return buildProcessCheck(in.base, in.entry, in.deps) }},
	// Condition, not health: OK means "nothing is stale", so a rule fires it
	// with `active:` the same way it would an alert-style predicate.
	{info: serviceConditionTypeInfo(CheckTypeStaleBinary), build: func(in checkBuildInput) (Check, string) { return buildStaleBinaryCheck(in.base, in.deps) }},
	{info: serviceConditionTypeInfo(CheckTypeMetric), build: func(in checkBuildInput) (Check, string) { return buildMetricCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeLibraries), build: func(in checkBuildInput) (Check, string) { return buildLibrariesCheck(in.base, in.entry) }},
	{info: conditionTypeInfo(CheckTypeCount), build: func(in checkBuildInput) (Check, string) { return buildCountCheck(in.base, in.entry) }},
	{info: conditionTypeInfo(CheckTypeStorage), build: func(in checkBuildInput) (Check, string) { return buildStorageCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeAutofs), build: func(in checkBuildInput) (Check, string) { return buildAutofsCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeLoad), build: func(in checkBuildInput) (Check, string) { return buildLoadCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeUsers), build: func(in checkBuildInput) (Check, string) { return buildUsersCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeSSHIdle), build: func(in checkBuildInput) (Check, string) { return buildSSHIdleCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeTerminalSessions), build: func(in checkBuildInput) (Check, string) {
		return buildTerminalSessionsCheck(in.base, in.entry, in.runner)
	}},
	{info: conditionTypeInfo(CheckTypeProcessCount), build: func(in checkBuildInput) (Check, string) { return buildProcessCountCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeHdparm), build: func(in checkBuildInput) (Check, string) { return buildHdparmCheck(in.base, in.entry, in.runner) }},
	{info: conditionTypeInfo(CheckTypeSensors), build: func(in checkBuildInput) (Check, string) { return buildSensorsCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeSmart), build: func(in checkBuildInput) (Check, string) { return buildSmartCheck(in.base, in.entry, in.runner) }},
	{info: conditionTypeInfo(CheckTypeRAID), build: func(in checkBuildInput) (Check, string) { return buildRaidCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeLVM), build: func(in checkBuildInput) (Check, string) { return buildLVMCheck(in.base, in.entry, in.runner) }},
	{info: conditionTypeInfo(CheckTypeEDAC), build: func(in checkBuildInput) (Check, string) { return buildEdacCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeConfig), build: func(in checkBuildInput) (Check, string) { return buildConfigCheck(in.base, in.entry, in.runner) }},
	{info: conditionTypeInfo(CheckTypeFDS), build: func(in checkBuildInput) (Check, string) { return buildFdsCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeMemory), build: func(in checkBuildInput) (Check, string) { return buildMemoryCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypePressure), build: func(in checkBuildInput) (Check, string) { return buildPressureCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypePIDs), build: func(in checkBuildInput) (Check, string) { return buildPidsCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeDiskIO), build: func(in checkBuildInput) (Check, string) { return buildDiskIOCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeConntrack), build: func(in checkBuildInput) (Check, string) { return buildConntrackCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeEntropy), build: func(in checkBuildInput) (Check, string) { return buildEntropyCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeZombies), build: func(in checkBuildInput) (Check, string) { return buildZombieCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeOOM), build: func(in checkBuildInput) (Check, string) { return buildOomCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeCert), build: func(in checkBuildInput) (Check, string) { return buildCertCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeSQLite), build: func(in checkBuildInput) (Check, string) { return buildSqliteCheck(in.base, in.entry) }},
	{info: healthTypeInfo(CheckTypeSQLite3), build: func(in checkBuildInput) (Check, string) { return buildSqliteCheck(in.base, in.entry) }},
	{info: conditionTypeInfo(CheckTypeSQL), build: func(in checkBuildInput) (Check, string) { return buildSQLCheck(in.base, in.entry) }},
	{info: conditionTypeInfo(CheckTypeMongoDBQuery), build: func(in checkBuildInput) (Check, string) { return buildMongoCheck(in.base, in.entry) }},
	{info: conditionTypeInfo(CheckTypeInfluxDBQuery), build: func(in checkBuildInput) (Check, string) { return buildInfluxCheck(in.base, in.entry) }},
	{info: conditionTypeInfo(CheckTypeSize), build: func(in checkBuildInput) (Check, string) { return buildSizeCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeWebsocket), build: func(in checkBuildInput) (Check, string) { return buildWebsocketCheck(in.base, in.entry) }},
	{info: conditionTypeInfo(CheckTypeNet), build: func(in checkBuildInput) (Check, string) { return buildNetCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeICMP), build: func(in checkBuildInput) (Check, string) { return buildICMPCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeSwap), build: func(in checkBuildInput) (Check, string) { return buildSwapCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeRoute), build: func(in checkBuildInput) (Check, string) { return buildRouteCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeFirewallRules), build: func(in checkBuildInput) (Check, string) {
		return buildFirewallRulesCheck(in.base, in.entry, in.runner, in.deps)
	}},
}

func buildCheck(typ string, b base, entry map[string]any, runner execx.Runner, client *http.Client, deps Deps) (Check, *buildFailure) {
	if typ == "" {
		return nil, &buildFailure{kind: BuildIssueInvalidConfiguration, detail: "missing type"}
	}
	if spec, ok := checkSpecByName[typ]; ok {
		check, warn := spec.build(checkBuildInput{base: b, entry: entry, runner: runner, client: client, deps: deps})
		switch {
		case warn != "":
			return nil, &buildFailure{kind: BuildIssueInvalidConfiguration, detail: warn}
		case check == nil:
			// A builder must return either a check or a warning. Turning a
			// silent nil into an invariant issue keeps every caller's "no issue
			// means a usable check" assumption true instead of merely intended.
			return nil, &buildFailure{
				kind:   BuildIssueBuilderInvariant,
				detail: fmt.Sprintf("check type %q produced no check", typ),
			}
		default:
			return check, nil
		}
	}
	// A connection-protocol check (mysql, …) is owned by conn's extensible
	// registry, so new protocols need no change in this builder.
	if proto, ok := conn.Lookup(typ); ok {
		check, warn := buildConnCheck(b, proto, entry)
		if warn != "" {
			return nil, &buildFailure{kind: BuildIssueInvalidConfiguration, detail: warn}
		}
		return check, nil
	}
	return nil, &buildFailure{kind: BuildIssueUnsupportedType, detail: fmt.Sprintf("unsupported type %q", typ)}
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
	return buildPathCheck(entry, CheckTypeSQLite, func(path string) Check {
		return sqliteCheck{base: b, path: path, quick: cfgval.Bool(entry[CheckKeyQuick])}
	})
}

// BuildInline builds a single check from an inline entry (type + fields), used
// by inline rule conditions. Its *BuildError carries the same structured issue
// and operator message as section construction.
func BuildInline(name string, entry map[string]any, deps Deps) (Check, error) {
	runner, client := buildDependencies(deps)
	built, issue := buildOne(name, entry, deps, runner, client)
	if issue != nil {
		return nil, &BuildError{Issue: *issue}
	}
	return built.Check, nil
}

func checkBuildMessage(name, detail string) string {
	return fmt.Sprintf("check %q: %s", name, detail)
}

// buildCheckBase prepares the fields shared by regular and inline checks.
func buildCheckBase(name string, entry map[string]any, deps Deps) (string, base, *buildFailure) {
	typ := cfgval.AsString(entry[CheckKeyType])
	timeout := deps.DefaultTimeout
	if raw, present := entry[CheckKeyTimeout]; present {
		timeout = cfgval.Duration(raw)
		if timeout <= 0 {
			return typ, base{}, &buildFailure{
				kind: BuildIssueInvalidConfiguration, detail: positiveDurationMessage(CheckKeyTimeout),
			}
		}
	}
	reports := cfgval.AsString(entry[CheckKeyReports])
	return typ, base{
		name:      name,
		service:   deps.Service,
		timeout:   timeout,
		condition: ResolveCondition(typ, reports),
		reports:   reports,
	}, nil
}

// positiveDurationMessage reports the shared validation text for a duration
// field that was present but could not be parsed as a positive duration.
func positiveDurationMessage(key string) string { return key + positiveDurationMessageSuffix }

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
