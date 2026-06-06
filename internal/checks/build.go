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
		expect := 200
		if v, ok := intField(entry["expect_status"]); ok {
			expect = v
		}
		return httpCheck{base: b, client: client, url: url, method: method, expectStatus: expect}, ""

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
