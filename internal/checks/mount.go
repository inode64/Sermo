package checks

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// Mount is one entry of the mount table.
type Mount struct {
	Device     string
	MountPoint string
	FSType     string
	Options    []string
}

// MountSamplerFunc returns the current mount table. Injected for tests; the
// default reads /proc/mounts.
type MountSamplerFunc func() ([]Mount, error)

// mountCheck verifies a mount point. The base condition is "is it mounted"
// (expectMount); further conditions (fstype, options, device) refine what counts
// as healthy and are easy to extend. It is health-style: OK==true means the mount
// matches expectations, so as a watch it fires its hook on failure.
type mountCheck struct {
	base
	path        string
	expectMount bool
	fstype      string
	device      string
	options     []string
	sampler     MountSamplerFunc
}

func (c mountCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultMountSampler
	}
	mounts, err := sampler()
	if err != nil {
		return c.result(false, "mount: "+err.Error(), start)
	}

	var found *Mount
	for i := range mounts {
		if mounts[i].MountPoint == c.path {
			found = &mounts[i]
			break
		}
	}
	mounted := found != nil
	data := map[string]any{"path": c.path, "mounted": mounted}

	// expectMount=false: assert the path is NOT a mount point.
	if !c.expectMount {
		res := c.result(!mounted, fmt.Sprintf("%s mounted=%t (want unmounted)", c.path, mounted), start)
		res.Data = data
		return res
	}
	if !mounted {
		res := c.result(false, c.path+" is not mounted", start)
		res.Data = data
		return res
	}

	data["fstype"] = found.FSType
	data["device"] = found.Device
	data["options"] = strings.Join(found.Options, ",")

	ok := true
	var problems []string
	if c.fstype != "" && found.FSType != c.fstype {
		ok = false
		problems = append(problems, fmt.Sprintf("fstype %s (want %s)", found.FSType, c.fstype))
	}
	if c.device != "" && found.Device != c.device {
		ok = false
		problems = append(problems, fmt.Sprintf("device %s (want %s)", found.Device, c.device))
	}
	for _, opt := range c.options {
		if !containsString(found.Options, opt) {
			ok = false
			problems = append(problems, "missing option "+opt)
		}
	}

	msg := fmt.Sprintf("%s mounted (%s on %s)", c.path, found.FSType, found.Device)
	if !ok {
		msg = c.path + ": " + strings.Join(problems, ", ")
	}
	res := c.result(ok, msg, start)
	res.Data = data
	return res
}

// defaultMountSampler reads the mount table from /proc/mounts.
func defaultMountSampler() ([]Mount, error) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil, err
	}
	var out []Mount
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		out = append(out, Mount{
			Device:     unescapeMount(fields[0]),
			MountPoint: unescapeMount(fields[1]),
			FSType:     fields[2],
			Options:    strings.Split(fields[3], ","),
		})
	}
	return out, nil
}

// unescapeMount decodes the octal escapes /proc/mounts uses for space, tab,
// newline and backslash in device and mount-point fields.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

func containsString(list []string, v string) bool {
	for _, e := range list {
		if e == v {
			return true
		}
	}
	return false
}
