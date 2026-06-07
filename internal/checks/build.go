package checks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/servicemgr"
)

// MetricReader returns a sampled metric for a scope (section 12). The daemon
// supplies the per-cycle sample; nil means no metric source (metric checks then
// report unavailable).
type MetricReader func(scope, name string) (metrics.Reading, bool)

// Deps are the host capabilities a built check set may need.
type Deps struct {
	Service        string
	DefaultTimeout time.Duration
	Runner         execx.Runner
	HTTPClient     *http.Client
	// Status queries the service's backend status, for `service` checks. When
	// nil, service checks are skipped with a warning.
	Status func(context.Context) (servicemgr.Status, error)
	// Metrics reads a sampled metric value, for `metric` checks.
	Metrics MetricReader
	// Processes reports the observed state (running/zombie/absent) of processes
	// matching an exe/user selector, for `process` checks.
	Processes func(exe, user string) string
	// DiskUsage reports filesystem usage for `disk` checks. Nil uses statfs.
	DiskUsage DiskUsageFunc
	// NetSampler observes a network interface for `net` checks. Nil uses /sys.
	NetSampler NetSamplerFunc
	// PingSampler probes a host via ICMP for `icmp` checks. Nil uses native ICMP.
	PingSampler PingSamplerFunc
	// SwapSampler reads system swap for `swap` checks. Nil reads /proc.
	SwapSampler SwapSamplerFunc
	// LoadSampler reads load averages for `load` checks. Nil reads /proc.
	LoadSampler LoadSamplerFunc
	// OomSampler reads the cumulative OOM-kill counter for `oom` checks. Nil reads
	// /proc/vmstat.
	OomSampler OomSamplerFunc
	// FdsSampler reads system file-descriptor usage for `fds` checks. Nil reads
	// /proc/sys/fs/file-nr.
	FdsSampler FdsSamplerFunc
	// ConntrackSampler reads the netfilter conntrack table for `conntrack` checks.
	// Nil reads /proc/sys/net/netfilter.
	ConntrackSampler ConntrackSamplerFunc
	// EntropySampler reads the kernel entropy pool for `entropy` checks. Nil reads
	// /proc/sys/kernel/random/entropy_avail.
	EntropySampler EntropySamplerFunc
}

// Build turns a checks/preflight/postflight section (a map keyed by check name)
// into runnable checks, skipping `enabled: false` entries and reporting unusable
// ones as warnings. Entries are built in name order for stable output.
func Build(section map[string]any, deps Deps) ([]Built, []string) {
	if section == nil {
		return nil, nil
	}

	runner := deps.Runner
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	client := deps.HTTPClient
	if client == nil {
		client = &http.Client{}
	}

	var built []Built
	var warnings []string
	for _, name := range sortedKeys(section) {
		entry, ok := section[name].(map[string]any)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("check %q is not a mapping", name))
			continue
		}
		if disabled(entry) {
			continue
		}

		b := base{
			name:    name,
			service: deps.Service,
			timeout: durationOr(entry["timeout"], deps.DefaultTimeout),
		}
		typ := asString(entry["type"])

		check, warn := buildCheck(typ, b, entry, runner, client, deps)
		if warn != "" {
			warnings = append(warnings, fmt.Sprintf("check %q: %s", name, warn))
			continue
		}
		built = append(built, Built{Check: check, Optional: asBool(entry["optional"])})
	}
	return built, warnings
}

func buildCheck(typ string, b base, entry map[string]any, runner execx.Runner, client *http.Client, deps Deps) (Check, string) {
	switch typ {
	case "tcp":
		port, ok := intField(entry["port"])
		if !ok {
			return nil, "tcp check requires a numeric port"
		}
		host := asString(entry["host"])
		if host == "" {
			host = "127.0.0.1"
		}
		return tcpCheck{base: b, host: host, port: port}, ""

	case "http":
		url := asString(entry["url"])
		if url == "" {
			return nil, "http check requires a url"
		}
		method := strings.ToUpper(asString(entry["method"]))
		if method == "" {
			method = http.MethodGet
		}
		expect, err := parseStatusMatcher(entry["expect_status"])
		if err != nil {
			return nil, "http check: " + err.Error()
		}
		return httpCheck{base: b, client: client, url: url, method: method, expect: expect}, ""

	case "command":
		argv := stringArray(entry["command"])
		if len(argv) == 0 {
			return nil, "command check requires a non-empty command array"
		}
		expect := 0
		if v, ok := intField(entry["expect_exit"]); ok {
			expect = v
		}
		return commandCheck{base: b, runner: runner, argv: argv, expectExit: expect}, ""

	case "service":
		expect := asString(entry["expect"])
		if expect == "" {
			return nil, "service check requires expect"
		}
		if deps.Status == nil {
			return nil, "service check needs backend detection, unavailable here"
		}
		return serviceCheck{base: b, expect: expect, status: deps.Status}, ""

	case "file_exists":
		path := asString(entry["path"])
		if path == "" {
			return nil, "file_exists check requires a path"
		}
		return fileExistsCheck{base: b, path: path}, ""

	case "binary":
		path := asString(entry["path"])
		if path == "" {
			return nil, "binary check requires a path"
		}
		return binaryCheck{base: b, path: path}, ""

	case "libraries":
		binary := asString(entry["binary"])
		if binary == "" {
			return nil, "libraries check requires a binary"
		}
		return librariesCheck{base: b, runner: runner, binary: binary}, ""

	case "metric":
		name := asString(entry["name"])
		if name == "" {
			return nil, "metric check requires a name"
		}
		scope := asString(entry["scope"])
		if scope == "" {
			scope = "service"
		}
		op := asString(entry["op"])
		if op == "" {
			return nil, "metric check requires an op"
		}
		if deps.Metrics == nil {
			return nil, "metric check needs a metric source, unavailable here"
		}
		return metricCheck{base: b, scope: scope, metric: name, op: op, value: scalarString(entry["value"]), source: deps.Metrics}, ""

	case "process":
		exe := asString(entry["exe"])
		user := asString(entry["user"])
		if exe == "" && user == "" {
			return nil, "process check requires exe and/or user"
		}
		if deps.Processes == nil {
			return nil, "process check needs process discovery, unavailable here"
		}
		expect := asString(entry["state"])
		if expect == "" {
			expect = "running"
		}
		return processCheck{base: b, exe: exe, user: user, expect: expect, observe: deps.Processes}, ""

	case "count":
		path := asString(entry["path"])
		if path == "" {
			return nil, "count check requires a path"
		}
		kind := asString(entry["of"])
		if kind == "" {
			kind = countAny
		}
		if !validCountKind(kind) {
			return nil, "count check `of` must be file, dir, symlink or any"
		}
		op := asString(entry["op"])
		if !validDiskOp(op) {
			return nil, "count check requires a valid op (>=, >, <=, <, ==, !=)"
		}
		val, err := strconv.ParseFloat(scalarString(entry["value"]), 64)
		if err != nil {
			return nil, "count check value must be numeric"
		}
		return countCheck{base: b, path: path, kind: kind, recursive: asBool(entry["recursive"]), op: op, value: val}, ""

	case "disk":
		path := asString(entry["path"])
		if path == "" {
			return nil, "disk check requires a path"
		}
		preds, err := parseDiskPreds(entry)
		if err != nil {
			return nil, "disk check: " + err.Error()
		}
		return diskCheck{base: b, path: path, preds: preds, usage: deps.DiskUsage}, ""

	case "net":
		iface := asString(entry["interface"])
		if iface == "" {
			return nil, "net check requires an interface"
		}
		metric := asString(entry["metric"])
		c := &netCheck{base: b, iface: iface, metric: metric, sampler: deps.NetSampler}
		switch metric {
		case "state":
			if exp := asString(entry["expect"]); exp != "" {
				if exp != "up" && exp != "down" {
					return nil, "net state expect must be up or down"
				}
				c.expect = exp
			} else if asString(entry["on"]) == "change" {
				c.onChange = true
			} else {
				return nil, "net state requires expect: up|down or on: change"
			}
		case "speed":
			if asString(entry["on"]) != "change" {
				return nil, "net speed requires on: change"
			}
			c.onChange = true
		case "errors":
			c.counters = stringArray(entry["counters"])
			if len(c.counters) == 0 {
				c.counters = []string{"rx_errors", "tx_errors"}
			}
			delta, ok := entry["delta"].(map[string]any)
			if !ok {
				return nil, "net errors requires a delta {op, value}"
			}
			op := asString(delta["op"])
			if !validDiskOp(op) {
				return nil, "net errors delta has an invalid op"
			}
			v, err := strconv.ParseFloat(scalarString(delta["value"]), 64)
			if err != nil {
				return nil, "net errors delta value must be numeric"
			}
			c.op, c.value = op, v
		default:
			return nil, "net check metric must be state, speed or errors"
		}
		return c, ""

	case "load":
		preds, err := parseLoadPreds(entry)
		if err != nil {
			return nil, "load check: " + err.Error()
		}
		return loadCheck{base: b, preds: preds, perCPU: asBool(entry["per_cpu"]), sampler: deps.LoadSampler}, ""

	case "fds":
		preds, err := parseFdsPreds(entry)
		if err != nil {
			return nil, "fds check: " + err.Error()
		}
		return fdsCheck{base: b, preds: preds, sampler: deps.FdsSampler}, ""

	case "conntrack":
		preds, err := parseConntrackPreds(entry)
		if err != nil {
			return nil, "conntrack check: " + err.Error()
		}
		return conntrackCheck{base: b, preds: preds, sampler: deps.ConntrackSampler}, ""

	case "entropy":
		op, value, err := parseEntropyThreshold(entry)
		if err != nil {
			return nil, "entropy check: " + err.Error()
		}
		return entropyCheck{base: b, op: op, value: value, sampler: deps.EntropySampler}, ""

	case "oom":
		// delta is optional; the default fires on any OOM kill (> 0).
		op, value := ">", 0.0
		if d, ok := entry["delta"].(map[string]any); ok {
			op = asString(d["op"])
			if !validDiskOp(op) {
				return nil, "oom delta has an invalid op"
			}
			v, err := strconv.ParseFloat(scalarString(d["value"]), 64)
			if err != nil {
				return nil, "oom delta value must be numeric"
			}
			value = v
		}
		return &oomCheck{base: b, op: op, value: value, sampler: deps.OomSampler}, ""

	case "swap":
		metric := asString(entry["metric"])
		c := &swapCheck{base: b, metric: metric, sampler: deps.SwapSampler}
		switch metric {
		case "usage":
			preds, err := parseSwapPreds(entry)
			if err != nil {
				return nil, "swap usage: " + err.Error()
			}
			c.preds = preds
		case "io":
			delta, ok := entry["delta"].(map[string]any)
			if !ok {
				return nil, "swap io requires a delta {op, value}"
			}
			op := asString(delta["op"])
			if !validDiskOp(op) {
				return nil, "swap io delta has an invalid op"
			}
			v, err := strconv.ParseFloat(scalarString(delta["value"]), 64)
			if err != nil {
				return nil, "swap io delta value must be numeric"
			}
			c.op, c.value = op, v
		default:
			return nil, "swap check metric must be usage or io"
		}
		return c, ""

	case "icmp":
		host := asString(entry["host"])
		if host == "" {
			return nil, "icmp check requires a host"
		}
		count := 3
		if v, ok := intField(entry["count"]); ok {
			if v <= 0 {
				return nil, "icmp count must be a positive integer"
			}
			count = v
		}
		metric := asString(entry["metric"])
		c := &icmpCheck{base: b, host: host, count: count, metric: metric, sampler: deps.PingSampler}
		switch metric {
		case "state":
			if exp := asString(entry["expect"]); exp != "" {
				if exp != "up" && exp != "down" {
					return nil, "icmp state expect must be up or down"
				}
				c.expect = exp
			} else if asString(entry["on"]) == "change" {
				c.onChange = true
			} else {
				return nil, "icmp state requires expect: up|down or on: change"
			}
		case "latency":
			if th, ok := entry["threshold"].(map[string]any); ok {
				op := asString(th["op"])
				if !validDiskOp(op) {
					return nil, "icmp latency threshold has an invalid op"
				}
				v, err := strconv.ParseFloat(scalarString(th["value"]), 64)
				if err != nil {
					return nil, "icmp latency threshold value must be numeric"
				}
				c.hasThreshold, c.op, c.value = true, op, v
			} else if ch, ok := entry["change"].(map[string]any); ok {
				d, err := strconv.ParseFloat(scalarString(ch["delta"]), 64)
				if err != nil {
					return nil, "icmp latency change delta must be numeric"
				}
				c.hasChange, c.delta = true, d
			} else {
				return nil, "icmp latency requires threshold {op, value} or change {delta}"
			}
		default:
			return nil, "icmp check metric must be state or latency"
		}
		return c, ""

	case "":
		return nil, "missing type"
	default:
		return nil, fmt.Sprintf("unsupported type %q", typ)
	}
}

// BuildInline builds a single check from an inline entry (type + fields), used
// by inline rule conditions (section 14). It returns an error rather than a
// warning so the caller can surface a malformed inline probe.
func BuildInline(name string, entry map[string]any, deps Deps) (Check, error) {
	runner := deps.Runner
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	client := deps.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	b := base{
		name:    name,
		service: deps.Service,
		timeout: durationOr(entry["timeout"], deps.DefaultTimeout),
	}
	check, warn := buildCheck(asString(entry["type"]), b, entry, runner, client, deps)
	if warn != "" {
		return nil, errors.New(warn)
	}
	return check, nil
}

// Outcome summarizes a preflight/postflight run.
type Outcome struct {
	OK      bool // every required check passed
	Results []Result
}

// Evaluate computes the outcome: a required (non-optional) failure makes it not
// OK; optional failures are warnings only (section 19).
func Evaluate(results []Result) Outcome {
	ok := true
	for _, r := range results {
		if !r.OK && !r.Optional {
			ok = false
		}
	}
	return Outcome{OK: ok, Results: results}
}

func disabled(entry map[string]any) bool {
	v, ok := entry["enabled"]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && !b
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// scalarString renders a YAML scalar as a string. A metric `value` is logically
// a string (section 14) but a bare number like `0` decodes as an int, so it must
// be stringified before parsing.
func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case uint64:
		return strconv.FormatUint(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func stringArray(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s := asString(e); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parseStatusMatcher parses an expect_status field: a single code, a class
// ("2xx"), or a list of either. Empty defaults to 200 (section 12).
func parseStatusMatcher(v any) (statusMatcher, error) {
	if v == nil {
		return statusMatcher{codes: []int{200}}, nil
	}
	var m statusMatcher
	var items []any
	if list, ok := v.([]any); ok {
		items = list
	} else {
		items = []any{v}
	}
	for _, item := range items {
		if n, ok := intField(item); ok {
			m.codes = append(m.codes, n)
			continue
		}
		s := strings.TrimSpace(asString(item))
		if len(s) == 3 && (s[1] == 'x' || s[1] == 'X') && (s[2] == 'x' || s[2] == 'X') && s[0] >= '1' && s[0] <= '5' {
			m.classes = append(m.classes, int(s[0]-'0'))
			continue
		}
		return statusMatcher{}, fmt.Errorf("invalid expect_status %q", s)
	}
	return m, nil
}

// intField parses a field that may be a YAML int, float or string (a resolved
// FlexInt, section 10).
func intField(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case uint64:
		return int(t), true
	case float64:
		return int(t), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		return n, err == nil
	default:
		return 0, false
	}
}

func durationOr(v any, fallback time.Duration) time.Duration {
	s := asString(v)
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}
