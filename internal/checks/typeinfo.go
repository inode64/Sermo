package checks

// TypeInfo describes static capabilities of a built-in check type.
type TypeInfo struct {
	Name          string
	Health        bool
	ServiceScoped bool
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
	CheckTypeTCP           = "tcp"
	CheckTypePorts         = "ports"
	CheckTypeHTTP          = "http"
	CheckTypeCommand       = "command"
	CheckTypeClock         = "clock"
	CheckTypeService       = "service"
	CheckTypeFileExists    = "file_exists"
	CheckTypeFile          = "file"
	CheckTypeLockfile      = "lockfile"
	CheckTypeBinary        = "binary"
	CheckTypePidfile       = "pidfile"
	CheckTypeSocket        = "socket"
	CheckTypeProcess       = "process"
	CheckTypeStaleBinary   = "stale_binary"
	CheckTypeMetric        = "metric"
	CheckTypeLibraries     = "libraries"
	CheckTypeCount         = "count"
	CheckTypeStorage       = "storage"
	CheckTypeAutofs        = "autofs"
	CheckTypeLoad          = "load"
	CheckTypeUsers         = "users"
	CheckTypeSSHIdle       = "ssh_idle"
	CheckTypeProcessCount  = "process_count"
	CheckTypeHdparm        = "hdparm"
	CheckTypeSensors       = "sensors"
	CheckTypeSmart         = "smart"
	CheckTypeRAID          = "raid"
	CheckTypeLVM           = "lvm"
	CheckTypeEDAC          = "edac"
	CheckTypeConfig        = "config"
	CheckTypeFDS           = "fds"
	CheckTypeMemory        = "memory"
	CheckTypePressure      = "pressure"
	CheckTypePIDs          = "pids"
	CheckTypeDiskIO        = "diskio"
	CheckTypeConntrack     = "conntrack"
	CheckTypeEntropy       = "entropy"
	CheckTypeZombies       = "zombies"
	CheckTypeOOM           = "oom"
	CheckTypeCert          = "cert"
	CheckTypeSQLite        = "sqlite"
	CheckTypeSQLite3       = "sqlite3"
	CheckTypeSQL           = "sql"
	CheckTypeMongoDBQuery  = "mongodb-query"
	CheckTypeInfluxDBQuery = "influxdb-query"
	CheckTypeSize          = "size"
	CheckTypeWebsocket     = "websocket"
	CheckTypeNet           = "net"
	CheckTypeICMP          = "icmp"
	CheckTypeSwap          = "swap"
	CheckTypeRoute         = "route"
	CheckTypeFirewallRules = "firewall_rules"
)

// CheckTypeTCPConnections counts established local TCP sockets on a port.
const CheckTypeTCPConnections = "tcp_connections"

var (
	checkSpecByName = indexCheckSpecs(builtinCheckSpecs)
	typeInfoByName  = indexTypeInfos(builtinCheckSpecs)
	// singleShotCheckTypes are the check types valid in a service's
	// checks:/preflight: sections and (minus service-scoped types) as host
	// watches. TestSingleShotCheckTypesAreBuildable locks the list against the
	// buildCheck dispatch so the advertised types and the builder cannot drift.
	// Connection-protocol types (mysql, smtp, ...) are intentionally absent:
	// they come from the conn registry.
	singleShotCheckTypes = checkSpecNames(builtinCheckSpecs)
)

func indexCheckSpecs(specs []checkSpec) map[string]checkSpec {
	out := make(map[string]checkSpec, len(specs))
	for _, spec := range specs {
		out[spec.info.Name] = spec
	}
	return out
}

func indexTypeInfos(specs []checkSpec) map[string]TypeInfo {
	out := make(map[string]TypeInfo, len(specs))
	for _, spec := range specs {
		out[spec.info.Name] = spec.info
	}
	return out
}

func checkSpecNames(specs []checkSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.info.Name)
	}
	return out
}

// TypeInfoFor returns static metadata for a built-in check type.
func TypeInfoFor(typ string) (TypeInfo, bool) {
	info, ok := typeInfoByName[typ]
	return info, ok
}

// IsSingleShotType reports whether typ is a built-in single-shot check type.
func IsSingleShotType(typ string) bool {
	_, ok := typeInfoByName[typ]
	return ok
}

// IsServiceScopedType reports whether typ needs per-service runtime context and
// therefore cannot be used as a host watch.
func IsServiceScopedType(typ string) bool {
	info, ok := TypeInfoFor(typ)
	return ok && info.ServiceScoped
}
