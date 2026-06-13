package servicemgr

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"sermo/internal/execx"
)

// Init-definition patterns the wizard uses to derive a pidfile/exe. All are
// best-effort and only accept literal values (a leading `$` means the script
// builds the path from a variable we don't expand, so it's skipped).
var (
	// systemd ExecStart --value renders as `{ path=/usr/sbin/nginx ; argv[]=… }`.
	systemdExecPath = regexp.MustCompile(`path=([^ ;]+)`)
	// OpenRC shell assignments in init scripts and conf.d overrides.
	openrcAssign = regexp.MustCompile(`(?m)^[[:space:]]*(?:local[[:space:]]+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)
	// OpenRC `start-stop-daemon … --pidfile /run/foo.pid`.
	openrcPidfileArg = regexp.MustCompile(`--pidfile[ =]("[^"]+"|'[^']+'|[^[:space:]\\]+)`)
	// OpenRC/OpenVPN-style `--writepid /run/foo.pid`.
	openrcWritePIDArg = regexp.MustCompile(`--writepid[ =]("[^"]+"|'[^']+'|[^[:space:]\\]+)`)
	// OpenRC `start-stop-daemon … --exec /usr/bin/foo`.
	openrcExecArg = regexp.MustCompile(`--exec[ =]("[^"]+"|'[^']+'|[^[:space:]\\]+)`)
	// OpenRC `--user user[:group]`, either in start_stop_daemon_args or inline.
	openrcUserArg = regexp.MustCompile(`--user[ =]("[^"]+"|'[^']+'|[^[:space:]\\]+)`)
	// OpenRC start-stop-daemon command after `--`, possibly on the next line.
	openrcCommandAfterDash = regexp.MustCompile(`--[[:space:]]*\\?[[:space:]]*(?:\r?\n[[:space:]]*)?("[^"]+"|'[^']+'|\$\{?[A-Za-z_][A-Za-z0-9_]*\}?|/[^[:space:]\\]+)`)
	openrcSimpleVarRef     = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(%/)|#([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)
	openrcNonEmptyCond     = regexp.MustCompile(`^\[[[:space:]]+-n[[:space:]]+(.+)[[:space:]]+\]$`)
	openrcNotEqualCond     = regexp.MustCompile(`^\[[[:space:]]+(.+)[[:space:]]+!=[[:space:]]+(.+)[[:space:]]+\]$`)
)

// ProcInfo is the init-derived process identity the wizard can offer as a
// starting point. It is best-effort and only carries literal, resolved values.
type ProcInfo struct {
	Pidfile string
	Exe     string
	Cmd     string
	User    string
}

type openRCBranch struct {
	parent bool
	cond   bool
	known  bool
}

// DetectProc inspects a service's init definition to derive a stable pidfile
// path and main executable, for the wizard's PID question (see docs/wizards.md).
// It is best-effort: a field it cannot determine comes back "". For systemd it
// reads `systemctl show` PIDFile and ExecStart; for OpenRC it scans the init
// script and its conf.d override for `pidfile=`, a `start-stop-daemon
// --pidfile`, and `command=`. runner/readFile are injected for tests; nil uses
// the host.
func DetectProc(ctx context.Context, runner execx.Runner, readFile func(string) ([]byte, error), backend Backend, unit string) (pidfile, exe string) {
	info := DetectProcInfo(ctx, runner, readFile, backend, unit)
	return info.Pidfile, info.Exe
}

// DetectProcInfo is the richer form of DetectProc used by the wizard. It keeps
// DetectProc's legacy pidfile/exe fields and also reports a cmdline regex and
// user when OpenRC exposes command/command_user without a pidfile.
func DetectProcInfo(ctx context.Context, runner execx.Runner, readFile func(string) ([]byte, error), backend Backend, unit string) ProcInfo {
	if unit == "" {
		return ProcInfo{}
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
	return ProcInfo{}
}

func detectSystemdProc(ctx context.Context, runner execx.Runner, unit string) ProcInfo {
	var info ProcInfo
	if res, err := execx.Run(ctx, runner, defaultDetectTimeout, "systemctl", "show", "-p", "PIDFile", "--value", "--", unit); err == nil {
		if v := strings.TrimSpace(res.Stdout); v != "" {
			info.Pidfile = v
		}
	}
	if res, err := execx.Run(ctx, runner, defaultDetectTimeout, "systemctl", "show", "-p", "ExecStart", "--value", "--", unit); err == nil {
		if m := systemdExecPath.FindStringSubmatch(res.Stdout); m != nil {
			info.Exe = m[1]
		}
	}
	return info
}

func detectOpenRCProc(readFile func(string) ([]byte, error), unit string) ProcInfo {
	var blob strings.Builder
	for _, path := range []string{filepath.Join("/etc/init.d", unit), filepath.Join("/etc/conf.d", unit)} {
		if data, err := readFile(path); err == nil {
			blob.Write(data)
			blob.WriteByte('\n')
		}
	}
	text := blob.String()
	vars := openRCAssignments(text, unit)
	info := ProcInfo{
		Pidfile: firstNonEmpty(vars["pidfile"], vars["PIDFILE"], suffixVar(vars, "_PIDFILE")),
		Exe:     vars["command"],
		User:    serviceUser(firstNonEmpty(vars["command_user"], userFromArgs(vars["start_stop_daemon_args"]), userFromArgs(text))),
	}
	if info.Pidfile == "" {
		info.Pidfile = firstResolvedArg(text, vars, openrcPidfileArg, openrcWritePIDArg)
	}
	if info.Exe == "" {
		info.Exe = firstResolvedArg(text, vars, openrcExecArg, openrcCommandAfterDash)
	}
	if command := vars["command"]; command != "" {
		info.Cmd = commandRegex(command)
	}
	return info
}

func openRCAssignments(text, unit string) map[string]string {
	vars := map[string]string{
		"CHROOT":     "",
		"RC_PREFIX":  "",
		"RC_SVCNAME": unit,
		"SVCNAME":    unit,
	}
	active := true
	var stack []openRCBranch
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if b, ok := openRCIfBranch(line, active, vars); ok {
			stack = append(stack, b)
			if b.known {
				active = b.parent && b.cond
			}
			continue
		}
		if line == "else" && len(stack) > 0 {
			b := stack[len(stack)-1]
			if b.known {
				active = b.parent && !b.cond
			} else {
				active = b.parent
			}
			continue
		}
		if line == "fi" && len(stack) > 0 {
			b := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			active = b.parent
			continue
		}
		if !active {
			continue
		}
		if strings.HasPrefix(line, ":") {
			expr := strings.TrimSpace(strings.TrimPrefix(line, ":"))
			name, _, ok := defaultExpr(expr)
			if !ok {
				continue
			}
			if value, ok := resolveOpenRCValue(expr, vars); ok {
				vars[name] = value
			}
			continue
		}
		if m := openrcAssign.FindStringSubmatch(line); m != nil {
			name := m[1]
			value, ok := resolveOpenRCValue(m[2], vars)
			if ok {
				vars[name] = value
			}
		}
	}
	return vars
}

func openRCIfBranch(line string, active bool, vars map[string]string) (openRCBranch, bool) {
	if !strings.HasPrefix(line, "if ") || !strings.HasSuffix(line, "then") {
		return openRCBranch{}, false
	}
	expr := strings.TrimSpace(strings.TrimPrefix(line, "if "))
	expr = strings.TrimSpace(strings.TrimSuffix(expr, "then"))
	expr = strings.TrimSpace(strings.TrimSuffix(expr, ";"))
	cond, known := evalOpenRCCondition(expr, vars)
	return openRCBranch{parent: active, cond: cond, known: known}, true
}

func evalOpenRCCondition(expr string, vars map[string]string) (bool, bool) {
	result := true
	for _, part := range strings.Split(expr, "&&") {
		part = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part), "\\"))
		ok, known := evalOpenRCTerm(part, vars)
		if !known {
			return false, false
		}
		result = result && ok
	}
	return result, true
}

func evalOpenRCTerm(term string, vars map[string]string) (bool, bool) {
	if m := openrcNonEmptyCond.FindStringSubmatch(term); m != nil {
		value, ok := resolveOpenRCValue(m[1], vars)
		return value != "", ok
	}
	if m := openrcNotEqualCond.FindStringSubmatch(term); m != nil {
		left, ok := resolveOpenRCValue(m[1], vars)
		if !ok {
			return false, false
		}
		right := shellWord(m[2])
		return left != right, true
	}
	return false, false
}

func resolveOpenRCValue(raw string, vars map[string]string) (string, bool) {
	s := strings.TrimSpace(stripShellComment(raw))
	if strings.Contains(s, "$(") || strings.Contains(s, "`") {
		return "", false
	}
	s = shellWord(s)
	for range 8 {
		if name, def, ok := defaultExpr(s); ok {
			if value, exists := vars[name]; exists && value != "" {
				return value, true
			}
			s = shellWord(def)
			continue
		}
		var unresolved bool
		next := openrcSimpleVarRef.ReplaceAllStringFunc(s, func(match string) string {
			m := openrcSimpleVarRef.FindStringSubmatch(match)
			name := m[1]
			if name == "" {
				name = m[4]
			}
			value, ok := vars[name]
			if !ok {
				unresolved = true
				return match
			}
			if m[2] == "%/" {
				value = strings.TrimSuffix(value, "/")
			}
			if m[3] != "" {
				value = trimShellPrefixPattern(value, m[3])
			}
			return value
		})
		if unresolved {
			return "", false
		}
		if strings.Contains(next, "$") {
			return "", false
		}
		if next == s {
			return next, true
		}
		s = next
	}
	return "", false
}

func trimShellPrefixPattern(value, pattern string) string {
	if strings.HasPrefix(pattern, "*") {
		suffix := strings.TrimPrefix(pattern, "*")
		if suffix == "" {
			return value
		}
		if i := strings.Index(value, suffix); i >= 0 {
			return value[i+len(suffix):]
		}
		return value
	}
	return strings.TrimPrefix(value, pattern)
}

func defaultExpr(s string) (name, def string, ok bool) {
	if !strings.HasPrefix(s, "${") || !strings.HasSuffix(s, "}") {
		return "", "", false
	}
	body := s[2 : len(s)-1]
	depth := 0
	for i := 0; i < len(body)-1; i++ {
		if body[i] == '$' && body[i+1] == '{' {
			depth++
			i++
			continue
		}
		if body[i] == '}' && depth > 0 {
			depth--
			continue
		}
		if depth == 0 && body[i] == ':' && (body[i+1] == '-' || body[i+1] == '=') {
			name = strings.TrimSpace(body[:i])
			def = strings.TrimSpace(body[i+2:])
			return name, def, name != "" && def != ""
		}
	}
	return "", "", false
}

func stripShellComment(s string) string {
	inSingle, inDouble := false, false
	var prev rune
	for i, r := range s {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble && (i == 0 || unicode.IsSpace(prev)) {
				return s[:i]
			}
		}
		prev = r
	}
	return s
}

func shellWord(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return strings.TrimSpace(s[1 : len(s)-1])
		}
	}
	return s
}

func firstResolvedArg(text string, vars map[string]string, exprs ...*regexp.Regexp) string {
	for _, expr := range exprs {
		for _, m := range expr.FindAllStringSubmatch(text, -1) {
			if value, ok := resolveOpenRCValue(m[1], vars); ok && value != "" {
				return value
			}
		}
	}
	return ""
}

func suffixVar(vars map[string]string, suffix string) string {
	for name, value := range vars {
		if strings.HasSuffix(name, suffix) && value != "" {
			return value
		}
	}
	return ""
}

func userFromArgs(s string) string {
	if m := openrcUserArg.FindStringSubmatch(s); m != nil {
		return shellWord(m[1])
	}
	return ""
}

func serviceUser(s string) string {
	s = shellWord(strings.TrimSpace(s))
	user, _, _ := strings.Cut(s, ":")
	return strings.TrimSpace(user)
}

func commandRegex(command string) string {
	return `(^|[[:space:]])` + regexp.QuoteMeta(command) + `($|[[:space:]])`
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
