package config

import "sermo/internal/servicemgr"

const (
	pathKeyApps      = "apps"
	pathKeyLocks     = "locks"
	pathKeyNotifiers = "notifiers"
	pathKeyServices  = "services"
	pathKeyTemplates = "templates"
	pathKeyWatches   = "watches"
)

const (
	backendAuto    = string(servicemgr.BackendAuto)
	backendSystemd = string(servicemgr.BackendSystemd)
	backendOpenRC  = string(servicemgr.BackendOpenRC)
)
