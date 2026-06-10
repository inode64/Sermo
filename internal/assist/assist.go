package assist

// Volume is a candidate disk filesystem the volume assistant can monitor.
type Volume struct {
	Mountpoint string
	FSType     string
	Device     string
}

// Iface is a candidate network interface the net assistant can monitor.
type Iface struct {
	Name     string
	Up       bool
	Loopback bool
}

// Env carries the host facts and config an assistant needs, injected so the
// assistants are testable without touching the real host or config.
type Env struct {
	Notifiers     []string                 // names from the config's `notifiers:` section
	DefaultNotify []string                 // top-level `notify` default; nil means no inherited notification
	Volumes       func() ([]Volume, error) // candidate disk volumes
	Ifaces        func() ([]Iface, error)  // host network interfaces
}

// Result is what an assistant produced: a fragment to merge under `watches:`
// (watch name -> entry) and a short human summary.
type Result struct {
	Watches map[string]any
	Summary string
}

// Assistant guides the user through creating one kind of watch.
type Assistant interface {
	Name() string  // stable command token, e.g. "volume"
	Title() string // one-line description shown in the menu
	Run(p *Prompt, env Env) (Result, error)
}

// registry is the ordered set of available assistants. Add new ones here.
var registry = []Assistant{
	volumeAssistant{},
	netAssistant{},
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
