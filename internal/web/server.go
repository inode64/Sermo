// Package web serves a small read-and-act dashboard for the daemon: it lists the
// monitored services with their status and lets an operator monitor/unmonitor and
// start/stop/restart/reload/resume/repair them. It is deliberately minimal and depends on the daemon
// only through the Backend interface, so it stays decoupled and testable.
//
// Access is optional HTTP Basic auth with admin (read+act) and guest (read-only)
// roles; state-changing POST requests also require an X-Sermo-Csrf header. When
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
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sermo/internal/httpx"
	"sermo/internal/logfile"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/rules"
)

//go:embed index.html
var assets embed.FS

const (
	headerCacheControl          = "Cache-Control"
	headerContentSecurityPolicy = "Content-Security-Policy"
	headerContentType           = httpx.HeaderContentType
	headerReferrerPolicy        = "Referrer-Policy"
	headerSermoConfirm          = HeaderConfirm
	headerSermoCSRF             = HeaderCSRF
	headerSermoGeneration       = HeaderGeneration
	headerSecFetchMode          = "Sec-Fetch-Mode"
	secFetchModeNavigate        = "navigate"
	contentTypeHTML             = "text/html"
	headerWWWAuthenticate       = "WWW-Authenticate"
	headerXContentTypeOptions   = "X-Content-Type-Options"
	headerXFrameOptions         = "X-Frame-Options"
	// authBasicRealmPrefix is the product name in WWW-Authenticate realms.
	// challenge() appends the short hostname when known: `Basic realm="Sermo algieba"`.
	authBasicRealmPrefix       = "Sermo"
	contentTypeHTMLUTF8        = "text/html; charset=utf-8"
	contentTypeJSON            = httpx.ContentTypeJSON
	contentTypeTextUTF8        = "text/plain; charset=utf-8"
	headerValueDeny            = "DENY"
	headerValueNoCache         = "no-cache"
	headerValueNoStore         = "no-store"
	headerValueNoReferrer      = "no-referrer"
	headerValueNoSniff         = "nosniff"
	cspNonceBytes              = 16
	cspFallbackNonceBase       = 36
	assetIndexHTML             = "index.html"
	templateNoncePlaceholder   = "{{CSP_NONCE}}"
	templateVersionPlaceholder = "{{VERSION}}"
)

const (
	cspSeparator                = "; "
	cspSourceSelf               = "'self'"
	cspSourceNone               = "'none'"
	cspSourceUnsafeInline       = "'unsafe-inline'"
	cspSourceData               = "data:"
	cspNonceSourceSuffix        = "'"
	cspDirectiveDefaultSrc      = "default-src " + cspSourceSelf
	cspDirectiveScriptSrcPrefix = "script-src " + cspSourceSelf + " 'nonce-"
	cspDirectiveStyleSrc        = "style-src " + cspSourceSelf + " " + cspSourceUnsafeInline
	cspDirectiveImgSrc          = "img-src " + cspSourceSelf + " " + cspSourceData
	cspDirectiveBaseURI         = "base-uri " + cspSourceNone
	cspDirectiveFormAction      = "form-action " + cspSourceSelf
	cspDirectiveFrameAncestors  = "frame-ancestors " + cspSourceNone
)

const (
	routePathRoot   = "/"
	routePathLivez  = "/livez"
	routePathReadyz = "/readyz"
	routePathLogin  = "/login"
	routePathAPI    = APIPathRoot
	apiPathPrefix   = routePathAPI + "/"
)

// API path segment names used by routing and access-log classification.
const (
	apiSegmentRoot             = "api"
	apiSegmentActivity         = "activity"
	apiSegmentBlockers         = "blockers"
	apiSegmentDashboard        = "dashboard"
	apiSegmentDaemon           = "daemon"
	apiSegmentEvents           = "events"
	apiSegmentHost             = "host"
	apiSegmentLibraries        = "libraries"
	apiSegmentLocks            = "locks"
	apiSegmentMetrics          = "metrics"
	apiSegmentMonitoring       = "monitoring"
	apiSegmentMounts           = "mounts"
	apiSegmentNotifiers        = "notifiers"
	apiSegmentPanic            = "panic"
	apiSegmentPreflight        = "preflight"
	apiSegmentReload           = "reload"
	apiSegmentRuntime          = "runtime"
	apiSegmentServices         = "services"
	apiSegmentSessions         = "sessions"
	apiSegmentTerminalSessions = "terminal-sessions"
	apiSegmentSLA              = "sla"
	apiSegmentState            = "state"
	apiSegmentStream           = "stream"
	apiSegmentWatches          = "watches"
	apiSegmentWhoami           = "whoami"
)

// HTTP action names accepted by the dashboard API.
const (
	apiActionReload    = string(rules.ActionReload)
	apiActionResume    = string(rules.ActionResume)
	apiActionMonitor   = "monitor"
	apiActionUnmonitor = "unmonitor"
	// apiActionReap is deliberately not a service operation: it does not change
	// unit state, so it gets its own branch rather than joining the engine path.
	apiActionReap             = process.SectionReap
	apiActionExpand           = "expand"
	apiActionProbe            = "probe"
	apiActionPause            = "pause"
	apiActionReplicationStart = "replication-start"
	apiSegmentButton          = "button"
	apiParamButton            = "button"
	apiActionPanicOn          = "on"
	apiActionPanicOff         = "off"
	apiActionRelease          = "release"
	apiActionClear            = "clear"
	apiActionCompact          = "compact"
	apiActionAlert            = string(rules.ActionAlert)
	apiActionTest             = "test"
	apiActionClose            = "close"
	apiActionCloseEmpty       = "close-empty"

	queryBoolOne  = "1"
	queryBoolTrue = "true"
	queryBoolYes  = "yes"
	queryBoolOn   = "on"
)

const (
	apiErrorCheckQueryRequired       = "check query parameter is required"
	apiErrorMetricQueryRequired      = "metric query parameter is required"
	apiErrorEncodeResponse           = "failed to encode response"
	apiErrorBackendUnavailable       = "web backend unavailable"
	apiErrorGenerationInvalid        = "invalid X-Sermo-Generation header"
	apiErrorGenerationMissing        = "X-Sermo-Generation header is required"
	apiErrorGenerationStale          = "configuration changed; refresh and try again"
	apiErrorPanicAction              = "panic action must be on or off"
	apiErrorReloadUnavailable        = "reload is not available for this daemon"
	apiErrorUnknownActionPrefix      = "unknown action "
	apiErrorUnknownApplication       = "unknown application"
	apiErrorUnknownMountActionPrefix = "unknown mount action "
	apiErrorUnknownService           = "unknown service"
	apiErrorUnknownServiceOrCheck    = "unknown service or check"
	apiErrorUnknownAvailWatch        = "unknown watch or watch without availability"
	apiErrorUnknownWatchMetric       = "unknown watch or metric it does not publish"
	apiErrorUnknownCheckBand         = "unknown target or state band it does not declare"
	apiMessageReloadRequested        = "reload requested"
)

// Managed login1 close is an optional safety mode on the ordinary session route.
const apiQueryManagedByLogind = "managed_by_logind"

// API route variables and query parameter names.
const (
	apiParamAction      = "action"
	apiParamName        = "name"
	apiParamPID         = "pid"
	apiParamService     = "service"
	apiQueryBefore      = APIQueryBefore
	apiQueryBeforeID    = "before_id"
	apiQueryCheck       = "check"
	apiQueryForce       = "force"
	apiQueryKind        = "kind"
	apiQueryKill        = "kill"
	apiQueryLazy        = "lazy"
	apiQueryLimit       = APIQueryLimit
	apiQueryMetric      = "metric"
	apiQueryNoCascade   = "no_cascade"
	apiQueryOnlyErrors  = "only_errors"
	apiQuerySince       = "since"
	apiQueryStatus      = "status"
	apiQueryStartTicks  = "start_ticks"
	apiQueryTerminal    = "terminal"
	apiQueryUser        = "user"
	apiQueryMultiplexer = "multiplexer"
	apiQuerySession     = "session"
	apiQueryIdentity    = "identity"
	apiQueryVerbose     = "verbose"
	apiQueryWatch       = "watch"
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
	apiPathApplications = APIPathApplications
	apiPathDashboard    = apiPathPrefix + apiSegmentDashboard
	apiPathDaemon       = apiPathPrefix + apiSegmentDaemon
	apiPathEvents       = APIPathEvents
	apiPathHost         = apiPathPrefix + apiSegmentHost
	apiPathLibraries    = apiPathPrefix + apiSegmentLibraries
	apiPathLocks        = apiPathPrefix + apiSegmentLocks
	apiPathMonitoring   = apiPathPrefix + apiSegmentMonitoring
	apiPathMounts       = apiPathPrefix + apiSegmentMounts
	apiPathNotifiers    = apiPathPrefix + apiSegmentNotifiers
	apiPathPanic        = apiPathPrefix + apiSegmentPanic
	apiPathReload       = apiPathPrefix + apiSegmentReload
	apiPathServices     = APIPathServices
	apiPathSessions     = apiPathPrefix + apiSegmentSessions
	apiPathState        = apiPathPrefix + apiSegmentState
	apiPathStream       = apiPathPrefix + apiSegmentStream
	apiPathWatches      = APIPathWatches
	apiPathWhoami       = apiPathPrefix + apiSegmentWhoami
)

const (
	routeIndex                        = routeMethodGet + routePathRoot
	routeLivez                        = routeMethodGet + routePathLivez
	routeReadyz                       = routeMethodGet + routePathReadyz
	routeAPIWhoami                    = routeMethodGet + apiPathWhoami
	routeAPIServices                  = routeMethodGet + apiPathServices
	routeAPISessions                  = routeMethodGet + apiPathSessions
	routeAPIWatches                   = routeMethodGet + apiPathWatches
	routeAPIWatchAction               = routeMethodPost + apiPathWatches + "/" + routeVarName + "/" + routeVarAction
	routeAPIWatchSeries               = routeMethodGet + apiPathWatches + "/" + routeVarName + "/" + apiSegmentSLA
	routeAPIWatchMetrics              = routeMethodGet + apiPathWatches + "/" + routeVarName + "/" + apiSegmentMetrics
	routeAPINotifiers                 = routeMethodGet + apiPathNotifiers
	routeAPINotifierTest              = routeMethodPost + apiPathNotifiers + "/" + routeVarName + "/" + apiActionTest
	routeAPIApplications              = routeMethodGet + apiPathApplications
	routeAPILibraries                 = routeMethodGet + apiPathLibraries
	routeAPIDashboard                 = routeMethodGet + apiPathDashboard
	routeAPIMounts                    = routeMethodGet + apiPathMounts
	routeAPIMountAction               = routeMethodPost + apiPathMounts + "/" + routeVarName + "/" + routeVarAction
	routeAPIMountBlockers             = routeMethodGet + apiPathMounts + "/" + routeVarName + "/" + apiSegmentBlockers
	routeAPIDaemon                    = routeMethodGet + apiPathDaemon
	routeAPIDaemonMetrics             = routeMethodGet + apiPathDaemon + "/" + apiSegmentMetrics
	routeAPIHost                      = routeMethodGet + apiPathHost
	routeAPILocks                     = routeMethodGet + apiPathLocks
	routeAPILockRelease               = routeMethodPost + apiPathLocks + "/" + routeVarService + "/" + apiActionRelease
	routeAPIActivity                  = routeMethodGet + apiPathActivity
	routeAPIMonitoring                = routeMethodGet + apiPathMonitoring
	routeAPIDetail                    = routeMethodGet + apiPathServices + "/" + routeVarName
	routeAPISeries                    = routeMethodGet + apiPathServices + "/" + routeVarName + "/" + apiSegmentSLA
	routeAPIMetrics                   = routeMethodGet + apiPathServices + "/" + routeVarName + "/" + apiSegmentMetrics
	routeAPIServiceButton             = routeMethodPost + apiPathServices + "/" + routeVarName + "/" + apiSegmentButton + "/{" + apiParamButton + "}"
	routeAPIServiceRuntime            = routeMethodGet + apiPathServices + "/" + routeVarName + "/" + apiSegmentRuntime
	routeAPIServiceEvents             = routeMethodGet + apiPathServices + "/" + routeVarName + "/" + apiSegmentEvents
	routeAPIAppEvents                 = routeMethodGet + apiPathApplications + "/" + routeVarName + "/" + apiSegmentEvents
	routeAPIEvents                    = routeMethodGet + apiPathEvents
	routeAPIStream                    = routeMethodGet + apiPathStream
	routeAPIEventsClear               = routeMethodPost + APIPathEventsClear
	routeAPIStateCompact              = routeMethodPost + apiPathState + "/" + apiActionCompact
	routeAPIPanic                     = routeMethodPost + apiPathPanic + "/" + routeVarAction
	routeAPIPreflight                 = routeMethodPost + apiPathServices + "/" + routeVarName + "/" + apiSegmentPreflight
	routeAPISessionClose              = routeMethodPost + apiPathServices + "/" + routeVarName + "/" + apiSegmentSessions + "/{" + apiParamPID + "}/" + apiActionClose
	routeAPITerminalSessionClose      = routeMethodPost + apiPathServices + "/" + routeVarName + "/" + apiSegmentTerminalSessions + "/{" + apiQueryCheck + "}/" + apiActionClose
	routeAPIEmptyTerminalSessionClose = routeMethodPost + apiPathServices + "/" + routeVarName + "/" + apiSegmentTerminalSessions + "/{" + apiQueryCheck + "}/" + apiActionCloseEmpty
	routeAPIAction                    = routeMethodPost + apiPathServices + "/" + routeVarName + "/" + routeVarAction
	routeAPIReload                    = routeMethodPost + apiPathReload
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
// may run and sizes the initial HTTP write deadline; it should be the maximum
// per-service and watch-probe deadline (app.MaxOperationTimeout).
type Server struct {
	Addr    string
	Backend BackendSource
	Auth    Auth
	Logger  *slog.Logger

	// Hostname is the short host identity used in the Basic auth realm
	// (`Basic realm="Sermo <Hostname>"`) so multi-host operators can tell
	// password prompts apart. Empty falls back to realm "Sermo". The daemon
	// sets it from config.ShortHostname() (same source as ${hostname}).
	Hostname string

	// AllowedHosts lists extra hostnames accepted in the Host header when auth
	// is disabled (open mode), e.g. the public name of a fronting proxy.
	// localhost, IP-literal Hosts and the bind host are always accepted; other
	// names are refused (DNS-rebinding protection). The check needs Addr set
	// (Run always has it) and does not apply with auth enabled: a DNS-rebound
	// origin cannot attach Basic credentials, and proxies keep their Host.
	AllowedHosts []string

	// MaxSeriesWindow caps the history one series request may ask for. Zero uses
	// defaultMaxSeriesWindow; the daemon sets it from engine.retention_1d.
	MaxSeriesWindow time.Duration

	OperationTimeout time.Duration
	// OperationTimeoutSource supplies the active maximum timeout after a daemon
	// reload, including watch probe budgets. It is optional; OperationTimeout
	// remains the fallback.
	OperationTimeoutSource func() time.Duration
	// Readiness is optional; nil makes /readyz report ready (tests).
	Readiness ReadinessChecker

	// Reload, if set, is called for admin POST /api/reload requests. It should
	// trigger a configuration reload (equivalent to SIGHUP on the daemon).
	Reload func() error

	// AccessLog appends operator POST audit records when engine.access is set.
	AccessLog *logfile.Writer

	// Changes, if set, enables GET /api/stream: a Server-Sent Events channel
	// that pushes a payload-free "change" signal whenever the daemon emits an
	// event, so connected dashboards refetch immediately instead of waiting
	// for their next poll. Nil disables the endpoint (404).
	Changes *Broadcaster

	started  time.Time       // when the server began serving; for /livez uptime
	shutdown context.Context //nolint:containedctx // daemon lifetime; set in Run. Not a per-request context.
}

// dashboardSnapshotSource is an optional atomic aggregate source. The normal
// Backend interface stays granular for simple integrations, while sermod's
// reloadable holder implements this to keep one response on one generation.
type dashboardSnapshotSource interface {
	DashboardSnapshot(ctx context.Context, since time.Duration) DashboardSnapshot
}

type sessionInventorySource interface {
	Sessions(ctx context.Context) SessionInventory
}

// BackendSource hands the server the Backend a request runs against and the
// configuration generation it belongs to; 0 means generations are not tracked.
// The daemon's reloadable holder swaps generations atomically; a fixed backend
// wraps itself in StaticBackend.
type BackendSource interface {
	BeginBackendRead() (Backend, uint64)
}

// StaticBackend serves one fixed Backend without generation tracking.
type StaticBackend struct {
	Backend Backend
}

// BeginBackendRead implements BackendSource.
//
//nolint:ireturn // StaticBackend exists to hand out the Backend it was given.
func (b StaticBackend) BeginBackendRead() (Backend, uint64) { return b.Backend, 0 }

// Handler returns the router behind the auth middleware: the dashboard at /, the
// service list at /api/services, and POST /api/services/{name}/{action} for
// actions.
func (s *Server) Handler() http.Handler {
	if s.started.IsZero() {
		s.started = time.Now()
	}
	if s.shutdown == nil {
		// Run replaces this with the daemon lifetime; a handler built without
		// Run (tests, embedding) still detaches mutations from the request.
		s.shutdown = context.Background()
	}
	mux := http.NewServeMux()
	mux.HandleFunc(routeIndex, s.handleIndex)
	mux.HandleFunc(routeLivez, s.handleLivez)
	mux.HandleFunc(routeReadyz, s.handleReadyz)
	mux.HandleFunc(routeAPIWhoami, s.handleWhoami)
	mux.HandleFunc(routeAPIDashboard, s.handleDashboard)
	mux.HandleFunc(routeAPISessions, s.handleSessions)
	mux.HandleFunc(routeAPIServices, s.handleServices)
	mux.HandleFunc(routeAPIWatches, s.handleWatches)
	mux.HandleFunc(routeAPIWatchAction, s.handleWatchAction)
	mux.HandleFunc(routeAPIWatchSeries, s.handleWatchSeries)
	mux.HandleFunc(routeAPIWatchMetrics, s.handleWatchMetrics)
	mux.HandleFunc(routeAPINotifiers, s.handleNotifiers)
	mux.HandleFunc(routeAPINotifierTest, s.handleNotifierTest)
	mux.HandleFunc(routeAPIApplications, s.handleApplications)
	mux.HandleFunc(routeAPILibraries, s.handleLibraries)
	mux.HandleFunc(routeAPIMounts, s.handleMounts)
	mux.HandleFunc(routeAPIMountAction, s.handleMountAction)
	mux.HandleFunc(routeAPIMountBlockers, s.handleMountBlockers)
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
	mux.HandleFunc(routeAPIServiceButton, s.handleServiceButton)
	mux.HandleFunc(routeAPIServiceRuntime, s.handleServiceRuntime)
	mux.HandleFunc(routeAPIServiceEvents, s.handleServiceEvents)
	mux.HandleFunc(routeAPIAppEvents, s.handleApplicationEvents)
	mux.HandleFunc(routeAPIEvents, s.handleEvents)
	mux.HandleFunc(routeAPIStream, s.handleStream)
	mux.HandleFunc(routeAPIEventsClear, s.handleEventsClear)
	mux.HandleFunc(routeAPIStateCompact, s.handleStateCompact)
	mux.HandleFunc(routeAPIPanic, s.handlePanic)
	mux.HandleFunc(routeAPIPreflight, s.handlePreflight)
	mux.HandleFunc(routeAPISessionClose, s.handleSSHSessionClose)
	mux.HandleFunc(routeAPITerminalSessionClose, s.handleTerminalSessionClose)
	mux.HandleFunc(routeAPIEmptyTerminalSessionClose, s.handleEmptyTerminalSessionClose)
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

func (s *Server) actionWriteTimeout() time.Duration {
	timeout := s.OperationTimeout
	if s.OperationTimeoutSource != nil {
		if current := s.OperationTimeoutSource(); current > 0 {
			timeout = current
		}
	}
	return serverWriteTimeout(timeout)
}

// extendActionWriteDeadline lets a reloaded operation timeout exceed the
// server's startup write deadline without aborting the browser response after
// the safe operation has completed.
func (s *Server) extendActionWriteDeadline(w http.ResponseWriter) {
	deadline := time.Now().Add(s.actionWriteTimeout())
	if err := http.NewResponseController(w).SetWriteDeadline(deadline); err != nil && s.Logger != nil {
		s.Logger.Debug("set action response write deadline", "error", err)
	}
}

// operate runs one backend mutation and writes its outcome. do receives
// s.shutdown, the daemon lifetime, instead of the request's context on
// purpose: a start/stop/restart/reload/resume must not be aborted by a client
// disconnect or the generic write deadline, and the operation engine applies
// its own timeout. This is the single place that detaches an action from its
// request.
func (s *Server) operate(w http.ResponseWriter, _ *http.Request, backend Backend, do func(context.Context, Backend) (bool, any)) {
	ok, res := do(s.shutdown, backend)
	writeActionResult(w, ok, res)
}

// Run serves until ctx is cancelled, then shuts down gracefully. Timeouts bound
// slow clients (the server runs as root, so it is hardened by default).
func (s *Server) Run(ctx context.Context) error {
	s.shutdown = ctx //nolint:fatcontext // stores the daemon Run cancel for /readyz and in-flight mutations; not nested per request.
	srv := &http.Server{
		Addr:              s.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      s.actionWriteTimeout(),
		IdleTimeout:       serverIdleTimeout,
	}
	serveDone := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() { //nolint:gosec // G118: the shutdown deadline must NOT derive from ctx — it is already cancelled here
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
			defer cancel()
			_ = srv.Shutdown(shutCtx) //nolint:contextcheck // detached shutdown deadline; ctx is already cancelled
		case <-serveDone:
		}
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		close(serveDone)
		<-shutdownDone
		return fmt.Errorf("web server listen: %w", err)
	}
	close(serveDone)
	<-shutdownDone
	return nil
}

// writeActionResult writes an action outcome as JSON: 200 when the backend
// accepted it, 409 Conflict when it was rejected.
func writeActionResult(w http.ResponseWriter, ok bool, res any) {
	status := http.StatusOK
	if !ok {
		status = http.StatusConflict
	}
	writeJSON(w, status, res)
}

// backendRead pins one backend generation for the duration of a request. When
// the daemon has no backend yet it answers 503 and reports false, so a handler
// never touches a nil Backend.
//
//nolint:ireturn // The server must return the Backend implementation held by its reloadable seam.
func (s *Server) backendRead(w http.ResponseWriter) (Backend, uint64, bool) {
	if s.Backend != nil {
		if backend, generation := s.Backend.BeginBackendRead(); backend != nil {
			return backend, generation, true
		}
	}
	writeError(w, http.StatusServiceUnavailable, apiErrorBackendUnavailable)
	return nil, 0, false
}

// mutationBackend pins the active backend for one target-scoped mutation and
// rejects a request whose dashboard generation is missing or stale. The returned
// instance is the pin: the action keeps running against the generation whose
// precondition it passed, so a concurrent reload can neither swap service/watch
// identity underneath it nor wait on it.
//
//nolint:ireturn // The mutation must use the selected Backend implementation for its whole lifetime.
func (s *Server) mutationBackend(w http.ResponseWriter, r *http.Request) (Backend, bool) {
	backend, generation, ok := s.backendRead(w)
	if !ok {
		return nil, false
	}
	if generation == 0 {
		return backend, true
	}
	w.Header().Set(headerSermoGeneration, strconv.FormatUint(generation, 10))
	raw := strings.TrimSpace(r.Header.Get(headerSermoGeneration))
	if raw == "" {
		writeError(w, http.StatusPreconditionRequired, apiErrorGenerationMissing)
		return nil, false
	}
	expected, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || expected == 0 {
		writeError(w, http.StatusBadRequest, apiErrorGenerationInvalid)
		return nil, false
	}
	if expected != generation {
		writeError(w, http.StatusPreconditionFailed, apiErrorGenerationStale)
		return nil, false
	}
	return backend, true
}

// readJSON collects a read response from one backend generation and labels the
// encoded result with that same generation. Reads always answer 200: a backend
// read cannot fail, only observe the current snapshot.
func (s *Server) readJSON(w http.ResponseWriter, r *http.Request, read func(context.Context, Backend) any) {
	backend, generation, ok := s.backendRead(w)
	if !ok {
		return
	}
	s.writeBackendJSON(w, http.StatusOK, read(r.Context(), backend), generation)
}

// writeBackendJSON marks a read response with the generation that produced its
// body, so the browser can reject a response from another daemon reload.
func (*Server) writeBackendJSON(w http.ResponseWriter, status int, v any, generation uint64) {
	if generation > 0 {
		w.Header().Set(headerSermoGeneration, strconv.FormatUint(generation, 10))
	}
	writeJSON(w, status, v)
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
	// API payloads can carry process cmdlines and configuration; keep them out
	// of browser and proxy caches.
	w.Header().Set(headerCacheControl, headerValueNoStore)
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// writeError replies with an ActionResult failure — the uniform error body
// every JSON handler returns.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ActionResult{OK: false, Message: msg})
}
