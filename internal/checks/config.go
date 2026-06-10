package checks

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"sermo/internal/execx"
)

// configCheck verifies a service/app's configuration: it runs an optional
// config-test `command` (e.g. `apachectl configtest`, `nginx -t`, `sshd -t`) and
// alerts when it fails (invalid config), and — with on_change over the config
// `path`(s) — alerts when a config file changed since the last cycle. It is a
// health-style check (OK==true means valid and unchanged); change detection
// persists across cycles only when built once (a host watch), like the other
// stateful checks.
type configCheck struct {
	base
	runner   execx.Runner
	argv     []string // config-test command (optional)
	paths    []string // config file(s) to watch (optional)
	onChange bool
	state    *cmdState
}

func (c configCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	// Validity: a non-zero exit from the config-test command means invalid config.
	if len(c.argv) > 0 {
		res, _ := c.runner.Run(ctx, c.argv[0], c.argv[1:]...)
		if res.ExitCode != 0 {
			msg := "config invalid"
			if s := firstLine(res.Stderr); s != "" {
				msg += ": " + s
			} else if s := firstLine(res.Stdout); s != "" {
				msg += ": " + s
			}
			return c.result(false, msg, start)
		}
	}

	// Change detection: alert when a watched config file changed since last cycle.
	if c.onChange && c.state != nil && len(c.paths) > 0 {
		fp := configFingerprint(c.paths)
		if c.state.primed && fp != c.state.last {
			c.state.last = fp
			return c.result(false, "config changed: "+strings.Join(c.paths, ", "), start)
		}
		c.state.last, c.state.primed = fp, true
	}
	return c.result(true, "config ok", start)
}

// configFingerprint summarizes the watched paths by size and mtime so a change is
// detectable across cycles.
func configFingerprint(paths []string) string {
	var b strings.Builder
	for _, p := range paths {
		fi, err := os.Stat(p) //nolint:gosec // operator-configured config path
		if err != nil {
			b.WriteString(p + ":missing;")
			continue
		}
		fmt.Fprintf(&b, "%s:%d:%d;", p, fi.Size(), fi.ModTime().UnixNano())
	}
	return b.String()
}
