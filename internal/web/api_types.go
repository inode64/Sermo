package web

import (
	"context"
	"time"
)

// ServiceButton is one configured operator button of a service: the name the
// API route uses and the label the dashboard shows.
type ServiceButton struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

// Service is the web view of one configured service. Services with `enabled: false`
// in their configuration are still listed (with Enabled=false) so operators can
// see the full fleet and know what to activate by editing config + reloading.
type Service struct {
	Name                 string   `json:"name"`
	DisplayName          string   `json:"display_name"`
	Category             string   `json:"category,omitempty"`
	Backend              string   `json:"backend"`
	Unit                 string   `json:"unit"`
	State                string   `json:"state"`
	Status               string   `json:"status"`
	StatusObservedAt     string   `json:"status_observed_at,omitempty"` // RFC3339 when init status was actually sampled
	Interval             string   `json:"interval,omitempty"`           // resolved per-service cycle cadence (own interval or engine default)
	DryRun               bool     `json:"dry_run,omitempty"`            // true when automatic actions are simulated
	Enabled              bool     `json:"enabled"`                      // false when service document has `enabled: false`
	Monitored            bool     `json:"monitored"`
	MonitorSource        string   `json:"monitor_source,omitempty"`        // cli | web | config | daemon
	MonitorChangedAt     string   `json:"monitor_changed_at,omitempty"`    // RFC3339 when monitoring state last changed
	CheckHealth          string   `json:"check_health,omitempty"`          // ok | warning | failing | unknown | paused | disabled
	ChecksFailing        int      `json:"checks_failing,omitempty"`        // required checks currently failing
	ObservabilityReady   bool     `json:"observability_ready"`             // true when monitored service has fresh visible indicators
	ObservabilityMissing []string `json:"observability_missing,omitempty"` // indicator groups still collecting
	// StateReason is a machine-readable cause behind an operator-facing state
	// (for example "stale_binary"), for clients to phrase. Empty when unknown.
	StateReason string `json:"state_reason,omitempty"`
	// Strays counts the processes the init unit's control group holds that no
	// configured selector claims, from the strays check's published snapshot. It is
	// what the Strays column and the reap button read; 0 means either none found or
	// no current sample, a distinction the detail's check row makes.
	Strays int `json:"strays,omitempty"`
	// Buttons are the service's configured operator buttons: explicit admin
	// commands offered in the dashboard, run exactly as configured.
	Buttons          []ServiceButton `json:"buttons,omitempty"`
	ActiveLocks      []string        `json:"active_locks,omitempty"`      // named runtime locks blocking actions
	OperationActive  bool            `json:"operation_active,omitempty"`  // true while the engine holds this service's operation lock: an action is running, whoever started it
	PolicyCooldown   string          `json:"policy_cooldown,omitempty"`   // resolved automatic remediation cooldown
	RemediationState string          `json:"remediation_state,omitempty"` // eligible | cooldown | rate limit | paused | pending | disabled
	NextEligibleAt   string          `json:"next_eligible_at,omitempty"`  // RFC3339 when automatic remediation is next eligible
	CanReload        bool            `json:"can_reload"`                  // true when init or native reload support is available
	LastEvent        *Event          `json:"last_event,omitempty"`        // newest service event, when retained

	// Current process-tree runtime summary. These fields intentionally mirror
	// ProcessTotals so the service list and detail expansion use the same
	// semantics: matched processes plus their child/descendant processes.
	NoResidentProcess bool     `json:"no_resident_process,omitempty"` // true for oneshot/helper services with no resident process tree
	StartedAt         string   `json:"started_at,omitempty"`          // oldest discovered process start time, RFC3339
	Uptime            string   `json:"uptime,omitempty"`              // display-ready age of StartedAt
	UptimeSeconds     int64    `json:"uptime_seconds,omitempty"`
	RSS               int64    `json:"rss,omitempty"`
	IORead            int64    `json:"io_read,omitempty"`  // cumulative disk read bytes
	IOWrite           int64    `json:"io_write,omitempty"` // cumulative disk write bytes
	FDs               int64    `json:"fds,omitempty"`
	Threads           int64    `json:"threads,omitempty"`
	CPU               float64  `json:"cpu,omitempty"`        // live CPU %, all host CPUs
	CPUThread         float64  `json:"cpu_thread,omitempty"` // busiest thread, single-core normalized
	NumCPU            int      `json:"num_cpu,omitempty"`
	CPUReady          bool     `json:"cpu_ready,omitempty"`
	AlsoApply         []string `json:"also_apply,omitempty"` // also_apply cascade targets
}

// Mount is a view of one configured fstab-backed mount unit for the dashboard.
type Mount struct {
	Name         string          `json:"name"`
	DisplayName  string          `json:"display_name,omitempty"`
	Category     string          `json:"category,omitempty"`
	Path         string          `json:"path"`
	Mounted      bool            `json:"mounted"`
	Refcount     int             `json:"refcount"`
	State        string          `json:"state"`
	Operation    *MountOperation `json:"operation,omitempty"`
	Refcounted   bool            `json:"refcounted"`
	CanUmount    bool            `json:"can_umount"`
	UmountReason string          `json:"umount_disabled_reason,omitempty"`
	Message      string          `json:"message,omitempty"` // set when status sampling failed
	Blockers     []MountBlocker  `json:"blockers,omitempty"`
	BlockerError string          `json:"blocker_error,omitempty"`
}

// MountOperation reports a mount unit operation currently running in the daemon.
type MountOperation struct {
	Action    string `json:"action"`
	State     string `json:"state"`
	StartedAt string `json:"started_at,omitempty"` // RFC3339
	Message   string `json:"message,omitempty"`
}

// MountBlocker is one process currently using a mount path.
type MountBlocker struct {
	PID         int      `json:"pid"`
	PPID        int      `json:"ppid"`
	User        string   `json:"user,omitempty"`
	UID         uint32   `json:"uid"`
	Group       string   `json:"group,omitempty"`
	GID         uint32   `json:"gid"`
	Exe         string   `json:"exe,omitempty"`
	ExeResolved bool     `json:"exe_resolved"`
	Cmdline     []string `json:"cmdline,omitempty"`
	Killable    bool     `json:"killable"`
}

// MountActionOptions controls mount unit operation behavior from the web API.
type MountActionOptions struct {
	AllowForce   bool // allow umount -f after a failed normal umount
	AllowLazy    bool // allow umount -l as the last fallback
	KillBlockers bool // allow policy-gated SIGTERM/SIGKILL escalation during umount
}

// MountActionResult is the outcome of a mount or unmount web action.
type MountActionResult struct {
	OK        bool            `json:"ok"`
	Name      string          `json:"name,omitempty"`
	Path      string          `json:"path,omitempty"`
	Action    string          `json:"action,omitempty"`
	Status    string          `json:"status,omitempty"`
	Message   string          `json:"message,omitempty"`
	Mounted   bool            `json:"mounted"`
	Refcount  int             `json:"refcount"`
	Operation *MountOperation `json:"operation,omitempty"`
	Forced    bool            `json:"forced,omitempty"`
	Lazy      bool            `json:"lazy,omitempty"`
	Signalled []int           `json:"signalled,omitempty"`
	Blockers  []MountBlocker  `json:"blockers,omitempty"`
}

// MountBlockersResult is a read-only preflight view for a mount unit.
type MountBlockersResult struct {
	OK            bool           `json:"ok"`
	Name          string         `json:"name,omitempty"`
	CanUmount     bool           `json:"can_umount"`
	UmountReason  string         `json:"umount_disabled_reason,omitempty"`
	HasKillPolicy bool           `json:"has_kill_policy"`
	CanKill       bool           `json:"can_kill"`
	CanAlert      bool           `json:"can_alert"`
	Message       string         `json:"message,omitempty"`
	Blockers      []MountBlocker `json:"blockers,omitempty"`
}

// MountAlertResult is the outcome of notifying users that block a mount.
type MountAlertResult struct {
	OK      bool   `json:"ok"`
	Name    string `json:"name,omitempty"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message,omitempty"`
}

// CatalogItem is the shared web view of one installed catalog application or
// library. It mirrors the sermoctl `apps` and `libs` reports so every surface
// agrees about versions, locations and inspection status.
type CatalogItem struct {
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	Category      string `json:"category,omitempty"`
	Binary        string `json:"binary"`                   // resolved binary path (file location)
	Permissions   string `json:"permissions,omitempty"`    // binary mode, e.g. "-rwxr-xr-x (0755)"
	User          string `json:"user,omitempty"`           // owner username of the binary
	Group         string `json:"group,omitempty"`          // owner group of the binary
	Version       string `json:"version"`                  // raw first line of the version command
	VersionShort  string `json:"version_short"`            // numeric version, at most the patchlevel
	VersionSource string `json:"version_source,omitempty"` // app whose version probe supplied this version
	Status        string `json:"status"`                   // ok, or an error description
	State         string `json:"state,omitempty"`          // starting | ok | failed | warning
	ObservedAt    string `json:"observed_at,omitempty"`    // RFC3339 when version/status probes actually ran
	LastEvent     *Event `json:"last_event,omitempty"`     // populated with the newest retained application event

	// KeepsSLA marks an application that maps to a monitored service, so the
	// dashboard draws it that service's SLA section — the same panel, selector
	// and series the service detail shows. An application's availability is the
	// service's; it is fetched from the service's own endpoint rather than
	// carried here, so the figure has exactly one producer.
	KeepsSLA bool `json:"keeps_sla,omitempty"`
}

// Application is an installed catalog application returned by the dashboard.
type Application = CatalogItem

// Library is an installed catalog library returned by the dashboard.
type Library = CatalogItem

// WatchSampleState reports whether the latest daemon-published watch sample is
// usable for dashboard readings.
const (
	WatchSampleStateCollecting = "collecting"
	WatchSampleStateFresh      = "fresh"
	WatchSampleStateStale      = "stale"
	WatchScopeHost             = "host"
	WatchScopeService          = "service"
)

// Watch is a dashboard view of a host-level or service-scoped watch, enriched
// with useful runtime/config info for operators.
type Watch struct {
	Name              string            `json:"name"`
	Scope             string            `json:"scope"` // host | service
	DisplayName       string            `json:"display_name,omitempty"`
	Category          string            `json:"category,omitempty"`
	CheckType         string            `json:"check_type,omitempty"`
	Summary           string            `json:"summary,omitempty"`
	SummaryConfigured bool              `json:"summary_configured,omitempty"`
	Interval          string            `json:"interval,omitempty"`
	State             string            `json:"state"`
	Enabled           bool              `json:"enabled"`
	Monitor           string            `json:"monitor,omitempty"` // enabled | disabled | previous
	Monitored         bool              `json:"monitored"`
	MonitorSource     string            `json:"monitor_source,omitempty"`
	MonitorChangedAt  string            `json:"monitor_changed_at,omitempty"`
	FireOnFail        bool              `json:"fire_on_fail"` // true = fires when check fails (e.g. health checks); false = fires on condition (e.g. load/storage)
	HasHook           bool              `json:"has_hook"`
	HookCommand       []string          `json:"hook_command,omitempty"`
	Notifiers         []string          `json:"notifiers,omitempty"`
	NotifierCount     int               `json:"notifier_count"`
	DryRun            bool              `json:"dry_run"`
	Conditions        []WatchCondition  `json:"conditions,omitempty"`
	Storage           *StorageWatchInfo `json:"storage,omitempty"`
	Swap              *SwapWatchInfo    `json:"swap,omitempty"`
	Meter             *WatchMeter       `json:"meter,omitempty"`
	Readings          []WatchReading    `json:"readings,omitempty"`
	Expand            *WatchExpand      `json:"expand,omitempty"`
	CanProbe          bool              `json:"can_probe,omitempty"`
	CanControlRAID    bool              `json:"can_control_raid,omitempty"`
	RAIDArray         string            `json:"raid_array,omitempty"`
	// CanControlReplication marks a replication watch whose manual start
	// control is configured and startable by an admin.
	CanControlReplication bool        `json:"can_control_replication,omitempty"`
	LastActivity          string      `json:"last_activity,omitempty"` // RFC3339 of last watch activity, if any
	LastActivityKind      string      `json:"last_activity_kind,omitempty"`
	LastCheckedAt         string      `json:"last_checked_at,omitempty"` // RFC3339 of latest completed check sample
	SampleState           string      `json:"sample_state,omitempty"`    // collecting | fresh | stale
	Probe                 *WatchProbe `json:"probe,omitempty"`           // current manual probe, if one is running
	// KeepsSLA marks a watch whose check asserts availability, so the dashboard
	// draws it the same SLA section a service gets. A condition watch keeps none:
	// its threshold being met is not downtime.
	KeepsSLA bool `json:"keeps_sla,omitempty"`
	// Metrics are the numeric series this watch's check publishes, each read from
	// /api/watches/{name}/metrics?metric=NAME and drawn with the panel a service
	// check's metric gets. A watch has exactly one check, so nothing here names it.
	Metrics []CheckMetric `json:"metrics,omitempty"`
}

// WatchProbe reports a manual host-watch probe currently running in the daemon.
// It is intentionally transient: the completed sample and audit event are the
// durable record of a probe.
type WatchProbe struct {
	State     string `json:"state"`
	StartedAt string `json:"started_at"` // RFC3339
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
	// Warning carries the same bad news as Error for a watch its operator graded
	// an advisory. The two are separate fields because Error is what turns the
	// dashboard row red, and the whole point of a warning is that it must not.
	Warning string `json:"warning,omitempty"`
	// Good marks an explicitly healthy state reading — "none degraded", "idle" —
	// rendered green, so a state row answers at a glance instead of printing the
	// number that encodes it.
	Good bool `json:"good,omitempty"`
}

// WatchExpand is the configured manual/automatic storage growth action.
type WatchExpand struct {
	ByBytes int64 `json:"by_bytes"`
}

// SwapWatchInfo is live swap usage for a swap host watch, mirroring the
// volume-style used/free rendering of StorageWatchInfo.
type SwapWatchInfo struct {
	TotalBytes uint64  `json:"total_bytes"`
	UsedBytes  uint64  `json:"used_bytes"`
	FreeBytes  uint64  `json:"free_bytes"`
	UsedPct    float64 `json:"used_pct"`
}

// WatchMeter is a generic 0-100% usage gauge for a host watch that has a
// natural capacity (memory, load, fds, pids, conntrack), giving those watches the same
// progress-bar rendering as swap/storage. UsedPct always drives the bar; the
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

// StorageWatchInfo is the latest daemon-cycle filesystem data for a storage host watch.
type StorageWatchInfo struct {
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
	Summary string `json:"summary,omitempty"`
	UsedBy  int    `json:"used_by,omitempty"`
}

// DaemonInfo provides a summary of the running daemon configuration
// (engine settings and paths). Useful for operators to see effective
// behavior without reading the config file.
type DaemonInfo struct {
	Backend           string        `json:"backend,omitempty"`
	Hostname          string        `json:"hostname,omitempty"`
	OS                string        `json:"os,omitempty"`
	HostType          *HostTypeInfo `json:"host_type,omitempty"`
	HostUptime        string        `json:"host_uptime,omitempty"`         // display-ready uptime of the host/server since boot
	HostUptimeSeconds int64         `json:"host_uptime_seconds,omitempty"` // host/server uptime in whole seconds
	ConfigPath        string        `json:"config_path,omitempty"`
	RuntimeDir        string        `json:"runtime_dir,omitempty"`
	StateDir          string        `json:"state_dir,omitempty"`
	Interval          string        `json:"interval"`
	MaxParallelChecks int           `json:"max_parallel_checks"`
	// ActiveUsers is the number of distinct users with an active login session,
	// retained for API consumers that need a de-duplicated account count.
	ActiveUsers      int             `json:"active_users"`
	Sessions         *SessionSummary `json:"sessions,omitempty"`
	DefaultTimeout   string          `json:"default_timeout"`
	OperationTimeout string          `json:"operation_timeout"`
	StartupDelay     string          `json:"startup_delay"`
}

// SessionSummary separates active local-console and SSH terminal sessions.
// It is omitted when Sermo cannot prove SSH ancestry from the configured SSH
// service, rather than guessing from a terminal name.
type SessionSummary struct {
	Console int `json:"console"`
	SSH     int `json:"ssh"`
}

// HostTypeInfo describes the host's virtualization class for the dashboard.
type HostTypeInfo struct {
	Kind     string `json:"kind,omitempty"`     // bare_metal | virtual_machine | unknown
	Platform string `json:"platform,omitempty"` // kvm | hyperv | vmware | virtualbox | xen | ...
	Label    string `json:"label,omitempty"`    // display-ready summary, e.g. "KVM/QEMU VM"
	Detail   string `json:"detail,omitempty"`   // source detail such as DMI vendor/product
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
	Errors        int    `json:"errors"`
	LastEventKind string `json:"last_event_kind,omitempty"`
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
	OK       bool           `json:"ok"`
	Message  string         `json:"message,omitempty"`
	Readings []WatchReading `json:"readings,omitempty"`
	// Severity grades a rejected outcome for the client that renders it. A watch
	// declared an advisory reports "warning" here, so a manual probe of one is
	// not announced as a failure while the dashboard shows it amber.
	Severity string `json:"severity,omitempty"`
}

// OperateOpts controls optional service-operation behavior from the web API.
type OperateOpts struct {
	NoCascade bool // skip also_apply cascade targets
}

// StateCompactResult is the outcome of pruning old persisted history and
// vacuuming the SQLite state database.
type StateCompactResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Pruned  int64  `json:"pruned"`
	Before  string `json:"before,omitempty"` // RFC3339 cutoff, empty when none was given
	Rolled  int64  `json:"rolled,omitempty"`
	// Archives is the rows pruned from the resolution archives; Events the rows
	// pruned from the event feed. Pruned is their sum.
	Archives int64 `json:"archives,omitempty"`
	Events   int64 `json:"events,omitempty"`
	Vacuum   bool  `json:"vacuum"`
}

// PreflightResult is the outcome of an on-demand preflight run.
type PreflightResult struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// Check is one check's latest observed result in a service detail.
type Check struct {
	Name string `json:"name"`
	Type string `json:"type"`
	OK   bool   `json:"ok"`
	// Reports is the check's declared reporting mode ("state" for a state
	// sensor); empty means the default health semantics. It tells the dashboard
	// whether to label the check ok/fail or active/inactive.
	Reports  string `json:"reports,omitempty"`
	Stale    bool   `json:"stale,omitempty"`
	Optional bool   `json:"optional"`
	// Severity grades a failing check for the row that renders it: "warning"
	// reads amber like an optional check, "error" (the default) reads red.
	Severity string         `json:"severity,omitempty"`
	Skipped  bool           `json:"skipped,omitempty"` // gated off (requires/skip_when_changed)
	Message  string         `json:"message,omitempty"`
	Readings []WatchReading `json:"readings,omitempty"`
	Ran      bool           `json:"ran"`          // false if not observed yet
	At       string         `json:"at,omitempty"` // RFC3339 when the check last ran (cached checks keep prior time)
	// Metrics are the check's graphable named series (time-series), if any.
	// A check's availability is not embedded here: the dashboard reads it from
	// /api/services/{name}/sla?check=NAME, on the window its selector is on, the
	// same endpoint and window the service-level timeline uses.
	Metrics []CheckMetric `json:"metrics,omitempty"`
}

// Process is a discovered process belonging to a service (parity with
// `sermoctl processes`).
type Process struct {
	PID         int    `json:"pid"`
	PPID        int    `json:"ppid"`
	User        string `json:"user,omitempty"`
	Exe         string `json:"exe,omitempty"`
	ExeResolved bool   `json:"exe_resolved"`
	// ExePrevious names the path of a binary replaced or removed on disk while
	// this process kept running. Without it the dashboard can only render such
	// a process as "unknown".
	ExePrevious string `json:"exe_previous,omitempty"`
	Role        string `json:"role,omitempty"`
	// Stray marks a control-group member no configured selector claims. Without it
	// the dashboard shows the backend seed's role "main" and the process reads as
	// the service's principal one.
	Stray   bool     `json:"stray,omitempty"`
	Source  string   `json:"source"`
	Cmdline []string `json:"cmdline,omitempty"`
	RSS     int64    `json:"rss,omitempty"`      // resident memory, bytes
	IORead  int64    `json:"io_read,omitempty"`  // cumulative disk read, bytes
	IOWrite int64    `json:"io_write,omitempty"` // cumulative disk write, bytes
	FDs     int64    `json:"fds,omitempty"`      // open file descriptors
	Threads int64    `json:"threads,omitempty"`  // thread count
	CPU     float64  `json:"cpu,omitempty"`      // live CPU %, single-core normalized (100% = one core)
	HasCPU  bool     `json:"has_cpu,omitempty"`  // true when a live CPU rate is available (distinguishes 0% from unknown)
	// MaxCore is the most any single core was used on this process's behalf: its
	// busiest thread, normalized so 100% is one saturated core. It never exceeds CPU,
	// and equals it for a single-threaded process. MaxCoreExact is false when the
	// figure is CPU standing in as an upper bound because the process was below the
	// threads-sampling floor — the UI says so in the cell's tooltip.
	MaxCore      float64 `json:"max_core,omitempty"`
	MaxCoreExact bool    `json:"max_core_exact,omitempty"`
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
	// cores); CPUThread is the busiest single thread against one core (100% =
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

// Detail is a single service's view: its summary plus its checks.
type Detail struct {
	Service
	Checks            []Check        `json:"checks"`
	Locks             []Lock         `json:"locks,omitempty"`
	LockWarnings      []string       `json:"lock_warnings,omitempty"`
	NoResidentProcess bool           `json:"no_resident_process,omitempty"`
	ProcessWarnings   []string       `json:"process_warnings,omitempty"`
	Processes         []Process      `json:"processes,omitempty"`
	ProcessTotals     *ProcessTotals `json:"process_totals,omitempty"`
	Remediation       *Remediation   `json:"remediation,omitempty"`
	Rules             []RuleWindow   `json:"rules,omitempty"`
}

// SeriesPoint is one availability bucket of the SLA history. Ratio is nil for a
// bucket with no observed cycle. The bucket span is the resolution the requested
// window is stored at, so a point covers one minute on a short window and up to a
// day on the rolling year.
type SeriesPoint struct {
	Start       string   `json:"start"` // RFC3339, bucket-aligned
	Ratio       *float64 `json:"ratio"`
	Up          int64    `json:"up"`
	Total       int64    `json:"total"`
	DownBuckets int64    `json:"down_buckets"`
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

// CheckMetric is a graphable named metric a check publishes, including the
// metadata needed to summarize its latest sample and fetch its history.
type CheckMetric struct {
	Name  string `json:"name"`
	Unit  string `json:"unit"`
	Label string `json:"label,omitempty"`
	// Value is the latest fresh numeric sample. A pointer preserves the
	// difference between a measured zero and an absent/stale observation.
	Value *float64 `json:"value,omitempty"`
	// Band marks a state metric: drawn as an availability-style band from
	// /api/.../sla?metric=NAME, never as a line chart. Severity grades its
	// failing colour (error red, warning amber) and Label titles the panel.
	Band     bool   `json:"band,omitempty"`
	Severity string `json:"severity,omitempty"`
}

// ReadyReport is the /readyz readiness probe payload.
type ReadyReport struct {
	Ready    bool   `json:"ready"`
	Status   string `json:"status"` // ok | starting | shutting_down | panic mode
	Backend  string `json:"backend,omitempty"`
	Services int    `json:"services"`
	Watches  int    `json:"watches"`
	Message  string `json:"message,omitempty"`
	// Panic is true while the daemon-wide panic mode is on (hooks, alerts and
	// automatic remediation suspended). When true, Status is "panic mode".
	Panic bool `json:"panic,omitempty"`
}

// LiveReport is the verbose liveness payload embedded in DashboardSnapshot.
type LiveReport struct {
	Status        string `json:"status"`
	StartedAt     string `json:"started_at"`
	Now           string `json:"now"`
	Uptime        string `json:"uptime"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	Services      int    `json:"services"`
	Go            string `json:"go"`
}

// DashboardSnapshot combines the frequently refreshed, inexpensive dashboard
// sections. Existing section endpoints remain available for API clients and as
// a browser fallback when this aggregate request fails.
type DashboardSnapshot struct {
	// Generation identifies the daemon configuration generation that supplied
	// this snapshot. The UI uses it to reject follow-up data from another
	// generation while a reload is in progress.
	Generation    uint64           `json:"generation,omitempty"`
	Services      []Service        `json:"services"`
	Mounts        []Mount          `json:"mounts"`
	Notifiers     []Notifier       `json:"notifiers"`
	Daemon        DaemonInfo       `json:"daemon"`
	DaemonMetrics DaemonMetrics    `json:"daemon_metrics"`
	Locks         []Lock           `json:"locks"`
	Activity      ActivitySummary  `json:"activity"`
	Ready         ReadyReport      `json:"ready"`
	Live          LiveReport       `json:"live"`
	Monitoring    MonitoringStatus `json:"monitoring"`
	HostMetrics   []HostMetric     `json:"host_metrics"`
	Sessions      SessionInventory `json:"sessions"`
}

// ReadinessChecker reports whether the daemon has begun monitoring.
type ReadinessChecker interface {
	Report(ctx context.Context) ReadyReport
}

// Event is one recorded daemon event for the activity log.
type Event struct {
	ID      int64  `json:"id,omitempty"`
	Time    string `json:"time"` // RFC3339
	Service string `json:"service,omitempty"`
	Watch   string `json:"watch,omitempty"`
	App     string `json:"app,omitempty"`
	Kind    string `json:"kind"`
	Rule    string `json:"rule,omitempty"`
	Action  string `json:"action,omitempty"`
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
	// Output is the bounded stdout/stderr of the failing command behind this event
	// (app probe or service `command` check), shown expandable in the dashboard.
	Output string `json:"output,omitempty"`
}

// Target returns the event subject in the public precedence order: service,
// host watch, then catalog application. An event without a subject returns an
// empty string so each presenter can choose its own placeholder.
func (e Event) Target() string {
	switch {
	case e.Service != "":
		return e.Service
	case e.Watch != "":
		return e.Watch
	case e.App != "":
		return e.App
	default:
		return ""
	}
}

// EventQuery selects one cursor page from the global event feed.
type EventQuery struct {
	BeforeID   int64
	Limit      int
	Since      time.Duration
	Service    string
	Watch      string
	Kind       string
	Status     string
	OnlyErrors bool
}

// EventPage is a stable cursor page. NextBeforeID is passed as before_id to
// continue toward older events.
type EventPage struct {
	Events       []Event `json:"events"`
	NextBeforeID int64   `json:"next_before_id,omitempty"`
	HasMore      bool    `json:"has_more"`
}

// Backend is what the web server needs from the daemon.
//
// The surface is intentionally wide: one contract for the dashboard so the
// holder can swap implementations on reload without fragmenting callers.
//
//nolint:interfacebloat // dashboard API surface; splitting would fragment the web backend contract
type Backend interface {
	// Services returns the current view of every configured service (including those
	// with `enabled: false` in their YAML so they remain visible for activation).
	Services(ctx context.Context) []Service
	// Watches returns configured host-level and service-scoped watches (including
	// those with `enabled: false` so they remain visible in the dashboard).
	Watches(ctx context.Context) []Watch
	// Notifiers returns the named notifiers configured for use by watches.
	Notifiers(ctx context.Context) []Notifier
	// TestNotifier sends an explicit test message through one configured notifier.
	TestNotifier(ctx context.Context, name string) ActionResult
	// Applications returns the installed applications (catalog app daemons whose
	// binary is present), with their version and binary location.
	Applications(ctx context.Context) []Application
	// Libraries returns installed catalog libraries with their version and file
	// location, matching sermoctl libs.
	Libraries(ctx context.Context) []Library
	// Mounts returns configured fstab-backed mount units and their runtime status.
	Mounts(ctx context.Context) []Mount
	// MountAction runs mount|umount on a configured mount unit.
	MountAction(ctx context.Context, name, action string, opts MountActionOptions) MountActionResult
	// MountBlockers reports current processes using a configured mount unit.
	MountBlockers(ctx context.Context, name string) MountBlockersResult
	// AlertMountUsers sends a console alert to users blocking a mount unit.
	AlertMountUsers(ctx context.Context, name string) MountAlertResult
	// WatchMetrics returns one numeric series a host watch's check publishes; ok
	// is false for an unknown or disabled watch and for an unpublished metric.
	WatchMetrics(ctx context.Context, name, metric string, since time.Duration) (MetricSeries, bool)
	// Detail returns one service's checks and SLA; ok is false for unknown names.
	Detail(ctx context.Context, name string) (Detail, bool)
	// Series returns a service's per-minute availability history over since, or
	// one of its checks' when check is non-empty; ok is false for unknown names
	// and for a check the service does not define.
	Series(ctx context.Context, name, check, metric string, since time.Duration) ([]SeriesPoint, bool)
	// WatchSeries returns a host watch's per-minute availability history over
	// since; ok is false for an unknown watch and for one whose check asserts no
	// availability, which therefore has no uptime to serve.
	WatchSeries(ctx context.Context, name, metric string, since time.Duration) ([]SeriesPoint, bool)
	// Metrics returns a check's latency summary and per-minute history over since;
	// ok is false for unknown service names.
	Metrics(ctx context.Context, name, check, metric string, since time.Duration) (MetricSeries, bool)
	// ServiceRuntime returns process-tree CPU, memory and IO history for one
	// service over since; ok is false for unknown service names.
	ServiceRuntime(ctx context.Context, name string, since time.Duration) (ServiceRuntimeMetrics, bool)
	// EventPage returns one filtered cursor page from the global feed.
	EventPage(ctx context.Context, query EventQuery) EventPage
	// ServiceEvents returns up to limit recent events for one service, newest
	// first; ok is false for unknown names.
	ServiceEvents(ctx context.Context, name string, limit int) ([]Event, bool)
	// ApplicationEvents returns up to limit recent monitoring events for one
	// installed application, newest first; ok is false for unknown names.
	ApplicationEvents(ctx context.Context, name string, limit int) ([]Event, bool)
	// PruneEvents removes events older than 'before' (or all if zero time).
	// Intended for the `sermoctl events clear` command.
	PruneEvents(ctx context.Context, before time.Time) int
	// Operate runs start|stop|restart|reload|resume|repair on a service through the safe engine.
	Operate(ctx context.Context, name, action string, opts OperateOpts) ActionResult
	// ReapStrays signals the service's stray processes, gated by its own
	// reap.kill_only_if selector: with none declared it reports them and signals
	// nothing. It changes no unit state.
	ReapStrays(ctx context.Context, name string) ActionResult
	// CloseSSHSession gracefully closes one freshly verified terminal session
	// owned by the named SSH service.
	CloseSSHSession(ctx context.Context, name string, session SSHSession) ActionResult
	// CloseTerminalSession closes one freshly verified tmux or screen session.
	CloseTerminalSession(ctx context.Context, name string, session TerminalSession) ActionResult
	// CloseEmptyTerminalSession closes one freshly verified empty tmux server.
	CloseEmptyTerminalSession(ctx context.Context, name, check string) ActionResult
	// CompactState prunes persisted history older than before and vacuums the
	// state database. Zero before selects the normal retention window.
	CompactState(ctx context.Context, before time.Time) StateCompactResult
	// Preflight runs a service's preflight checks on demand; ok is false for
	// unknown names.
	Preflight(ctx context.Context, name string) (PreflightResult, bool)
	// SetMonitored pauses (false) or resumes (true) monitoring of a service.
	SetMonitored(ctx context.Context, name string, monitored bool) error
	// SetWatchMonitored pauses (false) or resumes (true) monitoring of a host watch.
	SetWatchMonitored(ctx context.Context, name string, monitored bool) error
	// ExpandWatch runs a configured storage watch's `then.expand` action on demand.
	ExpandWatch(ctx context.Context, name string) ActionResult
	// ProbeWatch runs a fresh, isolated read-only sample of a supported host watch.
	ProbeWatch(ctx context.Context, name string) ActionResult
	// ControlRAID pauses or resumes a configured RAID reconstruction.
	ControlRAID(ctx context.Context, name, action, confirmation string) ActionResult
	// ControlReplication starts a stopped replica for a replication watch.
	ControlReplication(ctx context.Context, name string) ActionResult
	// ServiceButton runs one configured operator button of a service.
	ServiceButton(ctx context.Context, service, button string) ActionResult
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
	// SetPanic enables (on=true) or disables the daemon-wide panic mode, which
	// suspends hooks, alerts and automatic remediation while monitoring keeps
	// running. The change is persisted so it survives daemon restarts.
	SetPanic(ctx context.Context, on bool) ActionResult
}
