package checks

// TypeInfo describes static capabilities of a built-in check type. Runtime
// construction still lives in buildCheck; this table keeps public type lists,
// validation capabilities and health/condition semantics from drifting apart.
type TypeInfo struct {
	Name          string
	Health        bool
	ServiceScoped bool
}

// Built-in check type names (the `type:` selector of a check). This is the
// canonical spelling reused by the typeInfos registry and the buildCheck
// dispatch (and reusable by config validation), so a new type is named once.
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
	CheckTypeMetric        = "metric"
	CheckTypeLibraries     = "libraries"
	CheckTypeCount         = "count"
	CheckTypeStorage       = "storage"
	CheckTypeAutofs        = "autofs"
	CheckTypeLoad          = "load"
	CheckTypeUsers         = "users"
	CheckTypeProcessCount  = "process_count"
	CheckTypeHdparm        = "hdparm"
	CheckTypeSensors       = "sensors"
	CheckTypeSmart         = "smart"
	CheckTypeRAID          = "raid"
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

var typeInfos = []TypeInfo{
	{Name: CheckTypeTCP, Health: true},
	{Name: CheckTypePorts, Health: true},
	{Name: CheckTypeHTTP, Health: true},
	{Name: CheckTypeCommand, Health: true},
	{Name: CheckTypeClock, Health: true},
	{Name: CheckTypeService, Health: true, ServiceScoped: true},
	{Name: CheckTypeFileExists, Health: true},
	{Name: CheckTypeFile, Health: true},
	{Name: CheckTypeLockfile, Health: true},
	{Name: CheckTypeBinary, Health: true},
	{Name: CheckTypePidfile, Health: true},
	{Name: CheckTypeSocket, Health: true},
	{Name: CheckTypeProcess, Health: true, ServiceScoped: true},
	{Name: CheckTypeMetric, ServiceScoped: true},
	{Name: CheckTypeLibraries, Health: true},
	{Name: CheckTypeCount},
	{Name: CheckTypeStorage},
	{Name: CheckTypeAutofs, Health: true},
	{Name: CheckTypeLoad},
	{Name: CheckTypeUsers},
	{Name: CheckTypeProcessCount},
	{Name: CheckTypeHdparm},
	{Name: CheckTypeSensors},
	{Name: CheckTypeSmart},
	{Name: CheckTypeRAID},
	{Name: CheckTypeEDAC},
	{Name: CheckTypeConfig, Health: true},
	{Name: CheckTypeFDS},
	{Name: CheckTypeMemory},
	{Name: CheckTypePressure},
	{Name: CheckTypePIDs},
	{Name: CheckTypeDiskIO},
	{Name: CheckTypeConntrack},
	{Name: CheckTypeEntropy},
	{Name: CheckTypeZombies},
	{Name: CheckTypeOOM},
	{Name: CheckTypeCert, Health: true},
	{Name: CheckTypeSQLite, Health: true},
	{Name: CheckTypeSQLite3, Health: true},
	{Name: CheckTypeSQL},
	{Name: CheckTypeMongoDBQuery},
	{Name: CheckTypeInfluxDBQuery},
	{Name: CheckTypeSize},
	{Name: CheckTypeWebsocket, Health: true},
	{Name: CheckTypeNet},
	{Name: CheckTypeICMP},
	{Name: CheckTypeSwap},
	{Name: CheckTypeRoute, Health: true},
	{Name: CheckTypeFirewallRules, Health: true},
}

var typeInfoByName = indexTypeInfos(typeInfos)

// SingleShotCheckTypes are the check types valid in a service's
// checks:/preflight: sections and (minus service-scoped types) as
// host watches. Config validation consumes this list directly and
// TestSingleShotCheckTypesAreBuildable locks it against the buildCheck dispatch,
// so the two can never drift. Connection-protocol types (mysql, smtp, ...) are
// intentionally absent: they come from the conn registry.
var SingleShotCheckTypes = typeInfoNames(typeInfos)

func indexTypeInfos(infos []TypeInfo) map[string]TypeInfo {
	out := make(map[string]TypeInfo, len(infos))
	for _, info := range infos {
		out[info.Name] = info
	}
	return out
}

func typeInfoNames(infos []TypeInfo) []string {
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.Name)
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
