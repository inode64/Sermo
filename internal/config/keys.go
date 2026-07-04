package config

import "sermo/internal/servicemgr"

const (
	pathKeyApps      = "apps"
	pathKeyCatalog   = "catalog"
	pathKeyNetworks  = "networks"
	pathKeyNotifiers = "notifiers"
	pathKeyRuntime   = "runtime"
	pathKeyServices  = "services"
	pathKeyState     = "state"
	pathKeyStorages  = "storages"
	pathKeyTemplates = "templates"
	pathKeyWatches   = "watches"
)

const (
	backendAuto    = string(servicemgr.BackendAuto)
	backendSystemd = string(servicemgr.BackendSystemd)
	backendOpenRC  = string(servicemgr.BackendOpenRC)
)
