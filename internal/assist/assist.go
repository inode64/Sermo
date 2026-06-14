package assist

// Volume is a candidate disk filesystem the volume assistant can monitor.
type Volume struct {
	Mountpoint string
	FSType     string
	Device     string
}

// Iface is a candidate network interface the net assistant can monitor.
type Iface struct {
	Name       string
	Up         bool
	Loopback   bool
	HasAddress bool
}

// DaemonCandidate is a service target detected on the host that the service
// assistant can enable, with the facts the wizard shows and confirms. Catalog
// candidates write `uses: Name`; Generic candidates are active backend units not
// backed by a catalog daemon and write a self-contained service check.
type DaemonCandidate struct {
	Name          string   // service name to write; catalog daemon name unless Generic
	Title         string   // display name
	Unit          string   // resolved init unit for the active backend
	Status        string   // backend status for Unit (active/inactive/failed/unknown)
	Generic       bool     // active backend unit without a catalog daemon
	Port          int      // catalog default port (0 = none)
	ConfigPaths   []string // config file locations that exist on the host
	UnitPresent   bool     // the init unit exists on the active backend
	PortListening bool     // something is listening on Port
	Pidfile       string   // pidfile path derived from the init definition (best-effort)
	Exe           string   // main executable derived from the init definition (best-effort)
	Cmd           string   // cmdline regex derived from the init definition (best-effort)
	User          string   // process owner derived from the init definition (best-effort)
}

// Env carries the host facts and config an assistant needs, injected so the
// assistants are testable without touching the real host or config.
type Env struct {
	Notifiers     []string                          // names from the config's `notifiers:` section
	DefaultNotify []string                          // top-level `notify` default; nil = no inherited notification
	Backend       string                            // active init system: "systemd" | "openrc"
	Volumes       func() ([]Volume, error)          // candidate disk volumes
	Ifaces        func() ([]Iface, error)           // host network interfaces
	DefaultIfaces []string                          // interfaces with an up default route
	Daemons       func() ([]DaemonCandidate, error) // catalog daemons detected as installed
	ServiceNames  map[string]struct{}               // already-configured service names (collision check)
}

// Result is what an assistant produced: a fragment to merge under `watches:`
// (watch name -> entry) and/or as kind:service files (`Services`: service name
// -> body), plus a short human summary.
type Result struct {
	Watches  map[string]any
	Services map[string]any
	Summary  string
}

// Assistant guides the user through creating one kind of watch.
type Assistant interface {
	Name() string  // stable command token, e.g. "volume"
	Title() string // one-line description shown in the menu
	Run(p *Prompt, env Env) (Result, error)
}

// registry is the ordered set of available assistants. Add new ones here.
var registry = []Assistant{
	serviceAssistant{},
	volumeAssistant{},
	netAssistant{},
	uplinkAssistant{},
}

// Assistants returns the registered assistants in menu order.
func Assistants() []Assistant { return registry }

// Lookup finds an assistant by name.
func Lookup(name string) (Assistant, bool) {
	for _, a := range registry {
		if a.Name() == name {
			return a, true
		}
	}
	return nil, false
}
