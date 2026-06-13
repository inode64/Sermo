package servicemgr

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"sermo/internal/execx"
)

// Init-definition patterns the wizard uses to derive a pidfile/exe. All are
// best-effort and only accept literal values (a leading `$` means the script
// builds the path from a variable we don't expand, so it's skipped).
var (
	// systemd ExecStart --value renders as `{ path=/usr/sbin/nginx ; argv[]=… }`.
	systemdExecPath = regexp.MustCompile(`path=([^ ;]+)`)
	// OpenRC `pidfile="/run/foo.pid"` (init script or conf.d override).
	openrcPidfileVar = regexp.MustCompile(`(?m)^[[:space:]]*pidfile=["']?([^"'\s$]+)`)
	// OpenRC `start-stop-daemon … --pidfile /run/foo.pid`.
	openrcPidfileArg = regexp.MustCompile(`--pidfile[ =]["']?([^"'\s$]+)`)
	// OpenRC `command="/usr/bin/foo"`.
	openrcCommandVar = regexp.MustCompile(`(?m)^[[:space:]]*command=["']?([^"'\s$]+)`)
)

// DetectProc inspects a service's init definition to derive a stable pidfile
// path and main executable, for the wizard's PID question (see docs/wizards.md).
// It is best-effort: a field it cannot determine comes back "". For systemd it
// reads `systemctl show` PIDFile and ExecStart; for OpenRC it scans the init
// script and its conf.d override for `pidfile=`, a `start-stop-daemon
// --pidfile`, and `command=`. runner/readFile are injected for tests; nil uses
// the host.
func DetectProc(ctx context.Context, runner execx.Runner, readFile func(string) ([]byte, error), backend Backend, unit string) (pidfile, exe string) {
	if unit == "" {
		return "", ""
	}
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	if readFile == nil {
		readFile = os.ReadFile
	}
	switch backend {
	case BackendSystemd:
		return detectSystemdProc(ctx, runner, unit)
	case BackendOpenRC:
		return detectOpenRCProc(readFile, unit)
	}
	return "", ""
}

func detectSystemdProc(ctx context.Context, runner execx.Runner, unit string) (pidfile, exe string) {
	if res, err := execx.Run(ctx, runner, defaultDetectTimeout, "systemctl", "show", "-p", "PIDFile", "--value", "--", unit); err == nil {
		if v := strings.TrimSpace(res.Stdout); v != "" {
			pidfile = v
		}
	}
	if res, err := execx.Run(ctx, runner, defaultDetectTimeout, "systemctl", "show", "-p", "ExecStart", "--value", "--", unit); err == nil {
		if m := systemdExecPath.FindStringSubmatch(res.Stdout); m != nil {
			exe = m[1]
		}
	}
	return pidfile, exe
}

func detectOpenRCProc(readFile func(string) ([]byte, error), unit string) (pidfile, exe string) {
	var blob strings.Builder
	for _, path := range []string{filepath.Join("/etc/init.d", unit), filepath.Join("/etc/conf.d", unit)} {
		if data, err := readFile(path); err == nil {
			blob.Write(data)
			blob.WriteByte('\n')
		}
	}
	text := blob.String()
	if m := openrcPidfileVar.FindStringSubmatch(text); m != nil {
		pidfile = m[1]
	} else if m := openrcPidfileArg.FindStringSubmatch(text); m != nil {
		pidfile = m[1]
	}
	if m := openrcCommandVar.FindStringSubmatch(text); m != nil {
		exe = m[1]
	}
	return pidfile, exe
}
