package servicemgr

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

// DetectProcInfo inspects a service's init definition to derive a stable
// pidfile path and main executable, for the wizard's PID question (see
// docs/wizards.md). It is best-effort: a field it cannot determine comes back
// "". For systemd it reads `systemctl show` PIDFile and ExecStart; for OpenRC
// it scans the init script and its conf.d override for `pidfile=`, a
// `start-stop-daemon --pidfile`, and `command=`. It also reports a cmdline
// regex and user when OpenRC exposes command/command_user without a pidfile.
// runner/readFile are injected for tests; nil uses the host.
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
	if res, err := execx.Run(ctx, runner, defaultDetectTimeout, cmdSystemctl, "show", "-p", "PIDFile", "--value", "--", unit); err == nil {
		if v := strings.TrimSpace(res.Stdout); v != "" {
			info.Pidfile = cleanProcPath(v)
		}
	}
	if res, err := execx.Run(ctx, runner, defaultDetectTimeout, cmdSystemctl, "show", "-p", "ExecStart", "--value", "--", unit); err == nil {
		if m := systemdExecPath.FindStringSubmatch(res.Stdout); m != nil {
			info.Exe = cleanProcPath(m[1])
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
		Pidfile: cleanProcPath(firstNonEmpty(vars["pidfile"], vars["PIDFILE"], suffixVar(vars, "_PIDFILE"))),
		Exe:     cleanProcPath(vars["command"]),
		User:    serviceUser(firstNonEmpty(vars["command_user"], userFromArgs(vars["start_stop_daemon_args"]), userFromArgs(text))),
	}
	if info.Pidfile == "" {
		info.Pidfile = cleanProcPath(firstResolvedArg(text, vars, openrcPidfileArg, openrcWritePIDArg))
	}
	if info.Exe == "" {
		info.Exe = cleanProcPath(firstResolvedArg(text, vars, openrcExecArg, openrcCommandAfterDash))
	}
	if command := cleanProcPath(vars["command"]); command != "" {
		info.Cmd = commandRegex(command)
	}
	runtime := detectOpenRCRuntimeProc(readFile, unit)
	if info.Pidfile == "" {
		info.Pidfile = runtime.Pidfile
	}
	if info.Exe == "" {
		info.Exe = runtime.Exe
	}
	if info.Cmd == "" {
		info.Cmd = runtime.Cmd
	}
	return info
}

func detectOpenRCRuntimeProc(readFile func(string) ([]byte, error), unit string) ProcInfo {
	data, err := readFile(filepath.Join("/run/openrc/daemons", unit, "001"))
	if err != nil {
		return ProcInfo{}
	}
	vars := openRCAssignments(string(data), unit)
	info := ProcInfo{
		Pidfile: cleanProcPath(vars["pidfile"]),
		Exe:     cleanProcPath(firstNonEmpty(vars["exec"], vars["argv_0"])),
	}
	if command := cleanProcPath(vars["argv_0"]); command != "" {
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
	// Iterate keys in sorted order: map ranging is randomized, so when more than
	// one variable ends with suffix (e.g. several *_PIDFILE) the chosen value
	// would otherwise vary between runs and a daemon could be matched to the
	// wrong pidfile non-reproducibly.
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		if value := vars[name]; strings.HasSuffix(name, suffix) && value != "" {
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

func cleanProcPath(s string) string {
	if strings.HasPrefix(s, "/") {
		clean := filepath.Clean(s)
		if clean == "/var/run" {
			return "/run"
		}
		if strings.HasPrefix(clean, "/var/run/") {
			return "/run/" + strings.TrimPrefix(clean, "/var/run/")
		}
		return clean
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
