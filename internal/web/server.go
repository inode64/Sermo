// Package web serves a small read-and-act dashboard for the daemon: it lists the
// monitored services with their status and lets an operator monitor/unmonitor and
// start/stop/restart/reload/resume them. It is deliberately minimal and depends on the daemon
// only through the Backend interface, so it stays decoupled and testable.
//
// Access is optional HTTP Basic auth with admin (read+act) and guest (read-only)
// roles; state-changing POST requests also require an X-Sermo-CSRF header. When
// no passwords are configured the UI is open — bind to a trusted interface
// (loopback by default) or set passwords / front it with an authenticating reverse
// proxy. GET /livez and GET /readyz are always public for health probes.
package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	htmlpkg "html"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"sermo/internal/buildinfo"
	"sermo/internal/httpx"
	"sermo/internal/logfile"
	"sermo/internal/mountctl"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/state"
)

//go:embed index.html
var assets embed.FS

const (
	headerCacheControl          = "Cache-Control"
	headerContentSecurityPolicy = "Content-Security-Policy"
	headerContentType           = httpx.HeaderContentType
	headerReferrerPolicy        = "Referrer-Policy"
	headerSermoCSRF             = "X-Sermo-CSRF"
	headerWWWAuthenticate       = "WWW-Authenticate"
	headerXContentTypeOptions   = "X-Content-Type-Options"
	headerXFrameOptions         = "X-Frame-Options"
	authBasicRealmSermo         = `Basic realm="Sermo"`
	contentTypeHTMLUTF8         = "text/html; charset=utf-8"
	contentTypeJSON             = httpx.ContentTypeJSON
	contentTypeTextUTF8         = "text/plain; charset=utf-8"
	headerValueDeny             = "DENY"
	headerValueNoCache          = "no-cache"
	headerValueNoReferrer       = "no-referrer"
	headerValueNoSniff          = "nosniff"
	cspNonceBytes               = 16
	cspFallbackNonceBase        = 36
	assetIndexHTML              = "index.html"
	templateNoncePlaceholder    = "{{CSP_NONCE}}"
	templateVersionPlaceholder  = "{{VERSION}}"
)

const (
	cspSeparator                   = "; "
	cspSourceSelf                  = "'self'"
	cspSourceNone                  = "'none'"
	cspSourceUnsafeInline          = "'unsafe-inline'"
	cspSourceData                  = "data:"
	cspNonceSourceSuffix           = "'"
	cspDirectiveDefaultSrc         = "default-src " + cspSourceSelf
	cspDirectiveScriptSrcPrefix    = "script-src " + cspSourceSelf + " 'nonce-"
	cspDirectiveScriptUnsafeInline = "script-src " + cspSourceSelf + " " + cspSourceUnsafeInline
	cspDirectiveStyleSrc           = "style-src " + cspSourceSelf + " " + cspSourceUnsafeInline
	cspDirectiveImgSrc             = "img-src " + cspSourceSelf + " " + cspSourceData
	cspDirectiveBaseURI            = "base-uri " + cspSourceNone
	cspDirectiveFormAction         = "form-action " + cspSourceSelf
	cspDirectiveFrameAncestors     = "frame-ancestors " + cspSourceNone
)

const (
	routePathRoot   = "/"
	routePathLivez  = "/livez"
	routePathReadyz = "/readyz"
	routePathLogin  = "/login"
	routePathAPI    = "/" + apiSegmentRoot
	apiPathPrefix   = routePathAPI + "/"
)

// API path segment names used by routing and access-log classification.
const (
	apiSegmentRoot         = "api"
	apiSegmentActivity     = "activity"
	apiSegmentApplications = "applications"
	apiSegmentDashboard    = "dashboard"
	apiSegmentDaemon       = "daemon"
	apiSegmentEvents       = "events"
	apiSegmentHost         = "host"
	apiSegmentLibraries    = "libraries"
	apiSegmentLocks        = "locks"
	apiSegmentMetrics      = "metrics"
	apiSegmentMonitoring   = "monitoring"
	apiSegmentMounts       = "mounts"
	apiSegmentNotifiers    = "notifiers"
	apiSegmentOps          = "ops"
	apiSegmentPanic        = "panic"
	apiSegmentPreflight    = "preflight"
	apiSegmentReload       = "reload"
	apiSegmentRuntime      = "runtime"
	apiSegmentServices     = "services"
	apiSegmentSLA          = "sla"
	apiSegmentState        = "state"
	apiSegmentWatches      = "watches"
	apiSegmentWhoami       = "whoami"
)

// HTTP action names accepted by the dashboard API.
const (
	apiActionStart     = string(rules.ActionStart)
	apiActionStop      = string(rules.ActionStop)
	apiActionRestart   = string(rules.ActionRestart)
	apiActionReload    = string(rules.ActionReload)
	apiActionResume    = string(rules.ActionResume)
	apiActionMonitor   = "monitor"
	apiActionUnmonitor = "unmonitor"
	apiActionExpand    = "expand"
	apiActionProbe     = "probe"
	apiActionPause     = "pause"
	apiActionPanicOn   = "on"
	apiActionPanicOff  = "off"
	apiActionRelease   = "release"
	apiActionClear     = "clear"
	apiActionCompact   = "compact"
	apiActionBlockers  = "blockers"
	apiActionAlert     = string(rules.ActionAlert)
	apiActionTest      = "test"

	queryBoolOne  = "1"
	queryBoolTrue = "true"
	queryBoolYes  = "yes"
	queryBoolOn   = "on"
)

const (
	apiErrorCheckQueryRequired       = "check query parameter is required"
	apiErrorEncodeResponse           = "failed to encode response"
	apiErrorPanicAction              = "panic action must be on or off"
	apiErrorReloadUnavailable        = "reload is not available for this daemon"
	apiErrorUnknownActionPrefix      = "unknown action "
	apiErrorUnknownApplication       = "unknown application"
	apiErrorUnknownMountActionPrefix = "unknown mount action "
	apiErrorUnknownService           = "unknown service"
	apiErrorUnknownServiceOrCheck    = "unknown service or check"
	apiMessageReloadRequested        = "reload requested"
)

// API route variables and query parameter names.
const (
	apiParamAction     = "action"
	apiParamName       = "name"
	apiParamService    = "service"
	apiQueryBefore     = "before"
	apiQueryBeforeID   = "before_id"
	apiQueryCheck      = "check"
	apiQueryForce      = "force"
	apiQueryKind       = "kind"
	apiQueryKill       = "kill"
	apiQueryLazy       = "lazy"
	apiQueryLimit      = "limit"
	apiQueryMetric     = "metric"
	apiQueryNoCascade  = "no_cascade"
	apiQueryOnlyErrors = "only_errors"
	apiQueryPage       = "page"
	apiQuerySince      = "since"
	apiQueryStatus     = "status"
	apiQueryVerbose    = "verbose"
	apiQueryWatch      = "watch"
)

const (
	routeMethodGet  = http.MethodGet + " "
	routeMethodPost = http.MethodPost + " "
	routeVarAction  = "{" + apiParamAction + "}"
	routeVarName    = "{" + apiParamName + "}"
	routeVarService = "{" + apiParamService + "}"
)

const (
	apiPathActivity     = apiPathPrefix + apiSegmentActivity
	apiPathApplications = apiPathPrefix + apiSegmentApplications
	apiPathDashboard    = apiPathPrefix + apiSegmentDashboard
	apiPathDaemon       = apiPathPrefix + apiSegmentDaemon
	apiPathEvents       = apiPathPrefix + apiSegmentEvents
	apiPathHost         = apiPathPrefix + apiSegmentHost
	apiPathLibraries    = apiPathPrefix + apiSegmentLibraries
	apiPathLocks        = apiPathPrefix + apiSegmentLocks
	apiPathMonitoring   = apiPathPrefix + apiSegmentMonitoring
	apiPathMounts       = apiPathPrefix + apiSegmentMounts
	apiPathNotifiers    = apiPathPrefix + apiSegmentNotifiers
	apiPathOps          = apiPathPrefix + apiSegmentOps
	apiPathPanic        = apiPathPrefix + apiSegmentPanic
	apiPathReload       = apiPathPrefix + apiSegmentReload
	apiPathServices     = apiPathPrefix + apiSegmentServices
	apiPathState        = apiPathPrefix + apiSegmentState
	apiPathWatches      = apiPathPrefix + apiSegmentWatches
	apiPathWhoami       = apiPathPrefix + apiSegmentWhoami
)

const (
	routeIndex             = routeMethodGet + routePathRoot
	routeLivez             = routeMethodGet + routePathLivez
	routeReadyz            = routeMethodGet + routePathReadyz
	routeAPIWhoami         = routeMethodGet + apiPathWhoami
	routeAPIServices       = routeMethodGet + apiPathServices
	routeAPIWatches        = routeMethodGet + apiPathWatches
	routeAPIWatchAction    = routeMethodPost + apiPathWatches + "/" + routeVarName + "/" + routeVarAction
	routeAPINotifiers      = routeMethodGet + apiPathNotifiers
	routeAPINotifierTest   = routeMethodPost + apiPathNotifiers + "/" + routeVarName + "/" + apiActionTest
	routeAPIApplications   = routeMethodGet + apiPathApplications
	routeAPILibraries      = routeMethodGet + apiPathLibraries
	routeAPIDashboard      = routeMethodGet + apiPathDashboard
	routeAPIMounts         = routeMethodGet + apiPathMounts
	routeAPIMountAction    = routeMethodPost + apiPathMounts + "/" + routeVarName + "/" + routeVarAction
	routeAPIDaemon         = routeMethodGet + apiPathDaemon
	routeAPIDaemonMetrics  = routeMethodGet + apiPathDaemon + "/" + apiSegmentMetrics
	routeAPIHost           = routeMethodGet + apiPathHost
	routeAPILocks          = routeMethodGet + apiPathLocks
	routeAPILockRelease    = routeMethodPost + apiPathLocks + "/" + routeVarService + "/" + apiActionRelease
	routeAPIActivity       = routeMethodGet + apiPathActivity
	routeAPIMonitoring     = routeMethodGet + apiPathMonitoring
	routeAPIDetail         = routeMethodGet + apiPathServices + "/" + routeVarName
	routeAPISeries         = routeMethodGet + apiPathServices + "/" + routeVarName + "/" + apiSegmentSLA
	routeAPIMetrics        = routeMethodGet + apiPathServices + "/" + routeVarName + "/" + apiSegmentMetrics
	routeAPIServiceRuntime = routeMethodGet + apiPathServices + "/" + routeVarName + "/" + apiSegmentRuntime
	routeAPIServiceEvents  = routeMethodGet + apiPathServices + "/" + routeVarName + "/" + apiSegmentEvents
	routeAPIAppEvents      = routeMethodGet + apiPathApplications + "/" + routeVarName + "/" + apiSegmentEvents
	routeAPIEvents         = routeMethodGet + apiPathEvents
	routeAPIEventsClear    = routeMethodPost + apiPathEvents + "/" + apiActionClear
	routeAPIStateCompact   = routeMethodPost + apiPathState + "/" + apiActionCompact
	routeAPIPanic          = routeMethodPost + apiPathPanic + "/" + routeVarAction
	routeAPIOps            = routeMethodGet + apiPathOps
	routeAPIPreflight      = routeMethodPost + apiPathServices + "/" + routeVarName + "/" + apiSegmentPreflight
	routeAPIAction         = routeMethodPost + apiPathServices + "/" + routeVarName + "/" + routeVarAction
	routeAPIReload         = routeMethodPost + apiPathReload
)

// Ad-hoc JSON keys used by small HTTP responses without a dedicated struct.
const (
	apiJSONKeyGo            = "go"
	apiJSONKeyNow           = "now"
	apiJSONKeyOK            = "ok"
	apiJSONKeyPoints        = "points"
	apiJSONKeyPruned        = "pruned"
	apiJSONKeyServices      = "services"
	apiJSONKeySince         = "since"
	apiJSONKeyStartedAt     = "started_at"
	apiJSONKeyStatus        = "status"
	apiJSONKeyUptime        = "uptime"
	apiJSONKeyUptimeSeconds = "uptime_seconds"
	apiStatusOK             = string(operation.ResultOK)
	apiStatusOKLine         = apiStatusOK + "\n"
)

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
	CheckHealth          string   `json:"check_health,omitempty"`          // ok | failing | unknown | paused | disabled
	ChecksFailing        int      `json:"checks_failing,omitempty"`        // required checks currently failing
	ObservabilityReady   bool     `json:"observability_ready"`             // true when monitored service has fresh visible indicators
	ObservabilityMissing []string `json:"observability_missing,omitempty"` // indicator groups still collecting
	ActiveLocks          []string `json:"active_locks,omitempty"`          // named runtime locks blocking actions
	PolicyCooldown       string   `json:"policy_cooldown,omitempty"`       // resolved automatic remediation cooldown
	RemediationState     string   `json:"remediation_state,omitempty"`     // eligible | cooldown | rate limit | paused | pending | disabled
	NextEligibleAt       string   `json:"next_eligible_at,omitempty"`      // RFC3339 when automatic remediation is next eligible
	CanReload            bool     `json:"can_reload"`                      // true when init or native reload support is available
	LastEvent            *Event   `json:"last_event,omitempty"`            // newest service event, when retained

	// Current process-tree runtime summary. These fields intentionally mirror
	// ProcessTotals so the service list and detail expansion use the same
	// semantics: matched processes plus their child/descendant processes.
	NoResidentProcess bool     `json:"no_resident_process,omitempty"` // true for oneshot/helper services with no resident process tree
	StartedAt         string   `json:"started_at,omitempty"`          // oldest discovered process start time, RFC3339
	Uptime            string   `json:"uptime,omitempty"`              // display-ready age of StartedAt
	UptimeSeconds     int64    `json:"uptime_seconds,omitempty"`
	ProcessCount      int      `json:"process_count,omitempty"`
	RSS               int64    `json:"rss,omitempty"`
	IORead            int64    `json:"io_read,omitempty"`  // cumulative disk read bytes
	IOWrite           int64    `json:"io_write,omitempty"` // cumulative disk write bytes
	FDs               int64    `json:"fds,omitempty"`
	Threads           int64    `json:"threads,omitempty"`
	CPU               float64  `json:"cpu,omitempty"`        // live CPU %, all host CPUs
	CPUThread         float64  `json:"cpu_thread,omitempty"` // busiest process, single-core normalized
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
	Path          string         `json:"path,omitempty"`
	Mounted       bool           `json:"mounted"`
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
	OK        bool     `json:"ok"`
	Name      string   `json:"name,omitempty"`
	Path      string   `json:"path,omitempty"`
	Users     []string `json:"users,omitempty"`
	Delivered int      `json:"delivered"`
	Message   string   `json:"message,omitempty"`
}

// CatalogItem is the shared web view of one installed catalog application or
// library. It mirrors the sermoctl `apps` and `libs` reports so every surface
// agrees about versions, locations and inspection status.
type CatalogItem struct {
	Name          string      `json:"name"`
	DisplayName   string      `json:"display_name"`
	Category      string      `json:"category,omitempty"`
	Binary        string      `json:"binary"`                   // resolved binary path (file location)
	Permissions   string      `json:"permissions,omitempty"`    // binary mode, e.g. "-rwxr-xr-x (0755)"
	User          string      `json:"user,omitempty"`           // owner username of the binary
	Group         string      `json:"group,omitempty"`          // owner group of the binary
	Version       string      `json:"version"`                  // raw first line of the version command
	VersionShort  string      `json:"version_short"`            // numeric version, at most the patchlevel
	VersionSource string      `json:"version_source,omitempty"` // app whose version probe supplied this version
	Status        string      `json:"status"`                   // ok, or an error description
	State         string      `json:"state,omitempty"`          // starting | ok | failed | warning
	ObservedAt    string      `json:"observed_at,omitempty"`    // RFC3339 when version/status probes actually ran
	SLA           []SLAWindow `json:"sla,omitempty"`            // populated when an application maps to a monitored service
	LastEvent     *Event      `json:"last_event,omitempty"`     // populated with the newest retained application event
}

// Application is an installed catalog application returned by the dashboard.
type Application = CatalogItem

// Library is an installed catalog library returned by the dashboard.
type Library = CatalogItem

// Watch is a view of a host watch for the dashboard (when services=0
// the watches section is the main thing to show). Enriched with useful
// runtime/config info for operators.
type Watch struct {
	Name             string            `json:"name"`
	DisplayName      string            `json:"display_name,omitempty"`
	Category         string            `json:"category,omitempty"`
	CheckType        string            `json:"check_type,omitempty"`
	Summary          string            `json:"summary,omitempty"`
	Interval         string            `json:"interval,omitempty"`
	State            string            `json:"state"`
	Enabled          bool              `json:"enabled"`
	Monitor          string            `json:"monitor,omitempty"` // enabled | disabled | previous
	Monitored        bool              `json:"monitored"`
	MonitorSource    string            `json:"monitor_source,omitempty"`
	MonitorChangedAt string            `json:"monitor_changed_at,omitempty"`
	FireOnFail       bool              `json:"fire_on_fail"` // true = fires when check fails (e.g. health checks); false = fires on condition (e.g. load/storage)
	HasHook          bool              `json:"has_hook"`
	HookCommand      []string          `json:"hook_command,omitempty"`
	Notifiers        []string          `json:"notifiers,omitempty"`
	NotifierCount    int               `json:"notifier_count"`
	DryRun           bool              `json:"dry_run"`
	Conditions       []WatchCondition  `json:"conditions,omitempty"`
	Storage          *StorageWatchInfo `json:"storage,omitempty"`
	Swap             *SwapWatchInfo    `json:"swap,omitempty"`
	Meter            *WatchMeter       `json:"meter,omitempty"`
	Readings         []WatchReading    `json:"readings,omitempty"`
	Expand           *WatchExpand      `json:"expand,omitempty"`
	CanProbe         bool              `json:"can_probe,omitempty"`
	CanControlRAID   bool              `json:"can_control_raid,omitempty"`
	RAIDArray        string            `json:"raid_array,omitempty"`
	LastActivity     string            `json:"last_activity,omitempty"` // RFC3339 of last watch activity, if any
	LastActivityKind string            `json:"last_activity_kind,omitempty"`
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

// StorageWatchInfo is live filesystem data for a storage host watch.
type StorageWatchInfo struct {
	Path          string   `json:"path"`
	Mounted       bool     `json:"mounted"`
	MountPoint    string   `json:"mount_point,omitempty"`
	Device        string   `json:"device,omitempty"`
	FileSystem    string   `json:"filesystem,omitempty"`
	Options       []string `json:"options,omitempty"`
	TotalBytes    uint64   `json:"total_bytes,omitempty"`
	UsedBytes     uint64   `json:"used_bytes,omitempty"`
	FreeBytes     uint64   `json:"free_bytes,omitempty"`
	UsedPct       float64  `json:"used_pct,omitempty"`
	FreePct       float64  `json:"free_pct,omitempty"`
	InodesTotal   uint64   `json:"inodes_total,omitempty"`
	InodesFree    uint64   `json:"inodes_free,omitempty"`
	InodesUsedPct float64  `json:"inodes_used_pct,omitempty"`
	InodesFreePct float64  `json:"inodes_free_pct,omitempty"`
	// OpenFiles is the number of open file descriptors on this mount's
	// filesystem (fds whose target resolves to an absolute path under the mount).
	// Display only; computed by a cached host-wide /proc scan, so 0 may mean
	// "none" or "not yet sampled".
	OpenFiles        int64  `json:"open_files,omitempty"`
	SampleError      string `json:"sample_error,omitempty"`
	MountSampleError string `json:"mount_sample_error,omitempty"`
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
	Backend               string        `json:"backend,omitempty"`
	Hostname              string        `json:"hostname,omitempty"`
	OS                    string        `json:"os,omitempty"`
	HostType              *HostTypeInfo `json:"host_type,omitempty"`
	HostUptime            string        `json:"host_uptime,omitempty"`         // display-ready uptime of the host/server since boot
	HostUptimeSeconds     int64         `json:"host_uptime_seconds,omitempty"` // host/server uptime in whole seconds
	ConfigPath            string        `json:"config_path,omitempty"`
	RuntimeDir            string        `json:"runtime_dir,omitempty"`
	StateDir              string        `json:"state_dir,omitempty"`
	Interval              string        `json:"interval"`
	MaxParallelChecks     int           `json:"max_parallel_checks"`
	MaxParallelOperations int           `json:"max_parallel_operations"`
	DefaultTimeout        string        `json:"default_timeout"`
	OperationTimeout      string        `json:"operation_timeout"`
	StartupDelay          string        `json:"startup_delay"`
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
	TotalEvents      int    `json:"total_events"`
	ServiceActions   int    `json:"service_actions"` // start/stop/restart/reload/resume
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
	OK       bool           `json:"ok"`
	Message  string         `json:"message,omitempty"`
	Readings []WatchReading `json:"readings,omitempty"`
}

// OperateOpts controls optional service-operation behavior from the web API.
type OperateOpts struct {
	NoCascade bool // skip also_apply cascade targets
}

// StateCompactResult is the outcome of pruning old persisted history and
// vacuuming the SQLite state database.
type StateCompactResult struct {
	OK             bool   `json:"ok"`
	Message        string `json:"message,omitempty"`
	Pruned         int64  `json:"pruned"`
	Before         string `json:"before,omitempty"` // RFC3339 cutoff
	SLA            int64  `json:"sla,omitempty"`
	Measurements   int64  `json:"measurements,omitempty"`
	Metrics        int64  `json:"metrics,omitempty"`
	DaemonMetrics  int64  `json:"daemon_metrics,omitempty"`
	ServiceMetrics int64  `json:"service_metrics,omitempty"`
	Events         int64  `json:"events,omitempty"`
	Vacuum         bool   `json:"vacuum"`
}

// PreflightResult is the outcome of an on-demand preflight run.
type PreflightResult struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// Check is one check's latest observed result in a service detail.
type Check struct {
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	OK       bool           `json:"ok"`
	Optional bool           `json:"optional"`
	Skipped  bool           `json:"skipped,omitempty"` // gated off (requires/skip_when_changed)
	Message  string         `json:"message,omitempty"`
	Readings []WatchReading `json:"readings,omitempty"`
	Ran      bool           `json:"ran"`          // false if not observed yet
	At       string         `json:"at,omitempty"` // RFC3339 when the check last ran (cached checks keep prior time)
	// Metrics are the check's graphable named series (time-series), if any.
	Metrics []CheckMetric `json:"metrics,omitempty"`
	SLA     []SLAWindow   `json:"sla,omitempty"`
}

// SLAWindow is a service's availability over one rolling window. Ratio is nil
// when the window has no data. Segments is the window split into equal sub-spans
// (oldest first) for the timeline strip; each entry is that sub-span's ratio in
// [0,1], or nil for a gap (no cycle observed in it).
type SLAWindow struct {
	Window     string     `json:"window"`
	Ratio      *float64   `json:"ratio"`
	Up         int64      `json:"up"`
	Total      int64      `json:"total"`
	Segments   []*float64 `json:"segments,omitempty"`
	ObservedAt string     `json:"observed_at,omitempty"` // RFC3339 when this rolling window was calculated
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

// OperationSlots is the global start/stop/restart/reload/resume concurrency pool.
type OperationSlots struct {
	InUse int `json:"in_use"`
	Total int `json:"total"`
	// ActiveUsers is the number of distinct users with an active login session,
	// surfaced on this payload so the header can show it alongside slot usage.
	ActiveUsers int `json:"active_users"`
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
	GeneratedAt   string           `json:"generated_at"`
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
	Operations    OperationSlots   `json:"operations"`
	HostMetrics   []HostMetric     `json:"host_metrics"`
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

const (
	eventKindAction         = "action"
	eventKindAlert          = string(rules.ActionAlert)
	eventKindError          = "error"
	eventKindHook           = "hook"
	eventKindFailedFragment = string(operation.ResultFailed)
	eventKindHookFailed     = eventKindHook + "-" + eventKindFailedFragment
	eventKindRecovery       = "recovery"
	eventStatusOK           = apiStatusOK
	eventStatusError        = "error"
	eventStatusFailed       = string(operation.ResultFailed)
)

// maxSeriesWindow bounds the history a single request may ask for (the retention).
const maxSeriesWindow = state.DefaultHistoryRetention

// defaultEventLimit / maxEventLimit bound how many log events a request returns.
const defaultEventLimit = 100
const maxEventLimit = 1000

// defaultSeriesWindow is used when no (or an invalid) `since` is given.
const defaultSeriesWindow = state.DefaultSeriesWindow

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
	// EventPage returns one filtered cursor page from the global feed.
	EventPage(ctx context.Context, query EventQuery) EventPage
	// Operations reports how many global operation slots are in use.
	Operations(ctx context.Context) OperationSlots
	// ServiceEvents returns up to limit recent events for one service, newest
	// first; ok is false for unknown names.
	ServiceEvents(ctx context.Context, name string, limit int) ([]Event, bool)
	// ApplicationEvents returns up to limit recent monitoring events for one
	// installed application, newest first; ok is false for unknown names.
	ApplicationEvents(ctx context.Context, name string, limit int) ([]Event, bool)
	// PruneEvents removes events older than 'before' (or all if zero time).
	// Intended for the `sermoctl events clear` command.
	PruneEvents(ctx context.Context, before time.Time) int
	// Operate runs start|stop|restart|reload|resume on a service through the safe engine.
	Operate(ctx context.Context, name, action string, opts OperateOpts) ActionResult
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

// operateActions, monitorActions and watchOperateActions are the action verbs the API accepts.
var operateActions = map[string]bool{
	apiActionStart:   true,
	apiActionStop:    true,
	apiActionRestart: true,
	apiActionReload:  true,
	apiActionResume:  true,
}
var monitorActions = map[string]bool{apiActionMonitor: true, apiActionUnmonitor: true}
var watchOperateActions = map[string]bool{apiActionExpand: true, apiActionProbe: true, apiActionPause: true, apiActionResume: true}

// defaultOperationTimeout matches operation.DefaultOperationTimeout when sermod
// does not set OperationTimeout on the server.
const defaultOperationTimeout = operation.DefaultOperationTimeout

// writeTimeoutMargin is added to OperationTimeout so the handler can finish
// writing the JSON response after a long operation completes.
const writeTimeoutMargin = 5 * time.Second

const (
	serverReadHeaderTimeout = 5 * time.Second
	serverReadTimeout       = 15 * time.Second
	serverIdleTimeout       = 60 * time.Second
	serverShutdownTimeout   = 5 * time.Second
)

// minWriteTimeout keeps short read-only requests bounded when OperationTimeout
// is unusually small.
const minWriteTimeout = 30 * time.Second

// Server is the HTTP dashboard. Addr is a host:port; Backend is required. Auth is
// optional (zero value = open). OperationTimeout bounds how long start/stop/restart/reload/resume
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
	Reload func() error

	// AccessLog appends operator POST audit records when engine.access is set.
	AccessLog *logfile.Writer

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
	mux.HandleFunc(routeIndex, s.handleIndex)
	mux.HandleFunc(routeLivez, s.handleLivez)
	mux.HandleFunc(routeReadyz, s.handleReadyz)
	mux.HandleFunc(routeAPIWhoami, s.handleWhoami)
	mux.HandleFunc(routeAPIDashboard, s.handleDashboard)
	mux.HandleFunc(routeAPIServices, s.handleServices)
	mux.HandleFunc(routeAPIWatches, s.handleWatches)
	mux.HandleFunc(routeAPIWatchAction, s.handleWatchAction)
	mux.HandleFunc(routeAPINotifiers, s.handleNotifiers)
	mux.HandleFunc(routeAPINotifierTest, s.handleNotifierTest)
	mux.HandleFunc(routeAPIApplications, s.handleApplications)
	mux.HandleFunc(routeAPILibraries, s.handleLibraries)
	mux.HandleFunc(routeAPIMounts, s.handleMounts)
	mux.HandleFunc(routeAPIMountAction, s.handleMountAction)
	mux.HandleFunc(routeAPIDaemon, s.handleDaemon)
	mux.HandleFunc(routeAPIDaemonMetrics, s.handleDaemonMetrics)
	mux.HandleFunc(routeAPIHost, s.handleHost)
	mux.HandleFunc(routeAPILocks, s.handleLocks)
	mux.HandleFunc(routeAPILockRelease, s.handleLockRelease)
	mux.HandleFunc(routeAPIActivity, s.handleActivity)
	mux.HandleFunc(routeAPIMonitoring, s.handleMonitoring)
	mux.HandleFunc(routeAPIDetail, s.handleDetail)
	mux.HandleFunc(routeAPISeries, s.handleSeries)
	mux.HandleFunc(routeAPIMetrics, s.handleMetrics)
	mux.HandleFunc(routeAPIServiceRuntime, s.handleServiceRuntime)
	mux.HandleFunc(routeAPIServiceEvents, s.handleServiceEvents)
	mux.HandleFunc(routeAPIAppEvents, s.handleApplicationEvents)
	mux.HandleFunc(routeAPIEvents, s.handleEvents)
	mux.HandleFunc(routeAPIEventsClear, s.handleEventsClear)
	mux.HandleFunc(routeAPIStateCompact, s.handleStateCompact)
	mux.HandleFunc(routeAPIPanic, s.handlePanic)
	mux.HandleFunc(routeAPIOps, s.handleOperations)
	mux.HandleFunc(routeAPIPreflight, s.handlePreflight)
	mux.HandleFunc(routeAPIAction, s.handleAction)
	mux.HandleFunc(routeAPIReload, s.handleReload)
	return securityHeaders(s.withAccessLog(s.withAuth(mux)))
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
		h.Set(headerXContentTypeOptions, headerValueNoSniff)
		h.Set(headerXFrameOptions, headerValueDeny)
		h.Set(headerReferrerPolicy, headerValueNoReferrer)
		h.Set(headerContentSecurityPolicy, contentSecurityPolicy(nonce))
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), cspNonceCtxKey{}, nonce)))
	})
}

func contentSecurityPolicy(nonce string) string {
	return strings.Join([]string{
		cspDirectiveDefaultSrc,
		cspDirectiveScriptSrcPrefix + nonce + cspNonceSourceSuffix,
		cspDirectiveStyleSrc,
		cspDirectiveImgSrc,
		cspDirectiveBaseURI,
		cspDirectiveFormAction,
		cspDirectiveFrameAncestors,
	}, cspSeparator)
}

func cspNonce() string {
	var b [cspNonceBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), cspFallbackNonceBase)
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
	if q := r.URL.Query().Get(apiQueryLimit); q != "" {
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
		Service:    q.Get(apiParamService),
		Watch:      q.Get(apiQueryWatch),
		Kind:       q.Get(apiQueryKind),
		Status:     q.Get(apiQueryStatus),
		OnlyErrors: truthy(q.Get(apiQueryOnlyErrors)),
	}
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case queryBoolOne, queryBoolTrue, queryBoolYes, queryBoolOn:
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
	if e.Kind == eventKindError || strings.Contains(e.Kind, eventKindFailedFragment) {
		return true
	}
	switch e.Status {
	case string(operation.ResultFailed),
		eventStatusError,
		string(operation.ResultBlocked),
		string(operation.ResultOrphanProcesses),
		string(operation.ResultPreflightFailed),
		string(operation.ResultPostflightFailed):
		return true
	default:
		return false
	}
}

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

// operateContext returns a context for start/stop/restart/reload/resume that is not tied to the
// HTTP request. Client disconnect and the generic write deadline must not abort
// an in-flight safe operation; the operation engine applies its own timeout.
func (s *Server) operateContext(r *http.Request) context.Context {
	if s.shutdown != nil {
		return context.WithoutCancel(s.shutdown)
	}
	return context.WithoutCancel(r.Context())
}

// Run serves until ctx is cancelled, then shuts down gracefully. Timeouts bound
// slow clients (the server runs as root, so it is hardened by default).
func (s *Server) Run(ctx context.Context) error {
	s.shutdown = ctx
	srv := &http.Server{
		Addr:              s.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout(s.OperationTimeout),
		IdleTimeout:       serverIdleTimeout,
	}
	go func() { //nolint:gosec // G118: the shutdown deadline must NOT derive from ctx — it is already cancelled here
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(shutCtx) //nolint:contextcheck // detached shutdown deadline; ctx is already cancelled
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("web server listen: %w", err)
	}
	return nil
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := assets.ReadFile(assetIndexHTML)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	html := strings.ReplaceAll(string(page), templateNoncePlaceholder, cspNonceFrom(r.Context()))
	page = []byte(strings.ReplaceAll(html, templateVersionPlaceholder, htmlpkg.EscapeString(buildinfo.Short())))
	w.Header().Set(headerContentType, contentTypeHTMLUTF8)
	// The dashboard markup/JS is embedded in the binary and changes across
	// versions (new sections like host watches are added over time). Without a
	// cache directive a browser may keep serving a stale copy after an upgrade,
	// so newly added sections never appear even though the API returns their
	// data. no-cache forces a revalidation on every load.
	w.Header().Set(headerCacheControl, headerValueNoCache)
	_, _ = w.Write(page)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.dashboardSnapshot(r.Context(), seriesSince(r)))
}

func (s *Server) dashboardSnapshot(ctx context.Context, since time.Duration) DashboardSnapshot {
	var snapshot DashboardSnapshot
	var wg sync.WaitGroup
	run := func(fn func()) {
		wg.Go(func() {
			fn()
		})
	}

	run(func() { snapshot.Services = s.Backend.Services(ctx) })
	run(func() { snapshot.Mounts = s.Backend.Mounts(ctx) })
	run(func() { snapshot.Notifiers = s.Backend.Notifiers(ctx) })
	run(func() { snapshot.Daemon = s.Backend.DaemonInfo(ctx) })
	run(func() { snapshot.DaemonMetrics = s.Backend.DaemonMetrics(ctx, since) })
	run(func() { snapshot.Locks = s.Backend.Locks(ctx) })
	run(func() { snapshot.Activity = s.Backend.ActivitySummary(ctx) })
	run(func() { snapshot.Monitoring = s.Backend.MonitoringStatus(ctx) })
	run(func() { snapshot.Operations = s.Backend.Operations(ctx) })
	run(func() { snapshot.HostMetrics = s.Backend.HostMetrics(ctx) })
	if s.Readiness != nil {
		run(func() { snapshot.Ready = s.Readiness.Report(ctx) })
	}
	wg.Wait()

	if s.Readiness == nil {
		snapshot.Ready = ReadyReport{Ready: true, Status: apiStatusOK, Services: len(snapshot.Services)}
	}
	now := time.Now()
	uptime := now.Sub(s.started)
	snapshot.GeneratedAt = now.UTC().Format(time.RFC3339)
	snapshot.Live = LiveReport{
		Status:        apiStatusOK,
		StartedAt:     s.started.Format(time.RFC3339),
		Now:           now.Format(time.RFC3339),
		Uptime:        uptime.Round(time.Second).String(),
		UptimeSeconds: int64(uptime.Seconds()),
		Services:      len(snapshot.Services),
		Go:            runtime.Version(),
	}
	return snapshot
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

func (s *Server) handleNotifierTest(w http.ResponseWriter, r *http.Request) {
	res := s.Backend.TestNotifier(s.operateContext(r), r.PathValue(apiParamName)) //nolint:contextcheck // see operateContext
	status := http.StatusOK
	if !res.OK {
		status = http.StatusConflict
	}
	writeJSON(w, status, res)
}

func (s *Server) handleApplications(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.Applications(r.Context()))
}

func (s *Server) handleLibraries(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.Libraries(r.Context()))
}

func (s *Server) handleMounts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.Mounts(r.Context()))
}

func (s *Server) handleMountAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue(apiParamName)
	action := r.PathValue(apiParamAction)
	switch action {
	case mountctl.ActionMount, mountctl.ActionUmount:
		res := s.Backend.MountAction(s.operateContext(r), name, action, MountActionOptions{ //nolint:contextcheck // see operateContext
			AllowForce:   queryBool(r, apiQueryForce),
			AllowLazy:    queryBool(r, apiQueryLazy),
			KillBlockers: queryBool(r, apiQueryKill),
		})
		status := http.StatusOK
		if !res.OK {
			status = http.StatusConflict
		}
		writeJSON(w, status, res)
	case apiActionBlockers:
		res := s.Backend.MountBlockers(r.Context(), name)
		status := http.StatusOK
		if !res.OK {
			status = http.StatusConflict
		}
		writeJSON(w, status, res)
	case apiActionAlert:
		res := s.Backend.AlertMountUsers(s.operateContext(r), name) //nolint:contextcheck // see operateContext
		status := http.StatusOK
		if !res.OK {
			status = http.StatusConflict
		}
		writeJSON(w, status, res)
	default:
		writeError(w, http.StatusBadRequest, apiErrorUnknownMountActionPrefix+action)
	}
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
	res := s.Backend.ReleaseLock(r.Context(), r.PathValue(apiParamService), r.URL.Query().Get(apiParamName))
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
	detail, ok := s.Backend.Detail(r.Context(), r.PathValue(apiParamName))
	if !ok {
		writeError(w, http.StatusNotFound, apiErrorUnknownService)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// seriesSince reads the `since` query param, defaulting and capping it.
func seriesSince(r *http.Request) time.Duration {
	since := defaultSeriesWindow
	if q := r.URL.Query().Get(apiQuerySince); q != "" {
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
	points, ok := s.Backend.Series(r.Context(), r.PathValue(apiParamName), since)
	if !ok {
		writeError(w, http.StatusNotFound, apiErrorUnknownService)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{apiJSONKeySince: since.String(), apiJSONKeyPoints: points})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	check := r.URL.Query().Get(apiQueryCheck)
	if check == "" {
		writeError(w, http.StatusBadRequest, apiErrorCheckQueryRequired)
		return
	}
	res, ok := s.Backend.Metrics(r.Context(), r.PathValue(apiParamName), check, r.URL.Query().Get(apiQueryMetric), seriesSince(r))
	if !ok {
		writeError(w, http.StatusNotFound, apiErrorUnknownServiceOrCheck)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleServiceRuntime(w http.ResponseWriter, r *http.Request) {
	res, ok := s.Backend.ServiceRuntime(r.Context(), r.PathValue(apiParamName), seriesSince(r))
	if !ok {
		writeError(w, http.StatusNotFound, apiErrorUnknownService)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	limit := eventLimit(r)
	filter := parseEventFilter(r)
	if queryBool(r, apiQueryPage) || r.URL.Query().Has(apiQueryBeforeID) {
		beforeID, err := eventBeforeID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		since, err := eventSince(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, s.Backend.EventPage(r.Context(), EventQuery{
			BeforeID: beforeID, Limit: limit, Since: since, Service: filter.Service, Watch: filter.Watch,
			Kind: filter.Kind, Status: filter.Status, OnlyErrors: filter.OnlyErrors,
		}))
		return
	}
	fetchLimit := limit
	if filter.active() {
		fetchLimit = maxEventLimit
	}
	writeJSON(w, http.StatusOK, filterEvents(s.Backend.Events(r.Context(), fetchLimit), filter, limit))
}

func eventSince(r *http.Request) (time.Duration, error) {
	raw := r.URL.Query().Get(apiQuerySince)
	if raw == "" {
		return 0, nil
	}
	since, err := time.ParseDuration(raw)
	if err != nil || since <= 0 {
		return 0, fmt.Errorf("bad %s: must be a positive duration", apiQuerySince)
	}
	return since, nil
}

func eventBeforeID(r *http.Request) (int64, error) {
	raw := r.URL.Query().Get(apiQueryBeforeID)
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("bad %s: must be a positive integer", apiQueryBeforeID)
	}
	return id, nil
}

// queryBool reports whether the query parameter key is set to a truthy value
// ("1", "true" or "yes", case-insensitive).
func queryBool(r *http.Request, key string) bool {
	v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key)))
	return v == queryBoolOne || v == queryBoolTrue || v == queryBoolYes
}

func parseBeforeQuery(beforeStr string) (time.Time, error) {
	if beforeStr == "" {
		return time.Time{}, nil
	}
	now := time.Now()
	if t, err := time.Parse(time.RFC3339, beforeStr); err == nil {
		if t.After(now) {
			return time.Time{}, fmt.Errorf("bad before: cutoff must not be in the future")
		}
		return t, nil
	}
	if d, err := time.ParseDuration(beforeStr); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("bad before: duration must be positive")
		}
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("bad before: RFC3339 timestamp or duration (e.g. 1h, 30m)")
}

// handleEventsClear supports `sermoctl events clear [--before TIME]`.
// TIME may be a non-future RFC3339 timestamp or a positive duration (e.g. "2h"
// means "before now-2h").
func (s *Server) handleEventsClear(w http.ResponseWriter, r *http.Request) {
	before, err := parseBeforeQuery(r.URL.Query().Get(apiQueryBefore))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ActionResult{OK: false, Message: err.Error()})
		return
	}
	n := s.Backend.PruneEvents(r.Context(), before)
	writeJSON(w, http.StatusOK, map[string]any{
		apiJSONKeyOK:     true,
		apiJSONKeyPruned: n,
	})
}

func (s *Server) handleStateCompact(w http.ResponseWriter, r *http.Request) {
	before, err := parseBeforeQuery(r.URL.Query().Get(apiQueryBefore))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, StateCompactResult{OK: false, Message: err.Error()})
		return
	}
	res := s.Backend.CompactState(s.operateContext(r), before) //nolint:contextcheck // see operateContext
	status := http.StatusOK
	if !res.OK {
		status = http.StatusConflict
	}
	writeJSON(w, status, res)
}

// handlePanic enables (action "on") or disables (action "off") the daemon-wide
// panic mode. It is admin-only (POST gated by withAuth) and CSRF-protected.
func (s *Server) handlePanic(w http.ResponseWriter, r *http.Request) {
	var on bool
	switch r.PathValue(apiParamAction) {
	case apiActionPanicOn:
		on = true
	case apiActionPanicOff:
		on = false
	default:
		writeJSON(w, http.StatusBadRequest, ActionResult{OK: false, Message: apiErrorPanicAction})
		return
	}
	res := s.Backend.SetPanic(s.operateContext(r), on) //nolint:contextcheck // see operateContext
	status := http.StatusOK
	if !res.OK {
		status = http.StatusConflict
	}
	writeJSON(w, status, res)
}

func (s *Server) handleOperations(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.Operations(r.Context()))
}

// readyReport builds the readiness report: it delegates to the configured
// Readiness probe when present, otherwise reports ready with the service count.
func (s *Server) readyReport(ctx context.Context) ReadyReport {
	if s.Readiness != nil {
		return s.Readiness.Report(ctx)
	}
	return ReadyReport{
		Ready: true, Status: apiStatusOK,
		Services: len(s.Backend.Services(ctx)),
	}
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	rep := s.readyReport(r.Context())
	status := http.StatusOK
	if !rep.Ready {
		status = http.StatusServiceUnavailable
	}
	if !r.URL.Query().Has(apiQueryVerbose) {
		w.Header().Set(headerContentType, contentTypeTextUTF8)
		w.WriteHeader(status)
		if rep.Ready {
			_, _ = io.WriteString(w, apiStatusOKLine)
		} else {
			_, _ = io.WriteString(w, rep.Status+"\n")
		}
		return
	}
	writeJSON(w, status, rep)
}

// handleLivez is the liveness probe: if the daemon's web server can answer, the
// process is alive, so it always returns 200. Plain requests get "ok"; `?verbose`
// returns JSON with uptime, the number of services and the runtime version. It is
// served without authentication (see withAuth) so probes need no credentials.
func (s *Server) handleLivez(w http.ResponseWriter, r *http.Request) {
	if !r.URL.Query().Has(apiQueryVerbose) {
		w.Header().Set(headerContentType, contentTypeTextUTF8)
		_, _ = io.WriteString(w, apiStatusOKLine)
		return
	}
	now := time.Now()
	uptime := now.Sub(s.started)
	writeJSON(w, http.StatusOK, map[string]any{
		apiJSONKeyStatus:        apiStatusOK,
		apiJSONKeyStartedAt:     s.started.Format(time.RFC3339),
		apiJSONKeyNow:           now.Format(time.RFC3339),
		apiJSONKeyUptime:        uptime.Round(time.Second).String(),
		apiJSONKeyUptimeSeconds: int64(uptime.Seconds()),
		apiJSONKeyServices:      len(s.Backend.Services(r.Context())),
		apiJSONKeyGo:            runtime.Version(),
	})
}

func (s *Server) handleServiceEvents(w http.ResponseWriter, r *http.Request) {
	events, ok := s.Backend.ServiceEvents(r.Context(), r.PathValue(apiParamName), eventLimit(r))
	if !ok {
		writeError(w, http.StatusNotFound, apiErrorUnknownService)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) handleApplicationEvents(w http.ResponseWriter, r *http.Request) {
	events, ok := s.Backend.ApplicationEvents(r.Context(), r.PathValue(apiParamName), eventLimit(r))
	if !ok {
		writeError(w, http.StatusNotFound, apiErrorUnknownApplication)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue(apiParamName)
	res, ok := s.Backend.Preflight(r.Context(), name)
	if !ok {
		writeError(w, http.StatusNotFound, apiErrorUnknownService)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue(apiParamName)
	action := r.PathValue(apiParamAction)
	switch {
	case operateActions[action]:
		opts := OperateOpts{NoCascade: queryBool(r, apiQueryNoCascade)}
		res := s.Backend.Operate(s.operateContext(r), name, action, opts) //nolint:contextcheck // see operateContext
		status := http.StatusOK
		if !res.OK {
			status = http.StatusConflict
		}
		writeJSON(w, status, res)
	case monitorActions[action]:
		err := s.Backend.SetMonitored(r.Context(), name, action == apiActionMonitor)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ActionResult{OK: true})
	default:
		writeError(w, http.StatusBadRequest, apiErrorUnknownActionPrefix+action)
	}
}

func (s *Server) handleWatchAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue(apiParamName)
	action := r.PathValue(apiParamAction)
	if watchOperateActions[action] {
		var res ActionResult
		switch action {
		case apiActionExpand:
			res = s.Backend.ExpandWatch(s.operateContext(r), name) //nolint:contextcheck // see operateContext
		case apiActionProbe:
			res = s.Backend.ProbeWatch(s.operateContext(r), name) //nolint:contextcheck // see operateContext
		default:
			res = s.Backend.ControlRAID(s.operateContext(r), name, action, r.Header.Get("X-Sermo-Confirm")) //nolint:contextcheck // see operateContext
		}
		status := http.StatusOK
		if !res.OK {
			status = http.StatusConflict
		}
		writeJSON(w, status, res)
		return
	}
	if !monitorActions[action] {
		writeError(w, http.StatusBadRequest, apiErrorUnknownActionPrefix+action)
		return
	}
	if err := s.Backend.SetWatchMonitored(r.Context(), name, action == apiActionMonitor); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ActionResult{OK: true})
}

func (s *Server) handleReload(w http.ResponseWriter, _ *http.Request) {
	if s.Reload == nil {
		writeError(w, http.StatusServiceUnavailable, apiErrorReloadUnavailable)
		return
	}
	if err := s.Reload(); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ActionResult{OK: true, Message: apiMessageReloadRequested})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	// Encode into a buffer before touching the ResponseWriter so an encoding
	// failure can still surface as a 500 instead of a truncated 200 body.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		http.Error(w, apiErrorEncodeResponse, http.StatusInternalServerError)
		return
	}
	w.Header().Set(headerContentType, contentTypeJSON)
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// writeError replies with an ActionResult failure — the uniform error body
// every JSON handler returns.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ActionResult{OK: false, Message: msg})
}
