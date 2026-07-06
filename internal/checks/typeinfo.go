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
// dispatch, so a new type is named once.
const (
	checkTypeTCP           = "tcp"
	checkTypePorts         = "ports"
	checkTypeHTTP          = "http"
	checkTypeCommand       = "command"
	checkTypeService       = "service"
	checkTypeFileExists    = "file_exists"
	checkTypeFile          = "file"
	checkTypeLockfile      = "lockfile"
	checkTypeBinary        = "binary"
	checkTypePidfile       = "pidfile"
	checkTypeSocket        = "socket"
	checkTypeProcess       = "process"
	checkTypeMetric        = "metric"
	checkTypeLibraries     = "libraries"
	checkTypeCount         = "count"
	checkTypeStorage       = "storage"
	checkTypeAutofs        = "autofs"
	checkTypeLoad          = "load"
	checkTypeUsers         = "users"
	checkTypeProcessCount  = "process_count"
	checkTypeHdparm        = "hdparm"
	checkTypeSensors       = "sensors"
	checkTypeSmart         = "smart"
	checkTypeRAID          = "raid"
	checkTypeEDAC          = "edac"
	checkTypeConfig        = "config"
	checkTypeFDS           = "fds"
	checkTypeMemory        = "memory"
	checkTypePressure      = "pressure"
	checkTypePIDs          = "pids"
	checkTypeDiskIO        = "diskio"
	checkTypeConntrack     = "conntrack"
	checkTypeEntropy       = "entropy"
	checkTypeZombies       = "zombies"
	checkTypeOOM           = "oom"
	checkTypeCert          = "cert"
	checkTypeSQLite        = "sqlite"
	checkTypeSQLite3       = "sqlite3"
	checkTypeSQL           = "sql"
	checkTypeMongoDBQuery  = "mongodb-query"
	checkTypeInfluxDBQuery = "influxdb-query"
	checkTypeSize          = "size"
	checkTypeWebsocket     = "websocket"
	checkTypeNet           = "net"
	checkTypeICMP          = "icmp"
	checkTypeSwap          = "swap"
	checkTypeRoute         = "route"
	checkTypeFirewallRules = "firewall_rules"
)

var typeInfos = []TypeInfo{
	{Name: checkTypeTCP, Health: true},
	{Name: checkTypePorts, Health: true},
	{Name: checkTypeHTTP, Health: true},
	{Name: checkTypeCommand, Health: true},
	{Name: checkTypeService, Health: true, ServiceScoped: true},
	{Name: checkTypeFileExists, Health: true},
	{Name: checkTypeFile, Health: true},
	{Name: checkTypeLockfile, Health: true},
	{Name: checkTypeBinary, Health: true},
	{Name: checkTypePidfile, Health: true},
	{Name: checkTypeSocket, Health: true},
	{Name: checkTypeProcess, Health: true, ServiceScoped: true},
	{Name: checkTypeMetric, ServiceScoped: true},
	{Name: checkTypeLibraries, Health: true},
	{Name: checkTypeCount},
	{Name: checkTypeStorage},
	{Name: checkTypeAutofs, Health: true},
	{Name: checkTypeLoad},
	{Name: checkTypeUsers},
	{Name: checkTypeProcessCount},
	{Name: checkTypeHdparm},
	{Name: checkTypeSensors},
	{Name: checkTypeSmart},
	{Name: checkTypeRAID},
	{Name: checkTypeEDAC},
	{Name: checkTypeConfig, Health: true},
	{Name: checkTypeFDS},
	{Name: checkTypeMemory},
	{Name: checkTypePressure},
	{Name: checkTypePIDs},
	{Name: checkTypeDiskIO},
	{Name: checkTypeConntrack},
	{Name: checkTypeEntropy},
	{Name: checkTypeZombies},
	{Name: checkTypeOOM},
	{Name: checkTypeCert, Health: true},
	{Name: checkTypeSQLite, Health: true},
	{Name: checkTypeSQLite3, Health: true},
	{Name: checkTypeSQL},
	{Name: checkTypeMongoDBQuery},
	{Name: checkTypeInfluxDBQuery},
	{Name: checkTypeSize},
	{Name: checkTypeWebsocket, Health: true},
	{Name: checkTypeNet},
	{Name: checkTypeICMP},
	{Name: checkTypeSwap},
	{Name: checkTypeRoute, Health: true},
	{Name: checkTypeFirewallRules, Health: true},
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
