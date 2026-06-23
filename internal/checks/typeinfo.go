package checks

// TypeInfo describes static capabilities of a built-in check type. Runtime
// construction still lives in buildCheck; this table keeps public type lists,
// validation capabilities and health/condition semantics from drifting apart.
type TypeInfo struct {
	Name          string
	Health        bool
	ServiceScoped bool
}

var typeInfos = []TypeInfo{
	{Name: "tcp", Health: true},
	{Name: "ports", Health: true},
	{Name: "http", Health: true},
	{Name: "command", Health: true},
	{Name: "service", Health: true, ServiceScoped: true},
	{Name: "file_exists", Health: true},
	{Name: "file", Health: true},
	{Name: "lockfile", Health: true},
	{Name: "binary", Health: true},
	{Name: "pidfile", Health: true},
	{Name: "socket", Health: true},
	{Name: "process", Health: true, ServiceScoped: true},
	{Name: "metric", ServiceScoped: true},
	{Name: "libraries", Health: true},
	{Name: "count"},
	{Name: "storage"},
	{Name: "autofs", Health: true},
	{Name: "load"},
	{Name: "users"},
	{Name: "hdparm"},
	{Name: "sensors"},
	{Name: "smart"},
	{Name: "raid"},
	{Name: "edac"},
	{Name: "config", Health: true},
	{Name: "fds"},
	{Name: "memory"},
	{Name: "pressure"},
	{Name: "pids"},
	{Name: "diskio"},
	{Name: "conntrack"},
	{Name: "entropy"},
	{Name: "zombies"},
	{Name: "oom"},
	{Name: "cert", Health: true},
	{Name: "sqlite", Health: true},
	{Name: "sqlite3", Health: true},
	{Name: "sql"},
	{Name: "mongodb-query"},
	{Name: "influxdb-query"},
	{Name: "size"},
	{Name: "websocket", Health: true},
	{Name: "net"},
	{Name: "icmp"},
	{Name: "swap"},
	{Name: "route", Health: true},
	{Name: "firewall_rules", Health: true},
}

var typeInfoByName = indexTypeInfos(typeInfos)

// SingleShotCheckTypes are the check types valid in a service's
// checks:/preflight:/postflight: sections and (minus service-scoped types) as
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
