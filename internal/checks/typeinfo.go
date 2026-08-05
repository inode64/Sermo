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

var checkSpecByName = indexCheckSpecs(builtinCheckSpecs)

func indexCheckSpecs(specs []checkSpec) map[string]checkSpec {
	out := make(map[string]checkSpec, len(specs))
	for _, spec := range specs {
		out[spec.info.Name] = spec
	}
	return out
}

// TypeInfoFor returns static metadata for a built-in check type.
func TypeInfoFor(typ string) (TypeInfo, bool) {
	spec, ok := checkSpecByName[typ]
	return spec.info, ok
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
