package assist

// Volume is a candidate disk filesystem the volume assistant can monitor.
type Volume struct {
	Mountpoint string
	FSType     string
	Device     string
}

// MountCandidate is an fstab-backed mount target the mount assistant can
// register as a storage target with a mount block.
type MountCandidate struct {
	Path    string
	Source  string
	FSType  string
	Options string
	Mounted bool
}

// Iface is a candidate network interface the net assistant can monitor.
type Iface struct {
	Name       string
	Up         bool
	Loopback   bool
	HasAddress bool
}

// ServiceCandidate is a service target detected on the host that the service
// assistant can enable, with the facts the wizard shows and confirms. Catalog
// candidates write `uses: Name`; Generic candidates are active backend units not
// backed by a catalog service and write a self-contained service check.
type ServiceCandidate struct {
	Name          string // service name to write; catalog service name unless Generic
	Title         string // display name
	Unit          string // resolved init unit for the active backend
	Status        string // backend status for Unit (active/inactive/failed/unknown)
	Generic       bool   // active backend unit without a catalog service
	Port          int    // catalog default port (0 = none)
	Variables     map[string]any
	ConfigPaths   []string // config file locations that exist on the host
	UnitPresent   bool     // the init unit exists on the active backend
	PortListening bool     // something is listening on Port
	Pidfile       string   // pidfile path derived from the init definition (best-effort)
	Exe           string   // main executable derived from the init definition (best-effort)
	Cmd           string   // cmdline regex derived from the init definition (best-effort)
	User          string   // process owner derived from the init definition (best-effort)
}

// DockerCandidate is a Docker container detected on the host that can be written
// as a controlled Sermo service.
type DockerCandidate struct {
	Name      string // service name to write
	Title     string // display label
	Container string // Docker container name or id for control.container
	Status    string // Docker state label: running, exited, paused, ...
	Socket    string // Docker API socket used by control and checks
}

// VMCandidate is a libvirt/QEMU domain detected on the host that can be written
// as a controlled Sermo service.
type VMCandidate struct {
	Name   string // service name to write
	Title  string // display label
	Domain string // libvirt domain name for control.domain
	Status string // Sermo-normalized status label
	URI    string // libvirt connect URI
	Socket string // libvirt API socket used by control and checks
}

// Env carries the host facts and config an assistant needs, injected so the
// assistants are testable without touching the real host or config.
type Env struct {
	Notifiers        []string                           // names from the config's `notifiers:` section
	DefaultNotify    []string                           // top-level `notify` default; nil = no inherited notification
	Backend          string                             // active init system: "systemd" | "openrc"
	Volumes          func() ([]Volume, error)           // candidate disk volumes
	Mounts           func() ([]MountCandidate, error)   // candidate fstab-backed mount units
	Ifaces           func() ([]Iface, error)            // host network interfaces
	DefaultIfaces    []string                           // interfaces with an up default route
	CatalogServices  func() ([]ServiceCandidate, error) // catalog services detected as installed
	DockerContainers func() ([]DockerCandidate, error)  // Docker containers detected on the host
	VMs              func() ([]VMCandidate, error)      // libvirt/QEMU domains detected on the host
	ServiceNames     map[string]struct{}                // already-configured service names (collision check)
}

// Result is what an assistant produced: watch entries that the CLI renders as
// watch or storage documents, kind:service files (`Services`: service name ->
// body), mount documents, plus a short human summary.
type Result struct {
	Watches  map[string]any
	Services map[string]any
	Mounts   map[string]any
	Summary  string
}

// Assistant guides the user through creating one kind of watch.
type Assistant interface {
	Name() string  // stable command token, e.g. "volume"
	Title() string // one-line description shown in the menu
	Run(p *Prompt, env Env) (Result, error)
}

// AssistantName* constants are the stable command tokens accepted by
// `sermoctl wizard`.
const (
	AssistantNameMount   = "mount"
	AssistantNameNet     = "net"
	AssistantNameService = "service"
	AssistantNameUplink  = "uplink"
	AssistantNameVM      = "vm"
	AssistantNameVolume  = "volume"
)

// registry is the ordered set of available assistants. Add new ones here.
var registry = []Assistant{
	serviceAssistant{},
	dockerAssistant{},
	vmAssistant{},
	mountAssistant{},
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
