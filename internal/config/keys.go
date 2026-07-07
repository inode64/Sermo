package config

import "sermo/internal/servicemgr"

const (
	pathKeyApps      = "apps"
	pathKeyLocks     = "locks"
	pathKeyNotifiers = "notifiers"
	pathKeyRuntime   = "runtime"
	pathKeyServices  = "services"
	pathKeyState     = "state"
	pathKeyTemplates = "templates"
	pathKeyWatches   = "watches"
)

const (
	backendAuto    = string(servicemgr.BackendAuto)
	backendSystemd = string(servicemgr.BackendSystemd)
	backendOpenRC  = string(servicemgr.BackendOpenRC)
)
