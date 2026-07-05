package checks

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"sermo/internal/execx"
	"sermo/internal/output"
)

// configCheck verifies a service/app's configuration: it runs an optional
// config-test `command` (e.g. `apachectl configtest`, `nginx -t`, `sshd -t`) and
// alerts when it fails (invalid config), and — with on_change over the config
// `path`(s) — alerts when a config file changed since the last cycle. It is a
// health-style check (OK==true means valid and unchanged). Service workers and
// host watches reuse the check instance across cycles, so change detection
// persists until a config reload/worker rebuild creates a fresh baseline.
type configCheck struct {
	base
	runner   execx.Runner
	argv     []string // config-test command (optional)
	user     string
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
		res, err := c.runConfigCommand(ctx)
		if res.ExitCode == -1 {
			msg := execx.OperatorFailure(err, res, c.timeout)
			if msg == "" {
				msg = execx.CommandDidNotStart
			}
			return c.result(false, "config invalid: "+msg, start)
		}
		if res.ExitCode != 0 {
			msg := "config invalid"
			if s := output.FirstNonEmptyLine(res.Stderr); s != "" {
				msg += ": " + s
			} else if s := output.FirstNonEmptyLine(res.Stdout); s != "" {
				msg += ": " + s
			} else if err != nil {
				msg += ": " + err.Error()
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

func (c configCheck) runConfigCommand(ctx context.Context) (execx.Result, error) {
	if c.user != "" {
		return execx.RunUser(ctx, c.runner, 0, c.user, c.argv[0], c.argv[1:]...)
	}
	return c.runner.Run(ctx, c.argv[0], c.argv[1:]...)
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
