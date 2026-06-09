package app

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/rules"
	"sermo/internal/volume"
)

// BuildWatches resolves the global `watches` section into runnable Watches.
// Disabled or malformed entries are skipped with a warning (like BuildWorkers).
func BuildWatches(cfg *config.Config, deps Deps, defaultInterval time.Duration) ([]*Watch, []string) {
	raw, ok := cfg.Global.Raw["watches"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil, nil
	}

	var watches []*Watch
	var warnings []string
	for _, name := range sortedWatchNames(raw) {
		entry, ok := raw[name].(map[string]any)
		if !ok {
			warnings = append(warnings, "watch "+name+" is not a mapping")
			continue
		}
		if isDisabled(entry) {
			continue
		}

		checkEntry, ok := entry["check"].(map[string]any)
		if !ok {
			warnings = append(warnings, "watch "+name+": missing check")
			continue
		}

		interval := defaultInterval
		if d := durationField(entry["interval"]); d > 0 {
			interval = d
		}

		switch stringField(checkEntry["type"]) {
		case "net", "icmp", "swap":
			expanded, warns := buildMetricWatches(name, entry, checkEntry, deps, interval)
			watches = append(watches, expanded...)
			warnings = append(warnings, warns...)
		case "file":
			w, warn := buildFileWatch(name, entry, checkEntry, deps, interval)
			if warn != "" {
				warnings = append(warnings, warn)
				continue
			}
			watches = append(watches, w)
		case "process":
			w, warn := buildProcWatch(name, entry, checkEntry, deps, interval)
			if warn != "" {
				warnings = append(warnings, warn)
				continue
			}
			watches = append(watches, w)
		default:
			w, warn := buildSingleWatch(name, entry, checkEntry, deps, interval)
			if warn != "" {
				warnings = append(warnings, warn)
				continue
			}
			watches = append(watches, w)
		}
	}
	return watches, warnings
}

// buildSingleWatch builds the standard one-Watch-per-entry shape: an inline check
// plus the entry's top-level then.hook. It serves the host-resource checks
// (disk/load/…) and any single-shot service check (tcp/http/…) used as a watch.
// Health checks fire the hook on failure; condition checks on OK (threshold met).
func buildSingleWatch(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	typ := stringField(checkEntry["type"])
	check, err := checks.BuildInline(name, checkEntry, checks.Deps{
		DefaultTimeout: deps.DefaultTimeout,
		DiskUsage:      nil, // statfs default
	})
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	then, _ := entry["then"].(map[string]any)
	hook, names, err := parseActions(then)
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	expand, err := parseExpand(then, typ)
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	if len(hook.Command) == 0 && len(names) == 0 && expand == nil {
		return nil, "watch " + name + ": then requires a hook, notify and/or expand"
	}
	w := &Watch{
		Name:       name,
		CheckType:  typ,
		Check:      check,
		Window:     rules.Rule{For: parseForField(entry["for"]), Within: parseWithinField(entry["within"])},
		Hook:       hook,
		Notifiers:  resolveNotifiers(names, deps.Notifiers),
		Runner:     OSHookRunner{Runner: deps.ExecxRunner},
		Interval:   interval,
		FireOnFail: isHealthCheckType(typ),
		Now:        deps.Now,
		Emit:       deps.Emit,
	}
	if expand != nil {
		w.Expand = expand
		w.Policy = rules.ParsePolicy(entry)
		w.Expander = volume.Expander{Runner: deps.ExecxRunner}
	}
	return w, ""
}

// isHealthCheckType reports whether a check type's OK==true means "healthy", so a
// watch over it fires its hook on failure rather than on OK (the alert condition
// for disk/load/metric/count and the other threshold checks).
func isHealthCheckType(typ string) bool {
	switch typ {
	case "tcp", "ports", "http", "command", "service", "file_exists", "binary", "libraries":
		return true
	default:
		return false
	}
}

// buildMetricWatches expands one multi-metric watch entry (net/icmp) into one
// Watch per metric, each with its own check, window and hook. The per-metric
// check entry is the watch's base check fields plus metric:<key> plus the
// metric block's condition keys (everything except then/for/within). Builder-set
// keys (type, host/interface, count, metric) take precedence over the block.
func buildMetricWatches(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) ([]*Watch, []string) {
	metrics, ok := entry["metrics"].(map[string]any)
	if !ok || len(metrics) == 0 {
		return nil, []string{"watch " + name + ": " + stringField(checkEntry["type"]) + " check requires a non-empty metrics map"}
	}
	var out []*Watch
	var warns []string
	for _, key := range sortedWatchNames(metrics) {
		mEntry, ok := metrics[key].(map[string]any)
		if !ok {
			warns = append(warns, "watch "+name+".metrics."+key+": not a mapping")
			continue
		}
		ce := map[string]any{}
		for k, v := range mEntry { // condition keys
			switch k {
			case "then", "for", "within":
			default:
				ce[k] = v
			}
		}
		for k, v := range checkEntry { // base check fields win
			ce[k] = v
		}
		ce["metric"] = key

		check, err := checks.BuildInline(name, ce, checks.Deps{DefaultTimeout: deps.DefaultTimeout})
		if err != nil {
			warns = append(warns, "watch "+name+".metrics."+key+": "+err.Error())
			continue
		}
		hook, names, err := parseThen(mEntry)
		if err != nil {
			warns = append(warns, "watch "+name+".metrics."+key+": "+err.Error())
			continue
		}
		out = append(out, &Watch{
			Name:      name,
			CheckType: stringField(checkEntry["type"]),
			Check:     check,
			Window:    rules.Rule{For: parseForField(mEntry["for"]), Within: parseWithinField(mEntry["within"])},
			Hook:      hook,
			Notifiers: resolveNotifiers(names, deps.Notifiers),
			Runner:    OSHookRunner{Runner: deps.ExecxRunner},
			Interval:  interval,
			Now:       deps.Now,
			Emit:      deps.Emit,
		})
	}
	return out, warns
}

// buildFileWatch builds a stateful file watch: a fileWatcher (its own per-path
// baseline, conditions and hook) wired into a Watch through Watch.Cycle so it can
// fire one hook per change. The Watch's check/window fields are unused.
func buildFileWatch(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	if stringField(checkEntry["path"]) == "" {
		return nil, "watch " + name + ": file check requires a path"
	}
	cond, err := parseFileCond(checkEntry)
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	hook, names, err := parseThen(entry)
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	fw := &fileWatcher{
		name:      name,
		path:      stringField(checkEntry["path"]),
		recursive: boolField(checkEntry["recursive"]),
		cond:      cond,
		hook:      hook,
		notifiers: resolveNotifiers(names, deps.Notifiers),
		runner:    OSHookRunner{Runner: deps.ExecxRunner},
		emit:      deps.Emit,
	}
	return &Watch{
		Name:      name,
		CheckType: "file",
		Interval:  interval,
		Now:       deps.Now,
		Emit:      deps.Emit,
		Cycle:     fw.runCycle,
	}, ""
}

// buildProcWatch builds a stateful process watch: a procWatcher (its own per-PID
// state, conditions and hook) wired into a Watch through Watch.Cycle so it can
// fire one hook per matching PID.
func buildProcWatch(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	pname := stringField(checkEntry["name"])
	if pname == "" {
		return nil, "watch " + name + ": process check requires a name"
	}
	cond, err := parseProcCond(checkEntry)
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	hook, names, err := parseThen(entry)
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	pw := &procWatcher{
		name:      name,
		match:     ProcMatch{Name: pname, User: stringField(checkEntry["user"])},
		cond:      cond,
		hook:      hook,
		notifiers: resolveNotifiers(names, deps.Notifiers),
		runner:    OSHookRunner{Runner: deps.ExecxRunner},
		now:       deps.Now,
		emit:      deps.Emit,
		sampler:   deps.ProcSampler, // nil -> osProcSampler at run time
	}
	return &Watch{
		Name:      name,
		CheckType: "process",
		Interval:  interval,
		Now:       deps.Now,
		Emit:      deps.Emit,
		Cycle:     pw.runCycle,
	}, ""
}

// parseProcCond reads the for/cpu/memory/io conditions from a process check
// entry. At least one must be present.
func parseProcCond(check map[string]any) (procCond, error) {
	var c procCond
	if _, present := check["for"]; present {
		d := durationField(check["for"])
		if d <= 0 {
			return c, fmt.Errorf("process for must be a positive duration")
		}
		c.minAge = d
	}
	type thr struct {
		key string
		op  *string
		val *float64
	}
	for _, t := range []thr{
		{"cpu", &c.cpuOp, &c.cpuValue},
		{"memory", &c.memOp, &c.memValue},
		{"io", &c.ioOp, &c.ioValue},
	} {
		m, ok := check[t.key].(map[string]any)
		if !ok {
			continue
		}
		op := stringField(m["op"])
		if !validThresholdOp(op) {
			return c, fmt.Errorf("process %s requires a valid op (>=, >, <=, <, ==, !=)", t.key)
		}
		v, ok := floatField(m["value"])
		if !ok {
			return c, fmt.Errorf("process %s value must be numeric", t.key)
		}
		*t.op, *t.val = op, v
	}
	if v, present := check["gone"]; present {
		b, ok := v.(bool)
		if !ok {
			return c, fmt.Errorf("process gone must be a boolean")
		}
		c.onGone = b
	}
	if !c.any() {
		return c, fmt.Errorf("process check requires at least one of for, cpu, memory, io, gone")
	}
	return c, nil
}

// parseFileCond reads the size/permissions/owner/existence conditions from a file
// check entry. At least one must be present.
func parseFileCond(check map[string]any) (fileCond, error) {
	var c fileCond
	if sz, ok := check["size"].(map[string]any); ok {
		if stringField(sz["on"]) == "change" {
			c.sizeChange = true
		} else {
			op := stringField(sz["op"])
			if !validThresholdOp(op) {
				return c, fmt.Errorf("file size requires on: change or {op, value}")
			}
			v, ok := floatField(sz["value"])
			if !ok {
				return c, fmt.Errorf("file size value must be numeric")
			}
			c.sizeOp, c.sizeValue = op, v
		}
	}
	if p, ok := check["permissions"].(map[string]any); ok {
		if stringField(p["on"]) != "change" {
			return c, fmt.Errorf("file permissions requires on: change")
		}
		c.permChange = true
	}
	if o, ok := check["owner"].(map[string]any); ok {
		if stringField(o["on"]) != "change" {
			return c, fmt.Errorf("file owner requires on: change")
		}
		c.ownerChange = true
	}
	if e, ok := check["existence"].(map[string]any); ok {
		if stringField(e["on"]) != "delete" {
			return c, fmt.Errorf("file existence requires on: delete")
		}
		c.onDelete = true
	}
	if !c.any() {
		return c, fmt.Errorf("file check requires at least one of size, permissions, owner, existence")
	}
	return c, nil
}

// parseThen reads a `then` block into an optional hook and an optional list of
// notifier names. A then block must declare a hook and/or at least one notifier.
func parseThen(entry map[string]any) (HookSpec, []string, error) {
	then, ok := entry["then"].(map[string]any)
	if !ok {
		return HookSpec{}, nil, fmt.Errorf("missing then")
	}
	hook, names, err := parseActions(then)
	if err != nil {
		return HookSpec{}, nil, err
	}
	if len(hook.Command) == 0 && len(names) == 0 {
		return HookSpec{}, nil, fmt.Errorf("then requires a hook and/or notify")
	}
	return hook, names, nil
}

// parseActions reads the hook and notify targets from a `then` block without
// requiring that at least one is present (the caller enforces that, so an
// expand-only `then` is allowed). It errors only on a malformed hook.
func parseActions(then map[string]any) (HookSpec, []string, error) {
	var hook HookSpec
	if h, ok := then["hook"].(map[string]any); ok {
		cmd := stringArray(h["command"])
		if len(cmd) == 0 {
			return HookSpec{}, nil, fmt.Errorf("hook requires a non-empty command")
		}
		hook = HookSpec{Command: cmd, Timeout: durationField(h["timeout"])}
	}
	return hook, stringArray(then["notify"]), nil
}

// parseExpand reads a `then.expand` disk-expansion action. It is only valid on a
// disk watch, since the action grows the volume backing the checked path.
func parseExpand(then map[string]any, checkType string) (*ExpandSpec, error) {
	raw, ok := then["expand"].(map[string]any)
	if !ok {
		return nil, nil
	}
	if checkType != "disk" {
		return nil, fmt.Errorf("then.expand is only valid on a disk watch, not %q", checkType)
	}
	by := stringField(raw["by"])
	if by == "" {
		return nil, fmt.Errorf("then.expand requires a `by` size (e.g. 5G)")
	}
	n, err := parseSize(by)
	if err != nil {
		return nil, fmt.Errorf("then.expand by: %w", err)
	}
	if n <= 0 {
		return nil, fmt.Errorf("then.expand by must be positive")
	}
	return &ExpandSpec{By: n}, nil
}

// resolveNotifiers maps notifier names to the configured notifiers, skipping
// unknown names (config validation reports those; a build-time miss means the
// notifier itself failed to build, already warned by notify.Build).
func resolveNotifiers(names []string, reg map[string]notify.Notifier) []notify.Notifier {
	out := make([]notify.Notifier, 0, len(names))
	for _, n := range names {
		if nt, ok := reg[n]; ok {
			out = append(out, nt)
		}
	}
	return out
}

func parseForField(v any) *rules.ForWindow {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return &rules.ForWindow{Cycles: intField(m["cycles"])}
}

func parseWithinField(v any) *rules.WithinWindow {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return &rules.WithinWindow{Cycles: intField(m["cycles"]), MinMatches: intField(m["min_matches"])}
}

func sortedWatchNames(m map[string]any) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func stringArray(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func durationField(v any) time.Duration {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

func intField(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case uint64:
		return int(t)
	case float64:
		return int(t)
	default:
		return 0
	}
}

func boolField(v any) bool {
	b, _ := v.(bool)
	return b
}

// floatField reads a numeric field that may decode as a YAML int, float or
// string, reporting whether it parsed.
func floatField(v any) (float64, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint64:
		return float64(t), true
	case float64:
		return t, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func stringField(v any) string {
	s, _ := v.(string)
	return s
}
