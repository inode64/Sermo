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

const (
	openRCVarArgv0               = "argv_0"
	openRCVarChroot              = "CHROOT"
	openRCVarCommand             = "command"
	openRCVarCommandUser         = "command_user"
	openRCVarExec                = "exec"
	openRCVarPidfile             = "pidfile"
	openRCVarPidfileUpper        = "PIDFILE"
	openRCVarPidfileSuffix       = "_PIDFILE"
	openRCVarPrefix              = "RC_PREFIX"
	openRCVarStartStopDaemonArgs = "start_stop_daemon_args"
	openRCVarSvcName             = "RC_SVCNAME"
	openRCVarSvcNameCompat       = "SVCNAME"

	openRCSuffixTrimDir = "%/"

	shellKeywordElse = "else"
	shellKeywordFi   = "fi"
	shellIfPrefix    = "if "
	shellThenSuffix  = "then"

	shellCommandSubstitutionPrefix = "$("
	shellCommandSubstitutionQuote  = "`"
	shellConditionAnd              = "&&"
	shellGlobAny                   = "*"
	shellLineContinuation          = "\\"
	shellNoOpPrefix                = ":"
	shellStatementTerminator       = ";"
	shellUserGroupSeparator        = ":"
	shellVariableMarker            = "$"
	shellVariablePrefix            = "${"
	shellVariableSuffix            = "}"

	legacyRunDir  = "/var/run"
	runtimeRunDir = "/run"

	openRCMaxValueExpansionPasses = 8

	shellClosingDelimiterBytes = 1
	shellDefaultOperatorBytes  = 2
	shellFirstByteIndex        = 0
	shellNextByteOffset        = 1
	shellQuoteBodyStart        = 1
	shellQuoteMinBytes         = 2
	shellVariablePrefixBytes   = len(shellVariablePrefix)

	systemdExecPathGroup        = 1
	openRCAssignNameGroup       = 1
	openRCAssignValueGroup      = 2
	openRCConditionLeftGroup    = 1
	openRCConditionRightGroup   = 2
	openRCValueExprGroup        = 1
	openRCUserArgGroup          = 1
	openRCVarRefNameGroup       = 1
	openRCVarRefSuffixTrimGroup = 2
	openRCVarRefPrefixTrimGroup = 3
	openRCVarRefBareNameGroup   = 4

	shellCloseBraceByte       = '}'
	shellCommentByte          = '#'
	shellDefaultAssignByte    = '='
	shellDefaultMinusByte     = '-'
	shellDefaultSeparatorByte = ':'
	shellDoubleQuoteByte      = '"'
	shellOpenBraceByte        = '{'
	shellSingleQuoteByte      = '\''
	shellVariableByte         = '$'
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
	if res, err := runSystemctlShow(ctx, runner, defaultDetectTimeout, systemctlPropertyPIDFile, unit); err == nil {
		if v := strings.TrimSpace(res.Stdout); v != "" {
			info.Pidfile = cleanProcPath(v)
		}
	}
	if res, err := runSystemctlShow(ctx, runner, defaultDetectTimeout, systemctlPropertyExecStart, unit); err == nil {
		if m := systemdExecPath.FindStringSubmatch(res.Stdout); m != nil {
			info.Exe = cleanProcPath(m[systemdExecPathGroup])
		}
	}
	return info
}

func detectOpenRCProc(readFile func(string) ([]byte, error), unit string) ProcInfo {
	var blob strings.Builder
	for _, path := range []string{filepath.Join(openRCInitDir, unit), filepath.Join(openRCConfDir, unit)} {
		if data, err := readFile(path); err == nil {
			blob.Write(data)
			blob.WriteByte(serviceOutputLineByte)
		}
	}
	text := blob.String()
	vars := openRCAssignments(text, unit)
	info := ProcInfo{
		Pidfile: cleanProcPath(firstNonEmpty(vars[openRCVarPidfile], vars[openRCVarPidfileUpper], suffixVar(vars, openRCVarPidfileSuffix))),
		Exe:     cleanProcPath(vars[openRCVarCommand]),
		User:    serviceUser(firstNonEmpty(vars[openRCVarCommandUser], userFromArgs(vars[openRCVarStartStopDaemonArgs]), userFromArgs(text))),
	}
	if info.Pidfile == "" {
		info.Pidfile = cleanProcPath(firstResolvedArg(text, vars, openrcPidfileArg, openrcWritePIDArg))
	}
	if info.Exe == "" {
		info.Exe = cleanProcPath(firstResolvedArg(text, vars, openrcExecArg, openrcCommandAfterDash))
	}
	if command := cleanProcPath(vars[openRCVarCommand]); command != "" {
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
	data, err := readFile(filepath.Join(openRCDaemonsDir, unit, "001"))
	if err != nil {
		return ProcInfo{}
	}
	vars := openRCAssignments(string(data), unit)
	info := ProcInfo{
		Pidfile: cleanProcPath(vars[openRCVarPidfile]),
		Exe:     cleanProcPath(firstNonEmpty(vars[openRCVarExec], vars[openRCVarArgv0])),
	}
	if command := cleanProcPath(vars[openRCVarArgv0]); command != "" {
		info.Cmd = commandRegex(command)
	}
	return info
}

func openRCAssignments(text, unit string) map[string]string {
	vars := map[string]string{
		openRCVarChroot:        "",
		openRCVarPrefix:        "",
		openRCVarSvcName:       unit,
		openRCVarSvcNameCompat: unit,
	}
	active := true
	var stack []openRCBranch
	for line := range strings.SplitSeq(text, serviceOutputLineSeparator) {
		line = strings.TrimSpace(line)
		if b, ok := openRCIfBranch(line, active, vars); ok {
			stack = append(stack, b)
			if b.known {
				active = b.parent && b.cond
			}
			continue
		}
		if line == shellKeywordElse && len(stack) > 0 {
			b := stack[len(stack)-1]
			if b.known {
				active = b.parent && !b.cond
			} else {
				active = b.parent
			}
			continue
		}
		if line == shellKeywordFi && len(stack) > 0 {
			b := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			active = b.parent
			continue
		}
		if !active {
			continue
		}
		if after, ok := strings.CutPrefix(line, shellNoOpPrefix); ok {
			expr := strings.TrimSpace(after)
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
			name := m[openRCAssignNameGroup]
			value, ok := resolveOpenRCValue(m[openRCAssignValueGroup], vars)
			if ok {
				vars[name] = value
			}
		}
	}
	return vars
}

func openRCIfBranch(line string, active bool, vars map[string]string) (openRCBranch, bool) {
	if !strings.HasPrefix(line, shellIfPrefix) || !strings.HasSuffix(line, shellThenSuffix) {
		return openRCBranch{}, false
	}
	expr := strings.TrimSpace(strings.TrimPrefix(line, shellIfPrefix))
	expr = strings.TrimSpace(strings.TrimSuffix(expr, shellThenSuffix))
	expr = strings.TrimSpace(strings.TrimSuffix(expr, shellStatementTerminator))
	cond, known := evalOpenRCCondition(expr, vars)
	return openRCBranch{parent: active, cond: cond, known: known}, true
}

func evalOpenRCCondition(expr string, vars map[string]string) (bool, bool) {
	result := true
	for part := range strings.SplitSeq(expr, shellConditionAnd) {
		part = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part), shellLineContinuation))
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
		value, ok := resolveOpenRCValue(m[openRCConditionLeftGroup], vars)
		return value != "", ok
	}
	if m := openrcNotEqualCond.FindStringSubmatch(term); m != nil {
		left, ok := resolveOpenRCValue(m[openRCConditionLeftGroup], vars)
		if !ok {
			return false, false
		}
		right := shellWord(m[openRCConditionRightGroup])
		return left != right, true
	}
	return false, false
}

func resolveOpenRCValue(raw string, vars map[string]string) (string, bool) {
	s := strings.TrimSpace(stripShellComment(raw))
	if strings.Contains(s, shellCommandSubstitutionPrefix) || strings.Contains(s, shellCommandSubstitutionQuote) {
		return "", false
	}
	s = shellWord(s)
	for range openRCMaxValueExpansionPasses {
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
			name := m[openRCVarRefNameGroup]
			if name == "" {
				name = m[openRCVarRefBareNameGroup]
			}
			value, ok := vars[name]
			if !ok {
				unresolved = true
				return match
			}
			if m[openRCVarRefSuffixTrimGroup] == openRCSuffixTrimDir {
				value = strings.TrimSuffix(value, "/")
			}
			if m[openRCVarRefPrefixTrimGroup] != "" {
				value = trimShellPrefixPattern(value, m[openRCVarRefPrefixTrimGroup])
			}
			return value
		})
		if unresolved {
			return "", false
		}
		if strings.Contains(next, shellVariableMarker) {
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
	if after, ok := strings.CutPrefix(pattern, shellGlobAny); ok {
		suffix := after
		if suffix == "" {
			return value
		}
		if _, after, ok := strings.Cut(value, suffix); ok {
			return after
		}
		return value
	}
	return strings.TrimPrefix(value, pattern)
}

func defaultExpr(s string) (name, def string, ok bool) {
	if !strings.HasPrefix(s, shellVariablePrefix) || !strings.HasSuffix(s, shellVariableSuffix) {
		return "", "", false
	}
	body := s[shellVariablePrefixBytes : len(s)-shellClosingDelimiterBytes]
	depth := 0
	for i := shellFirstByteIndex; i < len(body)-shellNextByteOffset; i++ {
		next := body[i+shellNextByteOffset]
		if body[i] == shellVariableByte && next == shellOpenBraceByte {
			depth++
			i++
			continue
		}
		if body[i] == shellCloseBraceByte && depth > 0 {
			depth--
			continue
		}
		if depth == 0 && body[i] == shellDefaultSeparatorByte && (next == shellDefaultMinusByte || next == shellDefaultAssignByte) {
			name = strings.TrimSpace(body[:i])
			def = strings.TrimSpace(body[i+shellDefaultOperatorBytes:])
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
		case shellSingleQuoteByte:
			if !inDouble {
				inSingle = !inSingle
			}
		case shellDoubleQuoteByte:
			if !inSingle {
				inDouble = !inDouble
			}
		case shellCommentByte:
			if !inSingle && !inDouble && (i == shellFirstByteIndex || unicode.IsSpace(prev)) {
				return s[:i]
			}
		}
		prev = r
	}
	return s
}

func shellWord(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= shellQuoteMinBytes {
		last := len(s) - shellClosingDelimiterBytes
		if (s[shellFirstByteIndex] == shellDoubleQuoteByte && s[last] == shellDoubleQuoteByte) ||
			(s[shellFirstByteIndex] == shellSingleQuoteByte && s[last] == shellSingleQuoteByte) {
			return strings.TrimSpace(s[shellQuoteBodyStart:last])
		}
	}
	return s
}

func firstResolvedArg(text string, vars map[string]string, exprs ...*regexp.Regexp) string {
	for _, expr := range exprs {
		for _, m := range expr.FindAllStringSubmatch(text, -1) {
			if value, ok := resolveOpenRCValue(m[openRCValueExprGroup], vars); ok && value != "" {
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
		return shellWord(m[openRCUserArgGroup])
	}
	return ""
}

func serviceUser(s string) string {
	s = shellWord(strings.TrimSpace(s))
	user, _, _ := strings.Cut(s, shellUserGroupSeparator)
	return strings.TrimSpace(user)
}

func commandRegex(command string) string {
	return `(^|[[:space:]])` + regexp.QuoteMeta(command) + `($|[[:space:]])`
}

func cleanProcPath(s string) string {
	if strings.HasPrefix(s, "/") {
		clean := filepath.Clean(s)
		if clean == legacyRunDir {
			return runtimeRunDir
		}
		if after, ok := strings.CutPrefix(clean, legacyRunDir+"/"); ok {
			return runtimeRunDir + "/" + after
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
