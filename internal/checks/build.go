package checks

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
	"sermo/internal/execx"
	"sermo/internal/httpx"
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
	StorageUsage    StorageUsageFunc
	NetSampler      NetSamplerFunc
	PingSampler     PingSamplerFunc
	SwapSampler     SwapSamplerFunc
	RouteSampler    RouteSamplerFunc
	LoadSampler     LoadSamplerFunc
	OomSampler      OomSamplerFunc
	FdsSampler      FdsSamplerFunc
	MemorySampler   MemorySamplerFunc
	PressureSampler PressureSamplerFunc
	PidsSampler     PidsSamplerFunc
	DiskIOSampler   DiskIOSamplerFunc
	// BlockDeviceSizer reports a block device's sysfs capacity for the checks
	// that address one (diskio, hdparm). Nil reads /sys/class/block.
	BlockDeviceSizer BlockDeviceSizeFunc
	// BlockDeviceBus reports the transport a block device sits on, for the checks
	// that address one (diskio, hdparm, smart). Nil reads /sys/class/block.
	BlockDeviceBus BlockDeviceBusFunc
	// BlockDeviceIdentity reports what the kernel says a block device is, for the
	// checks that address one and have to name a drive their own tool can no
	// longer reach. Nil reads /sys/class/block.
	BlockDeviceIdentity  BlockDeviceIdentityFunc
	SensorSampler        SensorSamplerFunc
	RaidSampler          RaidSamplerFunc
	EdacSampler          EdacSamplerFunc
	MountSampler         MountSamplerFunc
	ConntrackSampler     ConntrackSamplerFunc
	FirewallRulesSampler FirewallRulesSamplerFunc
	FailedUnitsSampler   FailedUnitsSamplerFunc
	InotifySampler       InotifySamplerFunc
	ZombieSampler        ZombieSamplerFunc
	UsersSampler         UsersSamplerFunc
	SSHIdleSampler       SSHIdleSamplerFunc
	// CertSampler fetches a TLS endpoint's certificate for `cert` checks. Nil
	// dials the host.
	CertSampler CertSamplerFunc
	// SizeSampler measures the byte size of a file or directory for `size`
	// checks. Nil uses os.Stat (file) / a recursive walk (directory).
	SizeSampler SizeSamplerFunc
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
	// Strays reports this service's control-group members that no selector
	// claims, for `strays` checks. Read-only: reporting a stray authorizes
	// nothing, and only `sermoctl reap --apply` can signal one.
	Strays StraysFunc
	Samplers
}

// BuildIssue is an unusable check entry reported during construction. Required
// issues block the outcome; optional issues remain warnings.
type BuildIssue struct {
	Service  string
	Check    string
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
				Service: deps.Service, Check: name,
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
		Service: deps.Service, Check: name,
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
	detail string
}

// builtinCheckSpecs is the central registry for built-in checks. It keeps
// construction and static type capabilities together. Connection protocols
// remain in conn's own registry because conn owns their catalog and aliases.
var builtinCheckSpecs = []checkSpec{
	{info: latencyTypeInfo(healthTypeInfo(CheckTypeTCP)), build: func(in checkBuildInput) (Check, string) { return buildTCPCheck(in.base, in.entry) }},
	{info: conditionTypeInfo(CheckTypeTCPConnections), predicateFields: TCPConnectionsPredFields, build: func(in checkBuildInput) (Check, string) { return buildTCPConnectionsCheck(in.base, in.entry) }},
	{info: latencyTypeInfo(healthTypeInfo(CheckTypePorts)), build: func(in checkBuildInput) (Check, string) { return buildPortsCheck(in.base, in.entry) }},
	{info: latencyTypeInfo(healthTypeInfo(CheckTypeHTTP)), build: func(in checkBuildInput) (Check, string) { return buildHTTPCheck(in.base, in.entry, in.client) }},
	{info: healthTypeInfo(CheckTypeCommand), build: func(in checkBuildInput) (Check, string) { return buildCommandCheck(in.base, in.entry, in.runner) }},
	{info: healthTypeInfo(CheckTypeClock), build: func(in checkBuildInput) (Check, string) { return buildClockCheck(in.base, in.entry) }},
	{info: latencyTypeInfo(serviceHealthTypeInfo(CheckTypeService)), build: func(in checkBuildInput) (Check, string) { return buildServiceCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeFileExists), build: func(in checkBuildInput) (Check, string) { return buildFileExistsCheck(in.base, in.entry) }},
	{info: resourcePathTypeInfo(healthTypeInfo(CheckTypeFile)), build: func(in checkBuildInput) (Check, string) { return buildFileCheck(in.base, in.entry) }},
	{info: resourcePathTypeInfo(healthTypeInfo(CheckTypeLockfile)), build: func(in checkBuildInput) (Check, string) { return buildLockfileCheck(in.base, in.entry) }},
	{info: resourcePathTypeInfo(healthTypeInfo(CheckTypeBinary)), build: func(in checkBuildInput) (Check, string) { return buildBinaryCheck(in.base, in.entry) }},
	{info: resourcePathTypeInfo(healthTypeInfo(CheckTypePidfile)), build: func(in checkBuildInput) (Check, string) { return buildPidfileCheck(in.base, in.entry, in.deps) }},
	{info: resourcePathTypeInfo(healthTypeInfo(CheckTypeSocket)), build: func(in checkBuildInput) (Check, string) { return buildSocketCheck(in.base, in.entry) }},
	{info: serviceHealthTypeInfo(CheckTypeProcess), build: func(in checkBuildInput) (Check, string) { return buildProcessCheck(in.base, in.entry, in.deps) }},
	// Condition, not health: OK means "nothing is stale", so a rule fires it
	// with `active:` the same way it would an alert-style predicate.
	{info: serviceConditionTypeInfo(CheckTypeStaleBinary), build: func(in checkBuildInput) (Check, string) { return buildStaleBinaryCheck(in.base, in.deps) }},
	// Condition too, and for the same reason: OK means the service's control group
	// holds nothing Sermo cannot account for.
	{info: serviceConditionTypeInfo(CheckTypeStrays), build: func(in checkBuildInput) (Check, string) { return buildStraysCheck(in.base, in.entry, in.deps) }},
	{info: serviceConditionTypeInfo(CheckTypeMetric), build: func(in checkBuildInput) (Check, string) { return buildMetricCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeLibraries), build: func(in checkBuildInput) (Check, string) { return buildLibrariesCheck(in.base, in.entry) }},
	{info: conditionTypeInfo(CheckTypeCount), build: func(in checkBuildInput) (Check, string) { return buildCountCheck(in.base, in.entry) }},
	{info: conditionTypeInfo(CheckTypeStorage), predicateFields: StoragePredFields, build: func(in checkBuildInput) (Check, string) { return buildStorageCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeLoad), predicateFields: LoadPredFields, build: func(in checkBuildInput) (Check, string) { return buildLoadCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeUsers), predicateFields: UsersPredFields, build: func(in checkBuildInput) (Check, string) { return buildUsersCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeSSHIdle), predicateFields: SSHIdlePredFields, build: func(in checkBuildInput) (Check, string) { return buildSSHIdleCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeTerminalSessions), predicateFields: TerminalSessionPredFields, build: func(in checkBuildInput) (Check, string) {
		return buildTerminalSessionsCheck(in.base, in.entry, in.runner)
	}},
	{info: conditionTypeInfo(CheckTypeProcessCount), predicateFields: ProcessCountPredFields, build: func(in checkBuildInput) (Check, string) { return buildProcessCountCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeHdparm), predicateFields: HdparmPredFields, build: func(in checkBuildInput) (Check, string) {
		return buildHdparmCheck(in.base, in.entry, in.runner, in.deps)
	}},
	{info: conditionTypeInfo(CheckTypeSensors), predicateFields: SensorPredFields, build: func(in checkBuildInput) (Check, string) { return buildSensorsCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeSmart), predicateFields: SmartPredFields, build: func(in checkBuildInput) (Check, string) {
		return buildSmartCheck(in.base, in.entry, in.runner, in.deps)
	}},
	{info: conditionTypeInfo(CheckTypeRAID), predicateFields: RaidPredFields, build: func(in checkBuildInput) (Check, string) { return buildRaidCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeStorCLI), predicateFields: HardwareRAIDPredFields, build: func(in checkBuildInput) (Check, string) {
		return buildHardwareRAIDCheck(in.base, in.entry, in.runner, CheckTypeStorCLI)
	}},
	{info: healthTypeInfo(CheckTypeSSACLI), predicateFields: HardwareRAIDPredFields, build: func(in checkBuildInput) (Check, string) {
		return buildHardwareRAIDCheck(in.base, in.entry, in.runner, CheckTypeSSACLI)
	}},
	{info: healthTypeInfo(CheckTypeGlusterCluster), build: func(in checkBuildInput) (Check, string) {
		return buildGlusterClusterCheck(in.base, in.entry, in.runner)
	}},
	{info: healthTypeInfo(CheckTypeLVM), predicateFields: LVMPredFields, build: func(in checkBuildInput) (Check, string) { return buildLVMCheck(in.base, in.entry, in.runner) }},
	{info: conditionTypeInfo(CheckTypeEDAC), predicateFields: EdacPredFields, build: func(in checkBuildInput) (Check, string) { return buildEdacCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeConfig), build: func(in checkBuildInput) (Check, string) { return buildConfigCheck(in.base, in.entry, in.runner) }},
	{info: meteredTypeInfo(conditionTypeInfo(CheckTypeFDS), DataKeyAllocated), predicateFields: FdsPredFields, build: func(in checkBuildInput) (Check, string) { return buildFdsCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeMemory), predicateFields: MemoryPredFields, build: func(in checkBuildInput) (Check, string) { return buildMemoryCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypePressure), predicateFields: PressurePredFields, build: func(in checkBuildInput) (Check, string) { return buildPressureCheck(in.base, in.entry, in.deps) }},
	{info: meteredTypeInfo(conditionTypeInfo(CheckTypePIDs), DataKeyCount), predicateFields: PidsPredFields, build: func(in checkBuildInput) (Check, string) { return buildPidsCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeDiskIO), predicateFields: DiskIOPredFields, build: func(in checkBuildInput) (Check, string) { return buildDiskIOCheck(in.base, in.entry, in.deps) }},
	{info: meteredTypeInfo(conditionTypeInfo(CheckTypeConntrack), DataKeyCount), predicateFields: ConntrackPredFields, build: func(in checkBuildInput) (Check, string) { return buildConntrackCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeZombies), predicateFields: ZombiePredFields, build: func(in checkBuildInput) (Check, string) { return buildZombieCheck(in.base, in.entry, in.deps) }},
	{info: conditionTypeInfo(CheckTypeOOM), predicateFields: OOMPredFields, build: func(in checkBuildInput) (Check, string) { return buildOomCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeCert), build: func(in checkBuildInput) (Check, string) { return buildCertCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeSQLite), build: func(in checkBuildInput) (Check, string) { return buildSqliteCheck(in.base, in.entry) }},
	{info: healthTypeInfo(CheckTypeSQLite3), build: func(in checkBuildInput) (Check, string) { return buildSqliteCheck(in.base, in.entry) }},
	{info: conditionTypeInfo(CheckTypeSQL), build: func(in checkBuildInput) (Check, string) { return buildSQLCheck(in.base, in.entry) }},
	{info: healthTypeInfo(CheckTypeReplication), build: func(in checkBuildInput) (Check, string) { return buildReplicationCheck(in.base, in.entry) }},
	{info: conditionTypeInfo(CheckTypeMongoDBQuery), build: func(in checkBuildInput) (Check, string) { return buildMongoCheck(in.base, in.entry) }},
	{info: conditionTypeInfo(CheckTypeInfluxDBQuery), build: func(in checkBuildInput) (Check, string) { return buildInfluxCheck(in.base, in.entry) }},
	{info: conditionTypeInfo(CheckTypeSize), build: func(in checkBuildInput) (Check, string) { return buildSizeCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeWebsocket), build: func(in checkBuildInput) (Check, string) { return buildWebsocketCheck(in.base, in.entry) }},
	{info: multiMetricTypeInfo(conditionTypeInfo(CheckTypeNet)), build: func(in checkBuildInput) (Check, string) { return buildNetCheck(in.base, in.entry, in.deps) }},
	{info: multiMetricTypeInfo(conditionTypeInfo(CheckTypeICMP)), build: func(in checkBuildInput) (Check, string) { return buildICMPCheck(in.base, in.entry, in.deps) }},
	{info: multiMetricTypeInfo(conditionTypeInfo(CheckTypeSwap)), build: func(in checkBuildInput) (Check, string) { return buildSwapCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeRoute), build: func(in checkBuildInput) (Check, string) { return buildRouteCheck(in.base, in.entry, in.deps) }},
	{info: healthTypeInfo(CheckTypeFirewallRules), build: func(in checkBuildInput) (Check, string) {
		return buildFirewallRulesCheck(in.base, in.entry, in.runner, in.deps)
	}},
	{info: conditionTypeInfo(CheckTypeFailedUnits), predicateFields: FailedUnitsPredFields, build: func(in checkBuildInput) (Check, string) {
		return buildFailedUnitsCheck(in.base, in.entry, in.runner, in.deps)
	}},
	{info: conditionTypeInfo(CheckTypeInotify), predicateFields: InotifyPredFields, build: func(in checkBuildInput) (Check, string) {
		return buildInotifyCheck(in.base, in.entry, in.deps)
	}},
}

func buildCheck(typ string, b base, entry map[string]any, runner execx.Runner, client *http.Client, deps Deps) (Check, *buildFailure) {
	if typ == "" {
		return nil, &buildFailure{detail: "missing type"}
	}
	if spec, ok := checkSpecByName[typ]; ok {
		check, warn := spec.build(checkBuildInput{base: b, entry: entry, runner: runner, client: client, deps: deps})
		return checkBuildResult(typ, check, warn)
	}
	// A connection-protocol check (mysql, …) is owned by conn's extensible
	// registry, so new protocols need no change in this builder.
	if proto, ok := conn.Lookup(typ); ok {
		check, warn := buildConnCheck(b, proto, entry)
		return checkBuildResult(typ, check, warn)
	}
	return nil, &buildFailure{detail: fmt.Sprintf("unsupported type %q", typ)}
}

func checkBuildResult(typ string, check Check, warning string) (Check, *buildFailure) {
	switch {
	case warning != "":
		return nil, &buildFailure{detail: warning}
	case check == nil:
		// Every builder must return either a check or a warning. Keeping this
		// invariant after both built-in and connection-protocol construction
		// makes "no issue" mean a usable check for every caller.
		return nil, &buildFailure{
			detail: fmt.Sprintf("check type %q produced no check", typ),
		}
	default:
		return check, nil
	}
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
				detail: positiveDurationMessage(CheckKeyTimeout),
			}
		}
	}
	reports := cfgval.AsString(entry[CheckKeyReports])
	if _, present := entry[CheckKeyReports]; present && !IsReportingMode(reports) {
		return typ, base{}, &buildFailure{
			detail: fmt.Sprintf("%s %q must be one of %s", CheckKeyReports, reports,
				strings.Join(ReportingModes(), ", ")),
		}
	}
	severity := cfgval.AsString(entry[CheckKeySeverity])
	if _, present := entry[CheckKeySeverity]; present && !IsCheckSeverity(severity) {
		return typ, base{}, &buildFailure{
			detail: fmt.Sprintf("%s %q must be one of %s", CheckKeySeverity, severity,
				strings.Join(CheckSeverities(), ", ")),
		}
	}
	return typ, base{
		name:      name,
		service:   deps.Service,
		timeout:   timeout,
		condition: ResolveCondition(typ, reports),
		reports:   reports,
		severity:  severity,
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
		client = httpx.NewClient(httpx.ClientOptions{})
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
