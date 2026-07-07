package checks

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// autofsCheck verifies the autofs automounter is active by inspecting the mount
// table for autofs-type mountpoints — the map roots `automount` maintains while
// it runs (they appear in /proc/mounts as fstype `autofs` and vanish when the
// daemon stops). With a `path` it requires that exact autofs mountpoint to be
// present; with a `count` predicate it compares the number of autofs mountpoints;
// with neither it requires at least one. OK==true means the expectation holds
// (the automounter is active as configured) — the health convention of the
// process/service checks. autofs has no socket/port, so this mount-table probe is
// the liveness signal.
type autofsCheck struct {
	base
	path    string  // a specific autofs mountpoint to require (optional)
	op      string  // count predicate operator (optional)
	value   float64 // count predicate value
	sampler MountSamplerFunc
}

func (c autofsCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultMountSampler
	}
	mounts, err := sampler()
	if err != nil {
		return c.result(false, "autofs: "+err.Error(), start)
	}

	var points []string
	for i := range mounts {
		if mounts[i].FSType == "autofs" {
			points = append(points, mounts[i].MountPoint)
		}
	}
	data := map[string]any{DataKeyCount: len(points), fieldValue: len(points), DataKeyMountpoints: strings.Join(points, ",")}

	if c.path != "" {
		ok := slices.Contains(points, c.path)
		msg := "autofs mountpoint " + c.path + " is active"
		if !ok {
			msg = "autofs mountpoint " + c.path + " is not active"
		}
		res := c.result(ok, msg, start)
		res.Data = data
		return res
	}

	op, value := c.op, c.value
	if op == "" {
		op, value = ">=", 1
	}
	ok := compareFloat(float64(len(points)), op, value)
	res := c.result(ok, fmt.Sprintf("%d autofs mountpoint(s) active", len(points)), start)
	res.Data = data
	return res
}
