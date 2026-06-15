// Package web serves a small read-and-act dashboard for the daemon: it lists the
// monitored services with their status and lets an operator monitor/unmonitor and
// start/stop/restart them. It is deliberately minimal and depends on the daemon
// only through the Backend interface, so it stays decoupled and testable.
//
// Access is optional HTTP Basic auth with admin (read+act) and guest (read-only)
// roles; state-changing POST requests also require an X-Sermo-CSRF header. When
// no passwords are configured the UI is open — bind to a trusted interface
// (loopback by default) or set passwords / front it with an authenticating reverse
// proxy. GET /livez and GET /readyz are always public for health probes.
package web

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
)

//go:embed index.html
var assets embed.FS

// Service is the web view of one configured service. Services with `enabled: false`
// in their configuration are still listed (with Enabled=false) so operators can
// see the full fleet and know what to activate by editing config + reloading.
type Service struct {
	Name             string   `json:"name"`
	DisplayName      string   `json:"display_name"`
	Category         string   `json:"category,omitempty"`
	Backend          string   `json:"backend"`
	Unit             string   `json:"unit"`
	State            string   `json:"state"`
	Status           string   `json:"status"`
	Interval         string   `json:"interval,omitempty"` // resolved per-service cycle cadence (own interval or engine default)
	Enabled          bool     `json:"enabled"`            // false when service document has `enabled: false`
	Monitored        bool     `json:"monitored"`
	MonitorSource    string   `json:"monitor_source,omitempty"`     // cli | web | config | daemon
	MonitorChangedAt string   `json:"monitor_changed_at,omitempty"` // RFC3339 when monitoring state last changed
	CheckHealth      string   `json:"check_health,omitempty"`       // ok | failing | unknown | paused | disabled
	ChecksFailing    int      `json:"checks_failing,omitempty"`     // required checks currently failing
	ActiveLocks      []string `json:"active_locks,omitempty"`       // named runtime locks blocking actions
	PolicyCooldown   string   `json:"policy_cooldown,omitempty"`    // resolved automatic remediation cooldown
	RemediationState string   `json:"remediation_state,omitempty"`  // eligible | cooldown | rate limit | paused | pending | disabled
	NextEligibleAt   string   `json:"next_eligible_at,omitempty"`   // RFC3339 when automatic remediation is next eligible
	LastEvent        *Event   `json:"last_event,omitempty"`         // newest service event, when retained

	// Current process-tree runtime summary. These fields intentionally mirror
	// ProcessTotals so the service list and detail expansion use the same
	// semantics: matched processes plus their child/descendant processes.
	StartedAt     string  `json:"started_at,omitempty"` // oldest discovered process start time, RFC3339
	Uptime        string  `json:"uptime,omitempty"`     // display-ready age of StartedAt
	UptimeSeconds int64   `json:"uptime_seconds,omitempty"`
	ProcessCount  int     `json:"process_count,omitempty"`
	RSS           int64   `json:"rss,omitempty"`
	IORead        int64   `json:"io_read,omitempty"`  // cumulative disk read bytes
	IOWrite       int64   `json:"io_write,omitempty"` // cumulative disk write bytes
	FDs           int64   `json:"fds,omitempty"`
	Threads       int64   `json:"threads,omitempty"`
	CPU           float64 `json:"cpu,omitempty"`        // live CPU %, all host CPUs
	CPUThread     float64 `json:"cpu_thread,omitempty"` // busiest process, single-core normalized
	NumCPU        int     `json:"num_cpu,omitempty"`
	CPUReady      bool    `json:"cpu_ready,omitempty"`
}

// Application is a view of one installed application (a catalog app daemon) for
// the dashboard: its name, version and where its binary lives. It mirrors the
// sermoctl `apps` report so both surfaces agree.
type Application struct {
	Name         string      `json:"name"`
	DisplayName  string      `json:"display_name"`
	Category     string      `json:"category,omitempty"`
	Binary       string      `json:"binary"`                // resolved binary path (file location)
	Permissions  string      `json:"permissions,omitempty"` // binary mode, e.g. "-rwxr-xr-x (0755)"
	User         string      `json:"user,omitempty"`        // owner username of the binary
	Group        string      `json:"group,omitempty"`       // owner group of the binary
	Version      string      `json:"version"`               // raw first line of the version command
	VersionShort string      `json:"version_short"`         // numeric version, at most the patchlevel
	Status       string      `json:"status"`                // ok, or an error description
	SLA          []SLAWindow `json:"sla,omitempty"`         // service SLA when this app maps to a monitored service
}

// Watch is a view of a host watch for the dashboard (when services=0
// the watches section is the main thing to show). Enriched with useful
// runtime/config info for operators.
type Watch struct {
	Name             string           `json:"name"`
	DisplayName      string           `json:"display_name,omitempty"`
	CheckType        string           `json:"check_type,omitempty"`
	Summary          string           `json:"summary,omitempty"`
	Interval         string           `json:"interval,omitempty"`
	State            string           `json:"state"`
	Enabled          bool             `json:"enabled"`
	Monitor          string           `json:"monitor,omitempty"` // enabled | disabled | previous
	Monitored        bool             `json:"monitored"`
	MonitorSource    string           `json:"monitor_source,omitempty"`
	MonitorChangedAt string           `json:"monitor_changed_at,omitempty"`
	FireOnFail       bool             `json:"fire_on_fail"` // true = fires when check fails (e.g. health checks); false = fires on condition (e.g. load/storage)
	HasHook          bool             `json:"has_hook"`
	HookCommand      []string         `json:"hook_command,omitempty"`
	Notifiers        []string         `json:"notifiers,omitempty"`
	NotifierCount    int              `json:"notifier_count"`
	DryRun           bool             `json:"dry_run"`
	Conditions       []WatchCondition `json:"conditions,omitempty"`
	Disk             *DiskWatchInfo   `json:"disk,omitempty"`
	Swap             *SwapWatchInfo   `json:"swap,omitempty"`
	Meter            *WatchMeter      `json:"meter,omitempty"`
	Readings         []WatchReading   `json:"readings,omitempty"`
	Expand           *WatchExpand     `json:"expand,omitempty"`
	LastActivity     string           `json:"last_activity,omitempty"` // RFC3339 of last hook/notify for this watch, if any
	LastActivityKind string           `json:"last_activity_kind,omitempty"`
}

// WatchCondition is one configured watch predicate, rendered in the WebUI.
type WatchCondition struct {
	Field string `json:"field"`
	Op    string `json:"op,omitempty"`
	Value string `json:"value,omitempty"`
}

// WatchReading is one current host-watch observation rendered in the dashboard
// for checks that do not naturally fit the volume/meter views.
type WatchReading struct {
	Field string `json:"field"`
	Label string `json:"label,omitempty"`
	Value string `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

// WatchExpand is the configured manual/automatic storage growth action.
type WatchExpand struct {
	ByBytes int64 `json:"by_bytes"`
}

// SwapWatchInfo is live swap usage for a swap host watch, mirroring the
// volume-style used/free rendering of DiskWatchInfo.
type SwapWatchInfo struct {
	TotalBytes uint64  `json:"total_bytes"`
	UsedBytes  uint64  `json:"used_bytes"`
	FreeBytes  uint64  `json:"free_bytes"`
	UsedPct    float64 `json:"used_pct"`
}

// WatchMeter is a generic 0-100% usage gauge for a host watch that has a
// natural capacity (memory, load, fds, pids, conntrack), giving those watches the same
// progress-bar rendering as swap/disk. UsedPct always drives the bar; the
// kind-specific fields below carry the human-readable detail (bytes for
// memory, counts for fds/pids/conntrack, raw load vs CPU count for load).
type WatchMeter struct {
	Kind    string  `json:"kind"` // memory | load | fds | pids | conntrack
	UsedPct float64 `json:"used_pct"`
	// Memory: byte capacity.
	TotalBytes uint64 `json:"total_bytes,omitempty"`
	UsedBytes  uint64 `json:"used_bytes,omitempty"`
	FreeBytes  uint64 `json:"free_bytes,omitempty"`
	// fds / pids: count vs kernel limit.
	Count uint64 `json:"count,omitempty"`
	Max   uint64 `json:"max,omitempty"`
	// load: 1-minute load average vs logical CPU count.
	Load   float64 `json:"load,omitempty"`
	NumCPU int     `json:"num_cpu,omitempty"`
}

// DiskWatchInfo is live filesystem data for a storage host watch.
type DiskWatchInfo struct {
	Path             string   `json:"path"`
	Mounted          bool     `json:"mounted"`
	MountPoint       string   `json:"mount_point,omitempty"`
	Device           string   `json:"device,omitempty"`
	FileSystem       string   `json:"filesystem,omitempty"`
	Options          []string `json:"options,omitempty"`
	TotalBytes       uint64   `json:"total_bytes,omitempty"`
	UsedBytes        uint64   `json:"used_bytes,omitempty"`
	FreeBytes        uint64   `json:"free_bytes,omitempty"`
	UsedPct          float64  `json:"used_pct,omitempty"`
	FreePct          float64  `json:"free_pct,omitempty"`
	InodesTotal      uint64   `json:"inodes_total,omitempty"`
	InodesFree       uint64   `json:"inodes_free,omitempty"`
	InodesUsedPct    float64  `json:"inodes_used_pct,omitempty"`
	InodesFreePct    float64  `json:"inodes_free_pct,omitempty"`
	SampleError      string   `json:"sample_error,omitempty"`
	MountSampleError string   `json:"mount_sample_error,omitempty"`
}

// Notifier is a configured notification target referenced by watches.
type Notifier struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

// DaemonInfo provides a summary of the running daemon configuration
// (engine settings and paths). Useful for operators to see effective
// behavior without reading the config file.
type DaemonInfo struct {
	Backend               string `json:"backend,omitempty"`
	Hostname              string `json:"hostname,omitempty"`
	OS                    string `json:"os,omitempty"`
	HostUptime            string `json:"host_uptime,omitempty"`         // display-ready uptime of the host/server since boot
	HostUptimeSeconds     int64  `json:"host_uptime_seconds,omitempty"` // host/server uptime in whole seconds
	ConfigPath            string `json:"config_path,omitempty"`
	RuntimeDir            string `json:"runtime_dir,omitempty"`
	StateDir              string `json:"state_dir,omitempty"`
	Interval              string `json:"interval"`
	MaxParallelChecks     int    `json:"max_parallel_checks"`
	MaxParallelOperations int    `json:"max_parallel_operations"`
	DefaultTimeout        string `json:"default_timeout"`
	OperationTimeout      string `json:"operation_timeout"`
	StartupDelay          string `json:"startup_delay"`
}

// DaemonRuntime is the latest resource sample for the running sermod process.
type DaemonRuntime struct {
	At            string  `json:"at,omitempty"` // RFC3339
	PID           int     `json:"pid"`
	RSS           int64   `json:"rss,omitempty"` // resident memory, bytes
	MemoryPercent float64 `json:"memory_percent,omitempty"`
	CPU           float64 `json:"cpu,omitempty"` // % of all host CPUs
	CPUReady      bool    `json:"cpu_ready"`
	IORead        float64 `json:"io_read,omitempty"`  // bytes/s
	IOWrite       float64 `json:"io_write,omitempty"` // bytes/s
	IO            float64 `json:"io,omitempty"`       // bytes/s read+write
	IOReady       bool    `json:"io_ready"`
	FDs           int64   `json:"fds,omitempty"`
	Threads       int64   `json:"threads,omitempty"`
	NumCPU        int     `json:"num_cpu,omitempty"`
}

// DaemonMetrics contains current sermod process indicators and the historical
// CPU, memory and IO series for the selected window.
type DaemonMetrics struct {
	Since   string        `json:"since"`
	Current DaemonRuntime `json:"current"`
	CPU     MetricSeries  `json:"cpu"`
	Memory  MetricSeries  `json:"memory"`
	IO      MetricSeries  `json:"io"`
}

// ServiceRuntime is the current process-tree runtime sample for one service.
type ServiceRuntime struct {
	At string `json:"at,omitempty"` // RFC3339
	ProcessTotals
	StartedAt     string  `json:"started_at,omitempty"` // oldest discovered process start time, RFC3339
	Uptime        string  `json:"uptime,omitempty"`
	UptimeSeconds int64   `json:"uptime_seconds,omitempty"`
	IOReadRate    float64 `json:"io_read_rate,omitempty"`  // bytes/s
	IOWriteRate   float64 `json:"io_write_rate,omitempty"` // bytes/s
	IORate        float64 `json:"io_rate,omitempty"`       // bytes/s read+write
	IOReady       bool    `json:"io_ready"`
}

// ServiceRuntimeMetrics contains current and historical CPU, memory and IO for
// one service's process tree.
type ServiceRuntimeMetrics struct {
	Since   string         `json:"since"`
	Current ServiceRuntime `json:"current"`
	CPU     MetricSeries   `json:"cpu"`
	Memory  MetricSeries   `json:"memory"`
	IO      MetricSeries   `json:"io"`
}

// ActivitySummary is a lightweight rollup of recent events for the dashboard.
// It helps operators get a quick sense of what's been happening (especially
// useful when services=0 and you are mostly watching host resources).
type ActivitySummary struct {
	TotalEvents      int    `json:"total_events"`
	ServiceActions   int    `json:"service_actions"` // start/stop/restart
	WatchHooks       int    `json:"watch_hooks"`
	WatchNotifies    int    `json:"watch_notifies"`
	Errors           int    `json:"errors"`
	LastEventTime    string `json:"last_event_time,omitempty"` // RFC3339
	LastEventKind    string `json:"last_event_kind,omitempty"`
	LastEventService string `json:"last_event_service,omitempty"`
	LastEventWatch   string `json:"last_event_watch,omitempty"`
}

// MonitoringStatus summarizes how many services are currently being monitored
// vs paused. Useful for a quick header summary.
type MonitoringStatus struct {
	Total     int `json:"total"`
	Monitored int `json:"monitored"`
	Paused    int `json:"paused"`
}

// HostMetric is a single current host-level reading (from the metrics collector).
type HostMetric struct {
	Name     string  `json:"name"`
	Percent  float64 `json:"percent,omitempty"`
	Absolute float64 `json:"absolute,omitempty"`
	Total    float64 `json:"total,omitempty"` // capacity behind a usage metric (memory/swap bytes)
	Unit     string  `json:"unit,omitempty"`
	Ready    bool    `json:"ready"`
}

// ActionResult is the outcome of a state-changing web action.
type ActionResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

// PreflightResult is the outcome of an on-demand preflight run.
type PreflightResult struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// Check is one check's latest observed result in a service detail.
type Check struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	OK       bool   `json:"ok"`
	Optional bool   `json:"optional"`
	Skipped  bool   `json:"skipped,omitempty"` // gated off (requires/skip_when_changed)
	Message  string `json:"message,omitempty"`
	Ran      bool   `json:"ran"`          // false if not observed yet
	At       string `json:"at,omitempty"` // RFC3339 when the check last ran (cached checks keep prior time)
	// Metrics are the check's graphable named series (time-series), if any.
	Metrics []CheckMetric `json:"metrics,omitempty"`
	SLA     []SLAWindow   `json:"sla,omitempty"`
}

// SLAWindow is a service's availability over one rolling window. Ratio is nil
// when the window has no data.
type SLAWindow struct {
	Window string   `json:"window"`
	Ratio  *float64 `json:"ratio"`
	Up     int64    `json:"up"`
	Total  int64    `json:"total"`
}

// Process is a discovered process belonging to a service (parity with
// `sermoctl processes`).
type Process struct {
	PID         int      `json:"pid"`
	PPID        int      `json:"ppid"`
	User        string   `json:"user,omitempty"`
	Exe         string   `json:"exe,omitempty"`
	ExeResolved bool     `json:"exe_resolved"`
	Role        string   `json:"role,omitempty"`
	Source      string   `json:"source"`
	Cmdline     []string `json:"cmdline,omitempty"`
	RSS         int64    `json:"rss,omitempty"`      // resident memory, bytes
	IORead      int64    `json:"io_read,omitempty"`  // cumulative disk read, bytes
	IOWrite     int64    `json:"io_write,omitempty"` // cumulative disk write, bytes
	FDs         int64    `json:"fds,omitempty"`      // open file descriptors
	Threads     int64    `json:"threads,omitempty"`  // thread count
	CPU         float64  `json:"cpu,omitempty"`      // live CPU %, single-core normalized (100% = one core)
	HasCPU      bool     `json:"has_cpu,omitempty"`  // true when a live CPU rate is available (distinguishes 0% from unknown)
}

// ProcessTotals aggregates a service's whole discovered process tree — the
// matched processes and their child/descendant processes — so the totals reflect
// the service's workers and helpers, not just its main process.
type ProcessTotals struct {
	Count   int   `json:"count"`
	RSS     int64 `json:"rss,omitempty"`
	IORead  int64 `json:"io_read,omitempty"`
	IOWrite int64 `json:"io_write,omitempty"`
	FDs     int64 `json:"fds,omitempty"`
	Threads int64 `json:"threads,omitempty"`
	// Live CPU for the whole tree: CPU is the whole-machine rate (% of all
	// cores); CPUThread is the busiest single process against one core (100% =
	// one saturated core); NumCPU is the logical CPU count. HasCPU is true once a
	// rate is available (two samples), so the UI can tell 0% from "measuring".
	CPU       float64 `json:"cpu,omitempty"`
	CPUThread float64 `json:"cpu_thread,omitempty"`
	NumCPU    int     `json:"num_cpu,omitempty"`
	HasCPU    bool    `json:"has_cpu,omitempty"`
}

// RuleWindow is one rule's window progress in a service detail.
type RuleWindow struct {
	Name          string `json:"name"`
	Type          string `json:"type"` // remediation | alert
	Action        string `json:"action,omitempty"`
	Condition     string `json:"condition"`
	ConditionTrue bool   `json:"condition_true"`
	Window        string `json:"window"`
	Progress      string `json:"progress"`
	Firing        bool   `json:"firing"`
}

// Remediation is the automatic remediation policy gating view for one service.
type Remediation struct {
	Allowed           bool   `json:"allowed"`
	Reason            string `json:"reason,omitempty"` // cooldown | rate limit
	Cooldown          string `json:"cooldown,omitempty"`
	EffectiveCooldown string `json:"effective_cooldown,omitempty"`
	CurrentBackoff    string `json:"current_backoff,omitempty"`
	LastActionAt      string `json:"last_action_at,omitempty"`   // RFC3339
	CooldownUntil     string `json:"cooldown_until,omitempty"`   // RFC3339
	NextEligibleAt    string `json:"next_eligible_at,omitempty"` // RFC3339
	MaxActions        int    `json:"max_actions,omitempty"`
	MaxActionsWindow  string `json:"max_actions_window,omitempty"`
	RecentActions     int    `json:"recent_actions,omitempty"`
}

// Lock is a named runtime lock for one service (parity with `sermoctl locks`).
type Lock struct {
	Service             string   `json:"service,omitempty"`
	Name                string   `json:"name,omitempty"`
	Reason              string   `json:"reason,omitempty"`
	State               string   `json:"state"` // active | expired | stale
	OwnerPID            int      `json:"owner_pid"`
	OwnerStatus         string   `json:"owner_status,omitempty"` // live | stale | none | expired
	StaleReason         string   `json:"stale_reason,omitempty"`
	CreatedAt           string   `json:"created_at,omitempty"` // RFC3339
	ExpiresAt           string   `json:"expires_at,omitempty"` // RFC3339
	CreatedAgeSeconds   int64    `json:"created_age_seconds,omitempty"`
	TTLRemainingSeconds int64    `json:"ttl_remaining_seconds,omitempty"`
	BlockedActions      []string `json:"blocked_actions,omitempty"`
	Releaseable         bool     `json:"releaseable,omitempty"`
}

// Detail is a single service's view: its summary plus its checks and SLA.
type Detail struct {
	Service
	Checks            []Check        `json:"checks"`
	SLA               []SLAWindow    `json:"sla"`
	Locks             []Lock         `json:"locks,omitempty"`
	LockWarnings      []string       `json:"lock_warnings,omitempty"`
	NoResidentProcess bool           `json:"no_resident_process,omitempty"`
	ProcessWarnings   []string       `json:"process_warnings,omitempty"`
	Processes         []Process      `json:"processes,omitempty"`
	ProcessTotals     *ProcessTotals `json:"process_totals,omitempty"`
	Remediation       *Remediation   `json:"remediation,omitempty"`
	Rules             []RuleWindow   `json:"rules,omitempty"`
}

// SeriesPoint is one per-minute availability sample of the SLA history. Ratio is
// nil for a minute with no observed cycle.
type SeriesPoint struct {
	Start string   `json:"start"` // RFC3339, minute-aligned
	Ratio *float64 `json:"ratio"`
	Up    int64    `json:"up"`
	Total int64    `json:"total"`
}

// MetricPoint is one time bucket of a check's latency series (milliseconds).
type MetricPoint struct {
	Start string  `json:"start"` // RFC3339, minute-aligned
	N     int64   `json:"n"`
	Avg   float64 `json:"avg"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
}

// MetricSummary is a check's latency over the window: sample count and
// average/min/max in milliseconds (Count==0 means no data).
type MetricSummary struct {
	Count int64   `json:"count"`
	Avg   float64 `json:"avg"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
}

// MetricSeries is a check's metric history plus its summary for one window. Metric
// is empty for the built-in latency series, or the named metric (e.g. "read").
type MetricSeries struct {
	Check   string        `json:"check"`
	Metric  string        `json:"metric,omitempty"`
	Since   string        `json:"since"`
	Unit    string        `json:"unit"`
	Summary MetricSummary `json:"summary"`
	Points  []MetricPoint `json:"points"`
}

// CheckMetric is a graphable named metric a check publishes (for the detail UI to
// know which series to fetch and draw, with its unit).
type CheckMetric struct {
	Name string `json:"name"`
	Unit string `json:"unit"`
}

// Finding is one diagnostic result (level: error|warning|info).
type Finding struct {
	Time    string `json:"time,omitempty"` // RFC3339 when the diagnostic was generated
	Level   string `json:"level"`
	Scope   string `json:"scope"`
	Message string `json:"message"`
}

// OperationSlots is the global start/stop/restart concurrency pool (section 24).
type OperationSlots struct {
	InUse int `json:"in_use"`
	Total int `json:"total"`
}

// ReadyReport is the /readyz readiness probe payload.
type ReadyReport struct {
	Ready    bool   `json:"ready"`
	Status   string `json:"status"` // ok | starting | shutting_down
	Backend  string `json:"backend,omitempty"`
	Services int    `json:"services"`
	Watches  int    `json:"watches"`
	Message  string `json:"message,omitempty"`
}

// ReadinessChecker reports whether the daemon has begun monitoring.
type ReadinessChecker interface {
	Report(ctx context.Context) ReadyReport
}

// Event is one recorded daemon event for the activity log.
type Event struct {
	Time    string `json:"time"` // RFC3339
	Service string `json:"service,omitempty"`
	Watch   string `json:"watch,omitempty"`
	Kind    string `json:"kind"`
	Rule    string `json:"rule,omitempty"`
	Action  string `json:"action,omitempty"`
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
}

// maxSeriesWindow bounds the history a single request may ask for (the retention).
const maxSeriesWindow = 366 * 24 * time.Hour

// defaultEventLimit / maxEventLimit bound how many log events a request returns.
const defaultEventLimit = 100
const maxEventLimit = 1000

// defaultSeriesWindow is used when no (or an invalid) `since` is given.
const defaultSeriesWindow = 24 * time.Hour

// Backend is what the web server needs from the daemon.
type Backend interface {
	// Services returns the current view of every configured service (including those
	// with `enabled: false` in their YAML so they remain visible for activation).
	Services(ctx context.Context) []Service
	// Watches returns configured host watches (including those with `enabled: false`
	// so they remain visible even when services=0).
	Watches(ctx context.Context) []Watch
	// Notifiers returns the named notifiers configured for use by watches.
	Notifiers(ctx context.Context) []Notifier
	// Applications returns the installed applications (catalog app daemons whose
	// binary is present), with their version and binary location.
	Applications(ctx context.Context) []Application
	// Detail returns one service's checks and SLA; ok is false for unknown names.
	Detail(ctx context.Context, name string) (Detail, bool)
	// Series returns a service's per-minute availability history over since; ok is
	// false for unknown names.
	Series(ctx context.Context, name string, since time.Duration) ([]SeriesPoint, bool)
	// Metrics returns a check's latency summary and per-minute history over since;
	// ok is false for unknown service names.
	Metrics(ctx context.Context, name, check, metric string, since time.Duration) (MetricSeries, bool)
	// ServiceRuntime returns process-tree CPU, memory and IO history for one
	// service over since; ok is false for unknown service names.
	ServiceRuntime(ctx context.Context, name string, since time.Duration) (ServiceRuntimeMetrics, bool)
	// Events returns up to limit recent events, newest first (the global feed).
	Events(ctx context.Context, limit int) []Event
	// Diagnostics runs config/host/database consistency checks and returns the
	// findings (ordered by severity).
	Diagnostics(ctx context.Context) []Finding
	// Operations reports how many global operation slots are in use.
	Operations(ctx context.Context) OperationSlots
	// ServiceEvents returns up to limit recent events for one service, newest
	// first; ok is false for unknown names.
	ServiceEvents(ctx context.Context, name string, limit int) ([]Event, bool)
	// PruneEvents removes events older than 'before' (or all if zero time).
	// Intended for the `sermoctl events clear` command.
	PruneEvents(ctx context.Context, before time.Time) int
	// Operate runs start|stop|restart on a service through the safe engine.
	Operate(ctx context.Context, name, action string) ActionResult
	// Preflight runs a service's preflight checks on demand; ok is false for
	// unknown names.
	Preflight(ctx context.Context, name string) (PreflightResult, bool)
	// SetMonitored pauses (false) or resumes (true) monitoring of a service.
	SetMonitored(ctx context.Context, name string, monitored bool) error
	// SetWatchMonitored pauses (false) or resumes (true) monitoring of a host watch.
	SetWatchMonitored(ctx context.Context, name string, monitored bool) error
	// ExpandWatch runs a configured storage watch's `then.expand` action on demand.
	ExpandWatch(ctx context.Context, name string) ActionResult
	// DaemonInfo returns engine settings and basic daemon configuration.
	DaemonInfo(ctx context.Context) DaemonInfo
	// DaemonMetrics returns current and historical resource usage for sermod.
	DaemonMetrics(ctx context.Context, since time.Duration) DaemonMetrics
	// HostMetrics returns current system-level metrics (memory, cpu, load averages).
	HostMetrics(ctx context.Context) []HostMetric
	// Locks returns runtime locks (active, expired, stale) across all services.
	Locks(ctx context.Context) []Lock
	// ReleaseLock explicitly removes an inactive named runtime lock.
	ReleaseLock(ctx context.Context, service, name string) ActionResult
	// ActivitySummary returns a quick overview of recent daemon activity
	// (useful for the dashboard header when you have mostly watches).
	ActivitySummary(ctx context.Context) ActivitySummary
	// MonitoringStatus returns counts of monitored vs paused services.
	MonitoringStatus(ctx context.Context) MonitoringStatus
}

// operateActions, monitorActions and watchOperateActions are the action verbs the API accepts.
var operateActions = map[string]bool{"start": true, "stop": true, "restart": true, "reload": true}
var monitorActions = map[string]bool{"monitor": true, "unmonitor": true}
var watchOperateActions = map[string]bool{"expand": true}

// defaultOperationTimeout matches operation.DefaultOperationTimeout when sermod
// does not set OperationTimeout on the server.
const defaultOperationTimeout = 90 * time.Second

// writeTimeoutMargin is added to OperationTimeout so the handler can finish
// writing the JSON response after a long operation completes.
const writeTimeoutMargin = 5 * time.Second

// minWriteTimeout keeps short read-only requests bounded when OperationTimeout
// is unusually small.
const minWriteTimeout = 30 * time.Second

// Server is the HTTP dashboard. Addr is a host:port; Backend is required. Auth is
// optional (zero value = open). OperationTimeout bounds how long start/stop/restart
// may run and sizes the HTTP write deadline; it should be the maximum per-service
// deadline (app.MaxOperationTimeout).
type Server struct {
	Addr    string
	Backend Backend
	Auth    Auth
	Logger  *slog.Logger

	OperationTimeout time.Duration
	// Readiness is optional; nil makes /readyz report ready (tests).
	Readiness ReadinessChecker

	// Reload, if set, is called for admin POST /api/reload requests. It should
	// trigger a configuration reload (equivalent to SIGHUP on the daemon).
	// Used by both the web UI button and (indirectly) sermoctl reload when the
	// web UI is reachable.
	Reload func() error

	// DiagnosticsDisabled hides the Diagnostics panel in the dashboard and makes
	// GET /api/diagnostics return an empty result. Set from web.disable_diagnostics
	// in the global config.
	DiagnosticsDisabled bool

	started  time.Time       // when the server began serving; for /livez uptime
	shutdown context.Context // daemon lifetime; set in Run
}

// Handler returns the router behind the auth middleware: the dashboard at /, the
// service list at /api/services, and POST /api/services/{name}/{action} for
// actions.
func (s *Server) Handler() http.Handler {
	if s.started.IsZero() {
		s.started = time.Now()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /livez", s.handleLivez)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /api/whoami", s.handleWhoami)
	mux.HandleFunc("GET /api/services", s.handleServices)
	mux.HandleFunc("GET /api/watches", s.handleWatches)
	mux.HandleFunc("POST /api/watches/{name}/{action}", s.handleWatchAction)
	mux.HandleFunc("GET /api/notifiers", s.handleNotifiers)
	mux.HandleFunc("GET /api/applications", s.handleApplications)
	mux.HandleFunc("GET /api/daemon", s.handleDaemon)
	mux.HandleFunc("GET /api/daemon/metrics", s.handleDaemonMetrics)
	mux.HandleFunc("GET /api/host", s.handleHost)
	mux.HandleFunc("GET /api/locks", s.handleLocks)
	mux.HandleFunc("POST /api/locks/{service}/release", s.handleLockRelease)
	mux.HandleFunc("GET /api/activity", s.handleActivity)
	mux.HandleFunc("GET /api/monitoring", s.handleMonitoring)
	mux.HandleFunc("GET /api/services/{name}", s.handleDetail)
	mux.HandleFunc("GET /api/services/{name}/sla", s.handleSeries)
	mux.HandleFunc("GET /api/services/{name}/metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/services/{name}/runtime", s.handleServiceRuntime)
	mux.HandleFunc("GET /api/services/{name}/events", s.handleServiceEvents)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/events/clear", s.handleEventsClear)
	mux.HandleFunc("GET /api/diagnostics", s.handleDiagnostics)
	mux.HandleFunc("GET /api/ops", s.handleOperations)
	mux.HandleFunc("POST /api/services/{name}/preflight", s.handlePreflight)
	mux.HandleFunc("POST /api/services/{name}/{action}", s.handleAction)
	mux.HandleFunc("POST /api/reload", s.handleReload)
	return securityHeaders(s.withAuth(mux))
}

type cspNonceCtxKey struct{}

// securityHeaders adds standard hardening headers to every response. The CSP
// keeps the dashboard self-contained (no external origins). The embedded UI uses
// a per-response nonce for its script block; style-src must rely on
// 'unsafe-inline' alone — the dashboard hides sections and sizes its gauges via
// generated style attributes, and per CSP2 the presence of a nonce in the list
// makes browsers ignore 'unsafe-inline', silently stripping every one of them.
// Style injection cannot exfiltrate here anyway (CSS-loaded images fall under
// img-src, which stays 'self' + data:), while script-src keeps the real
// boundary nonce-strict.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := cspNonce()
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'nonce-"+nonce+"'; "+
				"style-src 'self' 'unsafe-inline'; img-src 'self' data:; "+
				"base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), cspNonceCtxKey{}, nonce)))
	})
}

func cspNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawStdEncoding.EncodeToString(b[:])
}

func cspNonceFrom(ctx context.Context) string {
	nonce, _ := ctx.Value(cspNonceCtxKey{}).(string)
	return nonce
}

// eventLimit reads the `limit` query param, defaulting and capping it.
func eventLimit(r *http.Request) int {
	limit := defaultEventLimit
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxEventLimit {
		limit = maxEventLimit
	}
	return limit
}

type eventFilter struct {
	Service    string
	Watch      string
	Kind       string
	Status     string
	OnlyErrors bool
}

func parseEventFilter(r *http.Request) eventFilter {
	q := r.URL.Query()
	return eventFilter{
		Service:    q.Get("service"),
		Watch:      q.Get("watch"),
		Kind:       q.Get("kind"),
		Status:     q.Get("status"),
		OnlyErrors: truthy(q.Get("only_errors")),
	}
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (f eventFilter) active() bool {
	return f.Service != "" || f.Watch != "" || f.Kind != "" || f.Status != "" || f.OnlyErrors
}

func filterEvents(events []Event, f eventFilter, limit int) []Event {
	if !f.active() {
		if len(events) > limit {
			return events[:limit]
		}
		return events
	}
	out := make([]Event, 0, min(limit, len(events)))
	for _, e := range events {
		if f.Service != "" && e.Service != f.Service {
			continue
		}
		if f.Watch != "" && e.Watch != f.Watch {
			continue
		}
		if f.Kind != "" && e.Kind != f.Kind {
			continue
		}
		if f.Status != "" && e.Status != f.Status {
			continue
		}
		if f.OnlyErrors && !isErrorEvent(e) {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func isErrorEvent(e Event) bool {
	if e.Kind == "error" || strings.Contains(e.Kind, "failed") {
		return true
	}
	switch e.Status {
	case "failed", "error", "blocked", "orphan_processes", "preflight_failed", "postflight_failed":
		return true
	default:
		return false
	}
}

// csrfHeader must be present on every state-changing (POST) request. A cross-site
// HTML form cannot set a custom header, and a cross-site fetch that tries to would
// trigger a CORS preflight we never answer — so requiring it blocks CSRF against
// the (root-privileged) action endpoints, in both authenticated and open modes.
const csrfHeader = "X-Sermo-CSRF"

// serverWriteTimeout returns the HTTP write deadline for action handlers that may
// block until a safe operation finishes.
func serverWriteTimeout(maxOp time.Duration) time.Duration {
	if maxOp <= 0 {
		maxOp = defaultOperationTimeout
	}
	wt := maxOp + writeTimeoutMargin
	if wt < minWriteTimeout {
		return minWriteTimeout
	}
	return wt
}

// operateContext returns a context for start/stop/restart that is not tied to the
// HTTP request. Client disconnect and the generic write deadline must not abort
// an in-flight safe operation; the operation engine applies its own timeout.
func (s *Server) operateContext() context.Context {
	if s.shutdown != nil {
		return s.shutdown
	}
	return context.Background()
}

// Run serves until ctx is cancelled, then shuts down gracefully. Timeouts bound
// slow clients (the server runs as root, so it is hardened by default).
func (s *Server) Run(ctx context.Context) error {
	s.shutdown = ctx
	srv := &http.Server{
		Addr:              s.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      serverWriteTimeout(s.OperationTimeout),
		IdleTimeout:       60 * time.Second,
	}
	go func() { //nolint:gosec // G118: the shutdown deadline must NOT derive from ctx — it is already cancelled here
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := assets.ReadFile("index.html")
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	page = []byte(strings.ReplaceAll(string(page), "{{CSP_NONCE}}", cspNonceFrom(r.Context())))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The dashboard markup/JS is embedded in the binary and changes across
	// versions (new sections like host watches are added over time). Without a
	// cache directive a browser may keep serving a stale copy after an upgrade,
	// so newly added sections never appear even though the API returns their
	// data. no-cache forces a revalidation on every load.
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(page)
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.Services(r.Context()))
}

func (s *Server) handleWatches(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.Watches(r.Context()))
}

func (s *Server) handleNotifiers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.Notifiers(r.Context()))
}

func (s *Server) handleApplications(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.Applications(r.Context()))
}

func (s *Server) handleDaemon(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.DaemonInfo(r.Context()))
}

func (s *Server) handleDaemonMetrics(w http.ResponseWriter, r *http.Request) {
	since := seriesSince(r)
	writeJSON(w, http.StatusOK, s.Backend.DaemonMetrics(r.Context(), since))
}

func (s *Server) handleHost(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.HostMetrics(r.Context()))
}

func (s *Server) handleLocks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.Locks(r.Context()))
}

func (s *Server) handleLockRelease(w http.ResponseWriter, r *http.Request) {
	res := s.Backend.ReleaseLock(r.Context(), r.PathValue("service"), r.URL.Query().Get("name"))
	status := http.StatusOK
	if !res.OK {
		status = http.StatusConflict
	}
	writeJSON(w, status, res)
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.ActivitySummary(r.Context()))
}

func (s *Server) handleMonitoring(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.MonitoringStatus(r.Context()))
}

func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request) {
	detail, ok := s.Backend.Detail(r.Context(), r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown service")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// seriesSince reads the `since` query param, defaulting and capping it.
func seriesSince(r *http.Request) time.Duration {
	since := defaultSeriesWindow
	if q := r.URL.Query().Get("since"); q != "" {
		if d, err := time.ParseDuration(q); err == nil && d > 0 {
			since = d
		}
	}
	if since > maxSeriesWindow {
		since = maxSeriesWindow
	}
	return since
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	since := seriesSince(r)
	points, ok := s.Backend.Series(r.Context(), r.PathValue("name"), since)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown service")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"since": since.String(), "points": points})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	check := r.URL.Query().Get("check")
	if check == "" {
		writeError(w, http.StatusBadRequest, "check query parameter is required")
		return
	}
	res, ok := s.Backend.Metrics(r.Context(), r.PathValue("name"), check, r.URL.Query().Get("metric"), seriesSince(r))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown service or check")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleServiceRuntime(w http.ResponseWriter, r *http.Request) {
	res, ok := s.Backend.ServiceRuntime(r.Context(), r.PathValue("name"), seriesSince(r))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown service")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	limit := eventLimit(r)
	filter := parseEventFilter(r)
	fetchLimit := limit
	if filter.active() {
		fetchLimit = maxEventLimit
	}
	writeJSON(w, http.StatusOK, filterEvents(s.Backend.Events(r.Context(), fetchLimit), filter, limit))
}

// handleEventsClear supports `sermoctl events clear [--before TIME]`.
// TIME may be RFC3339 or a duration (e.g. "2h" means "before now-2h").
func (s *Server) handleEventsClear(w http.ResponseWriter, r *http.Request) {
	beforeStr := r.URL.Query().Get("before")
	var before time.Time
	if beforeStr != "" {
		if t, err := time.Parse(time.RFC3339, beforeStr); err == nil {
			before = t
		} else if d, err := time.ParseDuration(beforeStr); err == nil {
			before = time.Now().Add(-d)
		} else {
			writeJSON(w, http.StatusBadRequest, ActionResult{OK: false, Message: "bad before: RFC3339 timestamp or duration (e.g. 1h, 30m)"})
			return
		}
	}
	n := s.Backend.PruneEvents(r.Context(), before)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"pruned": n,
	})
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if s.DiagnosticsDisabled {
		writeJSON(w, http.StatusOK, []Finding{})
		return
	}
	writeJSON(w, http.StatusOK, timestampFindings(s.Backend.Diagnostics(r.Context()), time.Now()))
}

func timestampFindings(findings []Finding, at time.Time) []Finding {
	if len(findings) == 0 {
		return findings
	}
	out := make([]Finding, len(findings))
	copy(out, findings)
	ts := at.Format(time.RFC3339)
	for i := range out {
		if out[i].Time == "" {
			out[i].Time = ts
		}
	}
	return out
}

func (s *Server) handleOperations(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.Operations(r.Context()))
}

// handleLivez is the liveness probe: if the daemon's web server can answer, the
// process is alive, so it always returns 200. Plain requests get "ok"; `?verbose`
// returns JSON with uptime, the number of services and the runtime version. It is
// served without authentication (see withAuth) so probes need no credentials.
func (s *Server) readyReport(ctx context.Context) ReadyReport {
	if s.Readiness != nil {
		return s.Readiness.Report(ctx)
	}
	return ReadyReport{
		Ready: true, Status: "ok",
		Services: len(s.Backend.Services(ctx)),
	}
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	rep := s.readyReport(r.Context())
	status := http.StatusOK
	if !rep.Ready {
		status = http.StatusServiceUnavailable
	}
	if !r.URL.Query().Has("verbose") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(status)
		if rep.Ready {
			_, _ = io.WriteString(w, "ok\n")
		} else {
			_, _ = io.WriteString(w, rep.Status+"\n")
		}
		return
	}
	writeJSON(w, status, rep)
}

func (s *Server) handleLivez(w http.ResponseWriter, r *http.Request) {
	if !r.URL.Query().Has("verbose") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
		return
	}
	now := time.Now()
	uptime := now.Sub(s.started)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"started_at":     s.started.Format(time.RFC3339),
		"now":            now.Format(time.RFC3339),
		"uptime":         uptime.Round(time.Second).String(),
		"uptime_seconds": int64(uptime.Seconds()),
		"services":       len(s.Backend.Services(r.Context())),
		"go":             runtime.Version(),
	})
}

func (s *Server) handleServiceEvents(w http.ResponseWriter, r *http.Request) {
	events, ok := s.Backend.ServiceEvents(r.Context(), r.PathValue("name"), eventLimit(r))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown service")
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	res, ok := s.Backend.Preflight(r.Context(), name)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown service")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	action := r.PathValue("action")
	switch {
	case operateActions[action]:
		res := s.Backend.Operate(s.operateContext(), name, action)
		status := http.StatusOK
		if !res.OK {
			status = http.StatusConflict
		}
		writeJSON(w, status, res)
	case monitorActions[action]:
		err := s.Backend.SetMonitored(r.Context(), name, action == "monitor")
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ActionResult{OK: true})
	default:
		writeError(w, http.StatusBadRequest, "unknown action "+action)
	}
}

func (s *Server) handleWatchAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	action := r.PathValue("action")
	if watchOperateActions[action] {
		res := s.Backend.ExpandWatch(s.operateContext(), name)
		status := http.StatusOK
		if !res.OK {
			status = http.StatusConflict
		}
		writeJSON(w, status, res)
		return
	}
	if !monitorActions[action] {
		writeError(w, http.StatusBadRequest, "unknown action "+action)
		return
	}
	if err := s.Backend.SetWatchMonitored(r.Context(), name, action == "monitor"); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ActionResult{OK: true})
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if s.Reload == nil {
		writeError(w, http.StatusServiceUnavailable, "reload is not available for this daemon")
		return
	}
	if err := s.Reload(); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ActionResult{OK: true, Message: "reload requested"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// writeError replies with an ActionResult failure — the uniform error body
// every JSON handler returns.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ActionResult{OK: false, Message: msg})
}
