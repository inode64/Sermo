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

func serviceConditionTypeInfo(name string) TypeInfo {
	return TypeInfo{Name: name, DefaultReports: ReportsCondition, ServiceScoped: true}
}

// checkSpec is the private source of truth for a built-in check's static
// capabilities and constructor. Field validation remains in internal/config so
// it can report configuration paths and error details without an import cycle.
type checkSpec struct {
	info  TypeInfo
	build checkBuilder
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
	CheckTypeMetric           = "metric"
	CheckTypeLibraries        = "libraries"
	CheckTypeCount            = "count"
	CheckTypeStorage          = "storage"
	CheckTypeAutofs           = "autofs"
	CheckTypeLoad             = "load"
	CheckTypeUsers            = "users"
	CheckTypeSSHIdle          = "ssh_idle"
	CheckTypeTerminalSessions = "terminal_sessions"
	CheckTypeProcessCount     = "process_count"
	CheckTypeHdparm           = "hdparm"
	CheckTypeSensors          = "sensors"
	CheckTypeSmart            = "smart"
	CheckTypeRAID             = "raid"
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
	CheckTypeEntropy          = "entropy"
	CheckTypeZombies          = "zombies"
	CheckTypeOOM              = "oom"
	CheckTypeCert             = "cert"
	CheckTypeSQLite           = "sqlite"
	CheckTypeSQLite3          = "sqlite3"
	CheckTypeSQL              = "sql"
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
)

// CheckTypeTCPConnections counts established local TCP sockets on a port.
const CheckTypeTCPConnections = "tcp_connections"

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

// TypeInfos returns every built-in single-shot check type in name order. The
// copy lets validators and tests verify registry parity without exposing the
// mutable constructor registry.
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
