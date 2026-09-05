package checks

import (
	"fmt"
	"maps"
	"slices"
)

// TypeInfo describes static capabilities of a built-in check type.
type TypeInfo struct {
	Name           string
	DefaultReports string
	ServiceScoped  bool
	RecordsLatency bool
	// MultiMetric marks a host-scoped type that expands into one watch per
	// metric (net, icmp, swap) and therefore cannot back a service watch.
	MultiMetric bool
	// MeterCountKey names the Result.Data field holding the current count of a
	// count-versus-limit type the dashboard draws as a gauge; empty otherwise.
	MeterCountKey string
	// ResourcePath marks a preflight type whose `path` names a host resource
	// (binary, file, socket, pidfile, lockfile) that variables may select.
	ResourcePath bool
}

func healthTypeInfo(name string) TypeInfo {
	return TypeInfo{Name: name, DefaultReports: ReportsHealth}
}

func conditionTypeInfo(name string) TypeInfo {
	return TypeInfo{Name: name, DefaultReports: ReportsCondition}
}

func serviceHealthTypeInfo(name string) TypeInfo {
	return TypeInfo{Name: name, DefaultReports: ReportsHealth, ServiceScoped: true}
}

func latencyTypeInfo(info TypeInfo) TypeInfo {
	info.RecordsLatency = true
	return info
}

func serviceConditionTypeInfo(name string) TypeInfo {
	return TypeInfo{Name: name, DefaultReports: ReportsCondition, ServiceScoped: true}
}

func multiMetricTypeInfo(info TypeInfo) TypeInfo {
	info.MultiMetric = true
	return info
}

func meteredTypeInfo(info TypeInfo, countKey string) TypeInfo {
	info.MeterCountKey = countKey
	return info
}

func resourcePathTypeInfo(info TypeInfo) TypeInfo {
	info.ResourcePath = true
	return info
}

// checkSpec is the private source of truth for a built-in check's static
// capabilities and constructor. Field validation remains in internal/config so
// it can report configuration paths and error details without an import cycle.
type checkSpec struct {
	info            TypeInfo
	predicateFields []string
	build           checkBuilder
}

// Built-in check type names (the `type:` selector of a check). This is the
// canonical spelling reused by the built-in check registry and config
// validation, so a new type is named once.
const (
	CheckTypeTCP              = "tcp"
	CheckTypePorts            = "ports"
	CheckTypeHTTP             = "http"
	CheckTypeCommand          = "command"
	CheckTypeClock            = "clock"
	CheckTypeService          = "service"
	CheckTypeFileExists       = "file_exists"
	CheckTypeFile             = "file"
	CheckTypeLockfile         = "lockfile"
	CheckTypeBinary           = "binary"
	CheckTypePidfile          = "pidfile"
	CheckTypeSocket           = "socket"
	CheckTypeProcess          = "process"
	CheckTypeStaleBinary      = "stale_binary"
	CheckTypeStrays           = "strays"
	CheckTypeMetric           = "metric"
	CheckTypeLibraries        = "libraries"
	CheckTypeCount            = "count"
	CheckTypeStorage          = "storage"
	CheckTypeLoad             = "load"
	CheckTypeUsers            = "users"
	CheckTypeSSHIdle          = "ssh_idle"
	CheckTypeTerminalSessions = "terminal_sessions"
	CheckTypeProcessCount     = "process_count"
	CheckTypeHdparm           = "hdparm"
	CheckTypeSensors          = "sensors"
	CheckTypeSmart            = "smart"
	CheckTypeRAID             = "raid"
	CheckTypeStorCLI          = "storcli"
	CheckTypeSSACLI           = "ssacli"
	CheckTypeGlusterCluster   = "gluster_cluster"
	CheckTypeLVM              = "lvm"
	CheckTypeEDAC             = "edac"
	CheckTypeConfig           = "config"
	CheckTypeFDS              = "fds"
	CheckTypeMemory           = "memory"
	CheckTypePressure         = "pressure"
	CheckTypePIDs             = "pids"
	CheckTypeDiskIO           = "diskio"
	CheckTypeConntrack        = "conntrack"
	CheckTypeZombies          = "zombies"
	CheckTypeOOM              = "oom"
	CheckTypeCert             = "cert"
	CheckTypeSQLite           = "sqlite"
	CheckTypeSQLite3          = "sqlite3"
	CheckTypeSQL              = "sql"
	CheckTypeReplication      = "replication"
	CheckTypeMongoDBQuery     = "mongodb-query"
	CheckTypeInfluxDBQuery    = "influxdb-query"
	CheckTypeSize             = "size"
	CheckTypeWebsocket        = "websocket"
	CheckTypeNet              = "net"
	CheckTypeICMP             = "icmp"
	CheckTypeSwap             = "swap"
	CheckTypeRoute            = "route"
	CheckTypeFirewallRules    = "firewall_rules"
	CheckTypeFailedUnits      = "failed_units"
	CheckTypeInotify          = "inotify"
)

// CheckTypeTCPConnections counts established local TCP sockets on a port.
const CheckTypeTCPConnections = "tcp_connections"

// CheckTypeProcessPolicy is a host-only, alert-only process execution policy
// watch. It is built by internal/app rather than the single-shot registry
// because it evaluates every process of one real user as a set.
const CheckTypeProcessPolicy = "process_policy"

var checkSpecByName = mustIndexCheckSpecs(builtinCheckSpecs)

func mustIndexCheckSpecs(specs []checkSpec) map[string]checkSpec {
	out := make(map[string]checkSpec, len(specs))
	for _, spec := range specs {
		if spec.info.Name == "" {
			panic("built-in check has an empty type name")
		}
		if spec.build == nil {
			panic(fmt.Sprintf("built-in check %q has no builder", spec.info.Name))
		}
		if !IsReportingMode(spec.info.DefaultReports) {
			panic(fmt.Sprintf("built-in check %q has invalid default reporting mode %q", spec.info.Name, spec.info.DefaultReports))
		}
		if _, exists := out[spec.info.Name]; exists {
			panic(fmt.Sprintf("duplicate built-in check type %q", spec.info.Name))
		}
		out[spec.info.Name] = spec
	}
	return out
}

// TypeInfoFor returns static metadata for a built-in check type.
func TypeInfoFor(typ string) (TypeInfo, bool) {
	spec, ok := checkSpecByName[typ]
	return spec.info, ok
}

// PredicateFieldsFor returns the threshold predicate fields accepted by a
// built-in check type. The returned slice is a copy so registry metadata cannot
// be mutated by presentation or validation callers.
func PredicateFieldsFor(typ string) []string {
	return slices.Clone(checkSpecByName[typ].predicateFields)
}

// TypeInfos returns every built-in single-shot check type in name order. It is
// the inspection seam cross-package contract tests use to keep config field
// validators in parity with the private constructor registry; the copy avoids
// exposing that mutable registry.
func TypeInfos() []TypeInfo {
	names := slices.Sorted(maps.Keys(checkSpecByName))
	infos := make([]TypeInfo, 0, len(names))
	for _, name := range names {
		infos = append(infos, checkSpecByName[name].info)
	}
	return infos
}

// IsSingleShotType reports whether typ is a built-in single-shot check type.
func IsSingleShotType(typ string) bool {
	_, ok := checkSpecByName[typ]
	return ok
}

// IsServiceScopedType reports whether typ needs per-service runtime context and
// therefore cannot be used as a host watch.
func IsServiceScopedType(typ string) bool {
	info, ok := TypeInfoFor(typ)
	return ok && info.ServiceScoped
}

// RecordsLatency reports whether typ publishes its elapsed check duration as a
// measurement series.
func RecordsLatency(typ string) bool {
	info, ok := TypeInfoFor(typ)
	return ok && info.RecordsLatency
}

// IsMultiMetricType reports whether typ is a host-scoped type that expands into
// one watch per metric and so cannot back a service watch.
func IsMultiMetricType(typ string) bool {
	info, ok := TypeInfoFor(typ)
	return ok && info.MultiMetric
}

// MeterCountKey returns the Result.Data field carrying the current count of a
// count-versus-limit type drawn as a gauge, and whether typ is one.
func MeterCountKey(typ string) (string, bool) {
	info, ok := TypeInfoFor(typ)
	if !ok || info.MeterCountKey == "" {
		return "", false
	}
	return info.MeterCountKey, true
}

// IsResourcePathType reports whether typ is a preflight type whose `path`
// names a host resource that variables may select.
func IsResourcePathType(typ string) bool {
	info, ok := TypeInfoFor(typ)
	return ok && info.ResourcePath
}
