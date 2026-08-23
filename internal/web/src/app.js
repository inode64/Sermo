import { html as tpl, render as litRender, nothing } from "./vendor/lit-html.js";
import watchPanelDescriptors from "./watch-panels.json";
import {
  apiActionSuffix, apiActivityPath, apiApplicationsPath, apiDaemonPath, apiHeaderGeneration,
  apiEventsRecentPath, apiHostPath, apiLibrariesPath, apiLocksPath,
  apiHeaderConfirm, apiMonitoringPath, apiMountsPath, apiNotifiersPath,
  apiQueryBefore, apiQueryBeforeID,
  apiQueryForce, apiQueryKill, apiQueryKind, apiQueryLazy, apiQueryLimit, apiQueryName, apiQueryNoCascade,
  apiQueryOnlyErrors, apiQueryService, apiQuerySince, apiQueryStatus,
  apiQueryWatch, apiReloadPath, notifierTestAPI,
  apiServicesPath, apiSessionsPath, apiStreamPath, apiWatchesPath, apiWhoamiPath, applicationEventsAPI,
  csrfPostOptions, dashboardAPI, daemonMetricsAPI, eventsAPI, eventsClearAPI,
  emptyTerminalSessionCloseAPI,
  liveVerbosePath, lockReleaseAPI, mountAPI, mountBlockersAPI, panicAPI,
  readyVerbosePath, serviceAPI, serviceButtonAPI, serviceEventsAPI, serviceMetricsAPI,
  servicePreflightAPI, serviceRuntimeAPI, serviceSLAAPI, sshSessionCloseAPI, terminalSessionCloseAPI, stateCompactAPI, watchAPI,
  watchMetricsAPI, watchSLAAPI,
} from "./api.js";
import {
  fmtAge, fmtBytes, fmtBytesPerSecond, fmtDuration, fmtMetricValue, fmtNum, fmtPct, fmtRemain,
  fmtSince, fmtTime, fmtUntilShort, millisecondsPerDay,
  millisecondsPerHour, millisecondsPerMinute, millisecondsPerSecond,
  metricUnitBytes, metricUnitBytesPerSecond, metricUnitMilliseconds,
  metricUnitPercent, pctClamp, percentMax,
  percentScale, rollingMonthDays, rollingWeekDays, rollingYearDays,
  secondsPerHour, secondsPerMinute,
} from "./format.js";

const $ = (s) => document.querySelector(s);

const metricNameCPU = "cpu";
const metricNameMemory = "memory";
const metricNameIO = "io";
const hostMetricTotalCPU = "total_cpu";
const hostMetricTotalMemory = "total_memory";
const hostMetricTotalSwap = "total_swap";
const hostMetricLoad1 = "load1";
const eventLogLimit = "500";
const httpStatusServiceUnavailable = 503;
const httpStatusUnauthorized = 401;
const loginPath = "/login";
const expansionPrefixApp = "app:";
const expansionPrefixLibrary = "lib:";
const expansionPrefixService = "svc:";
const expansionPrefixWatch = "wat:";
const globalTargetService = "service";
const globalTargetWatch = "watch";
const globalTargetApplication = "application";
const globalTargetLibrary = "library";
const globalTargetMount = "mount";
// Fallback category for a target whose configuration sets none. It becomes the
// group heading, so each panel names its own — these are group labels, not the
// globalTarget* search kinds above (storage mounts group under "storage").
const defaultCategoryService = "service";
const defaultCategoryWatch = "watch";
const defaultCategoryStorage = "storage";
const defaultCategoryApp = "app";
const defaultCategoryLibrary = "library";
// Classes for a General data field in a service/container/VM expansion whose
// reading is also a .services-table column: the field is hidden while that column
// is on screen and revealed at the breakpoint that retires it, so the reading is
// shown exactly once and a narrow viewport loses nothing. The suffix names the
// breakpoint, matching the .col-dup rules in styles.css — colDupWide for the
// columns the 1420px block drops (Category, Uptime, CPU total, Memory, FDs, IO R/W)
// and colDupPhone for Last activity, which survives down to 640px.
//
// A field the column does not restate *exactly* carries neither class:
// "FDs / Threads" also reports the thread count, which has no column of its own.
// TestIndexDetailDedupesVisibleColumns pins these against both the CSS and the
// fields that use them.
const colDupWide = "col-dup col-dup-1420";
const colDupPhone = "col-dup col-dup-640";
// Key of the host watch panel in watch-panels.json. Distinct from
// watchScopeHost, which is the daemon's `scope` value for a host-level watch:
// same string, different vocabulary.
const watchPanelKeyHost = "host";
const eventDetailLimit = "50";
// eventRangeAll is the events range selector's explicit "any time" value.
const eventRangeAll = "all";
const eventContextLimit = "1";
const queryBoolOne = "1";
const storageBoolTrue = "1";
const storageBoolFalse = "0";
const domBoolTrue = "true";
const domBoolFalse = "false";
const domEventChange = "change";
const domEventClick = "click";
const domEventClose = "close";
const eventSourceAPIName = "EventSource";
const domEventHashChange = "hashchange";
const domEventInput = "input";
const domEventKeydown = "keydown";
const domEventResize = "resize";
const domEventToggle = "toggle";
const domEventVisibilityChange = "visibilitychange";
const keyEnter = "Enter";
const keyEscape = "Escape";
const keySpace = " ";
const scrollBlockStart = "start";
const scrollBehaviorSmooth = "smooth";
const filterAll = "all";
const feedbackStatusOK = "ok";
const feedbackStatusWarn = "warn";
const feedbackStatusErr = "err";
const targetStateDisabled = "disabled";
const targetStateRunning = "running";
const targetStateStarted = "started";
const targetStateActive = "active";
const targetStatePaused = "paused";
const targetStateStopped = "stopped";
const targetStateWarning = "warning";
const targetStateStale = "stale";
// Mirrors warningReason* in internal/app/metric_constants.go.
const warningReasonStaleBinary = "stale_binary";
const warningReasonFailedUnitLiveProcess = "failed_unit_live_process";
const watchSampleStateFresh = "fresh";
const targetStateOK = "ok";
const targetStateMonitored = "monitored";
const targetStateCollecting = "collecting";
const targetStateTesting = "testing";
const targetStateRecovering = "recovering";
const targetStateRebuilding = "rebuilding";
const targetStateRepairing = "repairing";
const targetStateMoving = "moving";
const targetStateMerging = "merging";
// The one device state that is a fault rather than an operation in progress: the
// check's device stopped answering, so its live readings are gone, not pending.
// The row still carries what the drive is (model, serial, firmware) and what it
// last reported, marked "(last)" so neither reads as a current sample.
const targetStateMissing = "missing";
const targetStateFailed = "failed";
const targetStateStarting = "starting";
const targetStateStopping = "stopping";
const targetStateRestarting = "restarting";
const targetStateResuming = "resuming";
const targetStateReloading = "reloading";
const targetStateWorking = "working";
const operationStateRunning = "running";
const operationStateCompleted = "completed";
const healthStatusCritical = "critical";
const healthStatusCriticalShort = "crit";
const healthStatusWarning = targetStateWarning;
const healthStatusWarningShort = feedbackStatusWarn;
const healthStatusOK = targetStateOK;
const healthStatusInfo = "info";
const healthStatusMuted = "muted";
const policyStateEligible = "eligible";
const policyStateCooldown = "cooldown";
const policyStateRateLimit = "rate limit";
const policyStateBlocked = "blocked";
const policyStatePending = "pending";
const backendStatusActive = "active";
const backendStatusInactive = "inactive";
const backendStatusUnknown = "unknown";
const mountStateActive = "active";
const mountStateInactive = "inactive";
const mountStateError = "error";
const mountStateMounting = "mounting";
const mountStateUnmounting = "unmounting";
const lockStateActive = "active";
const lockStateStale = "stale";
const lockOwnerStatusLive = "live";
const lockOwnerStatusStale = "stale";
const daemonStatusStarting = targetStateStarting;
const daemonStatusShuttingDown = "shutting_down";
const monitorModeEnabled = "enabled";
const actionAlert = "alert";
const actionExpand = "expand";
const actionProbe = "probe";
const actionPause = "pause";
const actionReplicationStart = "replication-start";
const actionMonitor = "monitor";
const actionReload = "reload";
const actionReap = "reap";
const actionRepair = "repair";
const actionRestart = "restart";
const actionResume = "resume";
const actionStart = "start";
const actionStop = "stop";
const actionUnmonitor = "unmonitor";
const actionMount = "mount";
const actionUmount = "umount";
const eventKindAction = "action";
const eventKindCascade = "cascade";
const eventKindDryRun = "dry-run";
const eventKindExpandFailed = "expand-failed";
const eventKindExpandSkipped = "expand-skipped";
const eventKindFiring = "firing";
const eventKindHook = "hook";
const eventKindHookFailed = "hook-failed";
const eventKindKill = "kill";
const eventKindKillFailed = "kill-failed";
const eventKindMakeStep = "makestep";
const eventKindMakeStepFailed = "makestep-failed";
const eventKindMakeStepSkipped = "makestep-skipped";
const eventKindNotify = "notify";
const eventKindNotifyFailed = "notify-failed";
const eventKindNotifySuppressed = "notify-suppressed";
const eventKindPanicSuppressed = "panic-suppressed";
const eventKindRecovered = "recovered";
const eventKindReload = "reload";
const eventKindSuppressed = "suppressed";
const eventKindWarning = "warning";
const eventStatusPreflightFailed = "preflight_failed";
const eventStatusPostflightFailed = "postflight_failed";
const eventStatusOrphanProcesses = "orphan_processes";
const servicePreflightActions = [actionStart, actionStop, actionRestart, actionRepair];
// Only lifecycle actions that change the unit's running state propagate to
// also_apply targets. Repair remains a deliberately one-service recovery.
const serviceCascadeActions = [actionStart, actionStop, actionRestart];
// The engine operations (mirrors operation.IsServiceAction in the API): the
// actions that run through the operation pipeline, so they are the ones this
// browser tracks in liveOps and the ones a named lock blocks.
const serviceTrackedActions = [actionStart, actionStop, actionRestart, actionReload, actionResume, actionRepair];
const activityCriticalStatuses = [targetStateFailed, mountStateError, eventStatusPreflightFailed, eventStatusPostflightFailed, eventStatusOrphanProcesses];
const activityCriticalKinds = [mountStateError, eventKindHookFailed, eventKindNotifyFailed, eventKindExpandFailed, eventKindKillFailed, eventKindMakeStepFailed];
const activityWarningKinds = [actionAlert, eventKindFiring, eventKindWarning, eventKindSuppressed, eventKindPanicSuppressed, eventKindNotifySuppressed, eventKindExpandSkipped, eventKindMakeStepSkipped];
const activityOKKinds = [eventKindAction, eventKindCascade, eventKindHook, eventKindNotify, eventKindRecovered, actionExpand, eventKindKill, eventKindMakeStep];
const activityInfoKinds = [eventKindDryRun, eventKindReload];
const serviceStatusFilterStates = [
  targetStateDisabled,
  targetStateStopped,
  targetStateStarted,
  targetStateActive,
  targetStateStarting,
  targetStateCollecting,
  targetStateMonitored,
  targetStateWarning,
  targetStateFailed,
];
const watchStatusFilterStates = [targetStateDisabled, targetStateOK, targetStateStarting, targetStateWarning, targetStateStale, targetStateTesting, targetStateRecovering, targetStateRebuilding, targetStateRepairing, targetStateMoving, targetStateMerging, targetStateMissing, targetStateFailed];
const appStatusFilterStates = [targetStateOK, targetStateStarting, targetStateWarning, targetStateFailed];
const mountStatusFilterStates = [mountStateActive, mountStateInactive];
const sessionKindSSH = "ssh";
const sessionKindTmux = "tmux";
const sessionKindScreen = "screen";
const terminalSessionKinds = [sessionKindTmux, sessionKindScreen];
const sessionTypeFilterStates = [sessionKindSSH, ...terminalSessionKinds];
const sessionSourceAvailable = "available";
const sessionSourceUnavailable = "unavailable";
const sessionSourcePartial = "partial";
const sessionStateActive = "active";
const sessionStateAttached = "attached";
const sessionStateDetached = "detached";
const sessionStateEmpty = "empty";
const sessionStateUnknown = "unknown";
const sessionStateBadges = {
  [targetStateCollecting]: [targetStateCollecting, targetStateCollecting],
  [sessionSourceUnavailable]: [targetStateFailed, sessionSourceUnavailable],
  [sessionSourcePartial]: [targetStateWarning, sessionSourcePartial],
  [sessionStateActive]: [targetStateActive, sessionStateActive],
  [sessionStateAttached]: [targetStateRunning, sessionStateAttached],
  [sessionStateDetached]: [targetStateStopped, sessionStateDetached],
  [sessionStateEmpty]: [sessionStateEmpty, sessionStateEmpty],
  [sessionStateUnknown]: [targetStateWarning, sessionStateUnknown],
};
// SLA strips colour by how much of a bucket was down, not by availability: a
// 40-second outage inside a day-long bucket is 99.93% available, which any
// availability threshold reads as healthy. These are the upper edges of the four
// non-zero severity bands, in percent of failed cycles; zero down is the only
// green.
const slaNoData = "no data";
// Shown in place of the band when the selected window holds no sample at all,
// by both the service SLA panel and a check's SLA cell.
const slaNoWindowData = '<span class="muted">No SLA data yet for this window.</span>';
const slaDownBandLow = 25;
const slaDownBandMid = 50;
const slaDownBandHigh = 75;
const usageCriticalPct = 95;
const usageHighPct = 90;
const usageWarnPct = 75;
const loadWarnPct = 80;
const overviewSwapCriticalPct = 90;
const overviewSwapWarnPct = 70;
const overviewDaemonPanel = "daemon-section";
const overviewLoadingValue = "…";
const overviewLoadingText = "loading…";
const eventMessagePreviewChars = 160;
const liveOpsTickMs = millisecondsPerSecond;
const refreshAgeTickMs = millisecondsPerSecond;
const chartViewWidth = 640;
const chartViewHeight = 160;
const chartColumnCount = 120;
const metricChartPad = 34;
const slaBarCount = 90;
// A warning service is running without a hard health failure but needs operator
// attention (a process-tree observability gap or a replaced binary), so it
// counts as active like a collecting one.
const overviewActiveServiceStates = [targetStateStarted, targetStateActive, targetStateCollecting, targetStateWarning, targetStateMonitored];
const mountStateClasses = {
  [mountStateActive]: "state-running",
  [mountStateInactive]: "state-stopped",
  [mountStateError]: "state-failed",
  [mountStateMounting]: "state-starting",
  [mountStateUnmounting]: "state-starting",
};
const mountStateRanks = {
  [mountStateActive]: 0,
  [mountStateInactive]: 1,
  [mountStateMounting]: 2,
  [mountStateUnmounting]: 2,
  [mountStateError]: 3,
};
const targetStateClasses = {
  [targetStateDisabled]: "state-disabled",
  [targetStateRunning]: "state-running",
  [targetStateStarted]: "state-started",
  [targetStateActive]: "state-running",
  [targetStatePaused]: "state-paused",
  [targetStateStopped]: "state-stopped",
  [targetStateWarning]: "state-warning",
  [targetStateStale]: "state-stale",
  [targetStateOK]: "state-ok",
  [targetStateMonitored]: "state-monitored",
  [targetStateCollecting]: "state-collecting",
  [targetStateTesting]: "state-testing",
  [targetStateRecovering]: "state-recovering",
  [targetStateRebuilding]: "state-rebuilding",
  [targetStateRepairing]: "state-repairing",
  [targetStateMoving]: "state-moving",
  [targetStateMerging]: "state-merging",
  [targetStateMissing]: "state-missing",
  [targetStateFailed]: "state-failed",
  [sessionStateEmpty]: "state-empty",
  [targetStateStarting]: "state-starting",
  [targetStateStopping]: "state-starting",
  [targetStateRestarting]: "state-starting",
  [targetStateResuming]: "state-starting",
  [targetStateReloading]: "state-starting",
};
const targetStateRanks = {
  [targetStateDisabled]: 0,
  [targetStateStopped]: 1,
  [targetStateStarting]: 2,
  [targetStateCollecting]: 3,
  [targetStateTesting]: 3,
  [targetStateRecovering]: 3,
  [targetStateRebuilding]: 3,
  [targetStateRepairing]: 3,
  [targetStateMoving]: 3,
  [targetStateMerging]: 3,
  [targetStateStarted]: 4,
  [targetStateActive]: 4,
  [targetStateMonitored]: 5,
  [targetStateFailed]: 7,
  [targetStateMissing]: 7,
  [targetStateRunning]: 2,
  [targetStatePaused]: 3,
  [targetStateOK]: 5,
  [targetStateWarning]: 6,
  [targetStateStale]: 6,
  [targetStateStopping]: 1,
  [targetStateRestarting]: 1,
  [targetStateResuming]: 1,
  [targetStateReloading]: 1,
};
const operationActionStates = {
  [actionStart]: targetStateStarting,
  [actionRepair]: targetStateStarting,
  [actionStop]: targetStateStopping,
  [actionRestart]: targetStateRestarting,
  [actionResume]: targetStateResuming,
  [actionReload]: targetStateReloading,
};
const runtimeMetricDefs = [
  { key: metricNameCPU, label: "CPU", unit: metricUnitPercent, chartLabel: "Daemon CPU metric chart" },
  { key: metricNameMemory, label: "Memory", unit: metricUnitBytes, chartLabel: "Daemon memory metric chart" },
  { key: metricNameIO, label: "IO", unit: metricUnitBytesPerSecond, chartLabel: "Daemon IO metric chart" },
];

function expansionKey(prefix, name) { return `${prefix}${name}`; }
function expansionName(key, prefix) { return key.slice(prefix.length); }
function appExpansionKey(name) { return expansionKey(expansionPrefixApp, name); }
function libraryExpansionKey(name) { return expansionKey(expansionPrefixLibrary, name); }
function serviceExpansionKey(name) { return expansionKey(expansionPrefixService, name); }
function watchExpansionKey(name) { return expansionKey(expansionPrefixWatch, name); }
function isAppExpansionKey(key) { return key.startsWith(expansionPrefixApp); }
function isLibraryExpansionKey(key) { return key.startsWith(expansionPrefixLibrary); }
function isServiceExpansionKey(key) { return key.startsWith(expansionPrefixService); }
function isWatchExpansionKey(key) { return key.startsWith(expansionPrefixWatch); }
function isShareableExpansionKey(key) {
  return isServiceExpansionKey(key) || isWatchExpansionKey(key) || isAppExpansionKey(key) || isLibraryExpansionKey(key);
}
function isServicePreflightAction(action) { return servicePreflightActions.includes(action); }
function isServiceCascadeAction(action) { return serviceCascadeActions.includes(action); }
function isDangerServiceAction(action) { return action === actionStop || action === actionRestart || action === actionRepair; }

// Action feedback must survive the dashboard refresh that almost every action
// triggers: load() ends with a status clear, which used to wipe e.g.
// "umount failed: device busy" after ~100ms. A kinded message holds the line
// for statusStickyMs against that refresh clear; explicit setStatus("") calls
// (before a new action) still clear immediately.
const statusStickyMs = 5000;
let statusStickyUntil = 0;

function setStatus(msg, kind, sticky = true) {
  const el = $("#err");
  if (!el) return;
  const text = msg || "";
  statusStickyUntil = text && kind && sticky ? Date.now() + statusStickyMs : 0;
  const statusCls = text ? (kind === feedbackStatusOK ? "status-ok" : kind === feedbackStatusWarn ? "status-warn" : "status-err") : "";
  const prevCls = el.classList.contains("status-ok") ? "status-ok"
    : el.classList.contains("status-warn") ? "status-warn"
    : el.classList.contains("status-err") ? "status-err" : "";
  if (el.textContent === text && prevCls === statusCls) return;
  el.textContent = text;
  el.classList.remove("status-err", "status-ok", "status-warn");
  if (statusCls) el.classList.add(statusCls);
}

// Do not expose action controls until /api/whoami positively confirms that the
// current browser may use them. This avoids a transient fail-open UI when the
// identity request is unavailable during startup or a network error.
let me = { can_act: false, role: "", auth: true };

async function loadMe() {
  try {
    const res = await fetch(apiWhoamiPath);
    if (res.ok) me = await res.json();
  } catch { /* keep defaults */ }
  if (!me.auth) { $("#me").innerHTML = ""; }
  else if (me.role === "admin") { $("#me").textContent = "(admin)"; }
  else { $("#me").innerHTML = 'read-only &middot; <a href="login">log in</a>'; }
  // Show admin-only controls (reload config, clear event log).
  const reloadBtn = $("#reload-btn");
  if (reloadBtn) reloadBtn.classList.toggle("admin-hidden", !me.can_act);
  updateEventAdminControls();
  updateStateCompactControls();
  updatePanicControls();
}

// Connection state: when a fetch fails the table is dimmed and a "disconnected,
// retrying" banner (with the age of the last good update) replaces the refresh
// status, instead of silently showing stale data.
let connOK = true;
let lastLoadOk = Date.now();
let loadSeq = 0;
let dashboardGeneration = 0;
function targetPostOptions(headers = {}) {
  const generation = dashboardGeneration > 0
    ? { [apiHeaderGeneration]: String(dashboardGeneration) }
    : {};
  return csrfPostOptions({ ...generation, ...headers });
}
function showDisconnected() {
  document.body.classList.add("disconnected");
  const age = lastLoadOk ? ` (last update ${fmtSince(Date.now() - lastLoadOk)} ago)` : "";
  // Not sticky: the banner is re-asserted on every failed poll and must
  // disappear on the first successful refresh after the connection recovers.
  setStatus("Disconnected — retrying…" + age, feedbackStatusWarn, false);
}

// clearStatusAfterRefresh is load()'s end-of-refresh clear: it respects the
// sticky hold so a just-shown action result stays readable across the reload
// the action itself triggered.
function clearStatusAfterRefresh() {
  if (Date.now() < statusStickyUntil) return;
  setStatus("");
}

// load refreshes the dashboard in two phases: first the lightweight status and
// service panels, then the panels that can be cold and probe-heavy. This keeps a
// first visit after daemon start from waiting on /api/watches before showing the
// operator the main service/status view. Each endpoint still degrades
// independently to "keep the last render" on a transient error.
let loadRequested = 0;
let loadCompleted = 0;
let loadWorker = null;

function load() {
  setRefreshPending(false);
  loadRequested++;
  if (!loadWorker) {
    loadWorker = runLoadQueue().finally(() => { loadWorker = null; });
  }
  return loadWorker;
}

async function runLoadQueue() {
  while (loadCompleted < loadRequested) {
    const target = loadRequested;
    await performLoad();
    loadCompleted = target;
  }
}

function snapshotResult(snapshot, key, fallback) {
  return snapshot && Object.prototype.hasOwnProperty.call(snapshot, key)
    ? { ok: true, data: snapshot[key] }
    : { ok: false, data: fallback };
}

async function loadPrimaryDashboard() {
  const aggregate = await getJSONResult(dashboardAPI(daemonMetricWindow), null);
  if (aggregate.ok) {
    const snapshot = aggregate.data || {};
    const generation = Number(snapshot.generation) || aggregate.generation;
    return {
      servicesResult: snapshotResult(snapshot, "services", null),
      sessionsResult: snapshotResult(snapshot, "sessions", null),
      mountsResult: snapshotResult(snapshot, "mounts", null),
      notifiersResult: snapshotResult(snapshot, "notifiers", null),
      daemonResult: snapshotResult(snapshot, "daemon", null),
      daemonMetricsResult: snapshotResult(snapshot, "daemon_metrics", null),
      locksResult: snapshotResult(snapshot, "locks", null),
      activityResult: snapshotResult(snapshot, "activity", null),
      readyResult: snapshotResult(snapshot, "ready", {}),
      liveResult: snapshotResult(snapshot, "live", {}),
      monResult: snapshotResult(snapshot, "monitoring", {}),
      hostMetricsResult: snapshotResult(snapshot, "host_metrics", []),
      generation,
      generationMismatch: !!(generation && aggregate.generation && generation !== aggregate.generation),
      daemonReachable: true,
    };
  }

  const results = await Promise.all([
    getJSONResult(apiServicesPath, null),
    getJSONResult(apiSessionsPath, null),
    getJSONResult(apiMountsPath, null),
    getJSONResult(apiNotifiersPath, null),
    getJSONResult(apiDaemonPath, null),
    getJSONResult(daemonMetricsAPI(daemonMetricWindow), null),
    getJSONResult(apiLocksPath, null),
    getJSONResult(apiActivityPath, null),
    fetchReadyReportResult(),
    getJSONResult(liveVerbosePath, {}),
    getJSONResult(apiMonitoringPath, {}),
    getJSONResult(apiHostPath, []),
  ]);
  const [servicesResult, sessionsResult, mountsResult, notifiersResult, daemonResult, daemonMetricsResult,
    locksResult, activityResult, readyResult, liveResult, monResult,
    hostMetricsResult] = results;
  const { generation, mismatch: generationMismatch } = sharedBackendGeneration(results);
  return { servicesResult, sessionsResult, mountsResult, notifiersResult, daemonResult, daemonMetricsResult,
    locksResult, activityResult, readyResult, liveResult, monResult,
    hostMetricsResult, generation, generationMismatch,
    // Derived from the same collection the endpoints live in, so adding a new
    // endpoint to the Promise.all above keeps reachability in sync for free.
    daemonReachable: results.some((r) => r && r.ok) };
}

async function performLoad() {
  const seq = ++loadSeq;
  healthIconReady = false;
  let expandedServicesPromise = Promise.resolve(true);
  const sameLoad = () => seq === loadSeq;
  const { servicesResult, sessionsResult, mountsResult, notifiersResult, daemonResult,
    daemonMetricsResult, locksResult, activityResult, readyResult, liveResult,
    monResult, hostMetricsResult, generation, generationMismatch,
    daemonReachable } = await loadPrimaryDashboard();
  if (!sameLoad()) return;
  if (generationMismatch) {
    load();
    return;
  }
  dashboardGeneration = generation;
  const services = servicesResult.data;
  const sessions = sessionsResult.data;
  const mounts = mountsResult.data;
  const notifiers = notifiersResult.data;
  const daemon = daemonResult.data;
  const daemonMetrics = daemonMetricsResult.data;
  const locks = locksResult.data;
  const activity = activityResult.data;
  const ready = readyResult.data;
  const live = liveResult.data;
  const mon = monResult.data;
  const hostMetrics = hostMetricsResult.data;
  if (servicesResult.ok) {
    render(services);
    connOK = true;
    refreshFailures = 0;
    lastLoadOk = Date.now();
    document.body.classList.remove("disconnected");
    clearStatusAfterRefresh();
    // Open expansions fetch fresh detail once per poll here; re-renders in
    // between (filter keystrokes, ops ticker) only re-assert cached content.
    expandedServicesPromise = refreshExpandedServices({ generation });
  } else if (daemonReachable) {
    // Disconnected means the daemon is unreachable, not that one endpoint
    // failed: a services-only error must not dim a dashboard whose other
    // sections still answer.
    connOK = true;
    refreshFailures = 0;
    document.body.classList.remove("disconnected");
    // Only claim to keep a list when one was actually rendered before.
    const hasList = (allServices || []).length > 0;
    setStatus(hasList ? "services unavailable — keeping the last known list" : "services unavailable", feedbackStatusWarn, false);
  } else {
    connOK = false;
    refreshFailures++;
    showDisconnected();
  }
  if (mounts) renderMounts(mounts);
  if (sessionsResult.ok) renderSessions(sessions);
  if (notifiers) renderNotifiers(notifiers);
  if (daemon) renderDaemon(daemon);
  if (daemonMetrics) renderDaemonMetrics(daemonMetrics);
  if (locks) renderLocks(locks);
  if (activity) renderActivity(activity);

  // Status/readyz updates every refresh even when api/services fails, so the
  // header lifecycle line does not stay stuck on a stale "starting".
  renderStatus({
    ready,
    live,
    mon,
    locks: locks || latestLocks,
    daemon: daemon || {},
    // The failure fallback for host metrics is [] (truthy), so keeping the
    // last known readings needs the result's ok flag, not || on the data.
    hostMetrics: hostMetricsResult.ok ? hostMetrics : latestHostMetrics,
  });
  applyHash();
  if (connOK) {
    healthIconReady = true;
    renderAttention();
  } else {
    healthIconReady = true;
    setFavicon(healthStatusWarning);
  }

  const [watchesResult, appsResult, librariesResult, eventsResult, expandedServicesOK] = await Promise.all([
    getJSONResult(apiWatchesPath, null, generation),
    getJSONResult(apiApplicationsPath, null, generation),
    getJSONResult(apiLibrariesPath, null, generation),
    connOK ? loadEvents(seq, false, generation) : Promise.resolve({ ok: false }),
    expandedServicesPromise,
  ]);
  if (!sameLoad()) return;
  if (watchesResult.generationMismatch || appsResult.generationMismatch || librariesResult.generationMismatch || eventsResult.generationMismatch) {
    load();
    return;
  }
  if (watchesResult.ok) {
    renderWatches(watchesResult.data);
    if (connOK) renderAttention();
  }
  if (appsResult.ok) {
    renderApps(appsResult.data);
    if (connOK) renderAttention();
  }
  if (librariesResult.ok) renderLibraries(librariesResult.data);
  if (!connOK) return;

  // First complete snapshot: watches and apps have now loaded, so render the
  // alert-count tile and the attention banner for real (they were held neutral
  // until here). Later polls already carry this data into phase one, so this
  // one-time reveal runs only on the initial load.
  if (!firstSnapshotDone) {
    firstSnapshotDone = true;
    renderOverview({ ready, live, mon, locks: locks || latestLocks, hostMetrics: hostMetricsResult.ok ? hostMetrics : latestHostMetrics });
    renderAttention();
  }

  const [expandedWatchesOK, expandedApplicationsOK] = await Promise.all([
    watchesResult.ok ? refreshExpandedWatches(generation) : Promise.resolve(false),
    appsResult.ok ? refreshExpandedApplications(generation) : Promise.resolve(false),
  ]);
  if (!sameLoad()) return;

  const refreshResults = [
    ["sessions", sessionsResult], ["mounts", mountsResult], ["notifiers", notifiersResult], ["daemon", daemonResult],
    ["daemon metrics", daemonMetricsResult], ["locks", locksResult], ["activity", activityResult],
    ["readiness", readyResult], ["liveness", liveResult], ["monitoring", monResult],
    ["host metrics", hostMetricsResult], ["watches", watchesResult],
    ["applications", appsResult], ["libraries", librariesResult], ["events", eventsResult],
    ["service details", { ok: expandedServicesOK }],
    ["watch details", { ok: expandedWatchesOK }],
    ["application details", { ok: expandedApplicationsOK }],
  ];
  const failures = refreshResults.filter(([, result]) => !result.ok).map(([name]) => name);
  if (failures.length) {
    showPartialRefresh(failures);
    return;
  }
  // The services failure already produced the more useful keep-last-list
  // message in phase one. Do not clear it or mark this poll fully refreshed.
  if (!servicesResult.ok) return;
  lastRefresh = Date.now();
  tickRefreshAge();
  clearStatusAfterRefresh();
}

// redirectToLogin reacts to a 401 on an API call. The server sends the Basic
// challenge only on a document navigation and on /login, so that a background
// poll or an EventSource reconnect can no longer make the browser throw a
// password box at someone who was just reading the dashboard. The cost of that
// is that a 401 here would otherwise fail silently, so the page goes to /login,
// which is the deliberate way to summon the dialog and return home. Guarded so
// several concurrent polls cannot each start a navigation.
let loginRedirectStarted = false;
function redirectToLogin(res) {
  if (!res || res.status !== httpStatusUnauthorized) return false;
  if (!loginRedirectStarted) {
    loginRedirectStarted = true;
    window.location.assign(loginPath);
  }
  return true;
}

// jsonOrThrow parses a POST response as JSON (tolerating an empty body) and throws
// with the server message (or HTTP status) when the request or its result failed.
// actionResult is the one parser of action responses: the login redirect is
// handled first, the body parses leniently (an empty or non-JSON reply is an
// empty object, never a second error hiding the first), and failure is derived
// from the HTTP status and the body's own ok flag. Callers that need the body
// even on failure (probe readings, mount blockers) read it from here;
// jsonOrThrow stays the throwing form for callers that do not.
async function actionResult(res) {
  if (redirectToLogin(res)) throw new Error("authentication required");
  const body = await res.json().catch(() => ({}));
  return { body, failed: !res.ok || body.ok === false, status: res.status };
}

function actionError(body, status) {
  return new Error(body.message || ("HTTP " + status));
}

async function jsonOrThrow(res) {
  const { body, failed, status } = await actionResult(res);
  if (failed) throw actionError(body, status);
  return body;
}

async function reloadConfig() {
  setStatus("");
  const btn = $("#reload-btn");
  if (btn) btn.disabled = true;
  try {
    const res = await fetch(apiReloadPath, csrfPostOptions());
    await jsonOrThrow(res);
    setStatus("config reload requested", feedbackStatusOK);
    // next auto-refresh (or manual load) will pick up any service changes
    setTimeout(load, 800);
  } catch (e) {
    setStatus("reload failed: " + e.message, feedbackStatusErr);
  } finally {
    if (btn) btn.disabled = false;
  }
}

let eventNextBeforeID = 0;
let eventHasMore = false;

function eventPageEvents(page) {
  return Array.isArray(page.events) ? page.events : [];
}

async function loadEvents(seq = 0, append = false, generation = dashboardGeneration) {
  try {
    const params = new URLSearchParams({ [apiQueryLimit]: eventLogLimit });
    const add = (id, key) => {
      const el = $("#" + id);
      const v = el ? el.value.trim() : "";
      if (v) params.set(key, v);
    };
    add("event-service", apiQueryService);
    add("event-watch", apiQueryWatch);
    add("event-kind", apiQueryKind);
    add("event-status", apiQueryStatus);
    // "all" is the explicit any-time choice and sends no window at all.
    const range = ($("#event-range") || {}).value || "";
    if (range && range !== eventRangeAll) params.set(apiQuerySince, range);
    // An absolute "from" narrows the server fetch too: the API's since only
    // takes durations, so send now-from and let the exact bounds filter
    // client-side in renderGlobalEvents.
    const { fromMs } = eventDateBounds();
    if (fromMs && !params.has(apiQuerySince)) {
      const sinceMs = Date.now() - fromMs;
      if (sinceMs > 0) params.set(apiQuerySince, `${Math.ceil(sinceMs / millisecondsPerSecond)}s`);
    }
    if ($("#event-errors") && $("#event-errors").checked) params.set(apiQueryOnlyErrors, queryBoolOne);
    if (append && eventNextBeforeID > 0) params.set(apiQueryBeforeID, String(eventNextBeforeID));
    const { stale, res } = await fetchPinned(eventsAPI(params), generation, { reload: !seq });
    if (stale) {
      return { ok: false, generationMismatch: true };
    }
    if (!res.ok) return { ok: false };
    const page = await res.json();
    const events = eventPageEvents(page);
    if (seq && seq !== loadSeq) return { ok: true };
    if (append) {
      const known = new Set(allEvents.map((event) => event.id));
      allEvents = allEvents.concat(events.filter((event) => !known.has(event.id)));
    } else {
      allEvents = events;
    }
    eventNextBeforeID = Number(page.next_before_id) || 0;
    eventHasMore = !!page.has_more;
    renderGlobalEvents();
    return { ok: true };
  } catch {
    return { ok: false }; // keep the last feed on a transient error
  }
}

async function loadOlderEvents() {
  const button = $("#event-more");
  if (button) button.disabled = true;
  await loadEvents(0, true);
  if (button) button.disabled = false;
}

function flushLoadEvents() {
  saveUIState();
  loadEvents();
}

function eventFilterKey(e) {
  if (e.key === keyEscape) clearEventFilters();
}

function clearEventFilters() {
  ["event-service", "event-watch", "event-kind", "event-status", "event-range", "event-from", "event-to"].forEach((id) => {
    const el = $("#" + id);
    if (el) el.value = "";
  });
  const err = $("#event-errors");
  if (err) err.checked = false;
  saveUIState();
  loadEvents();
}

// toggleAdminControls is the one owner of the admin-hidden rule: the listed
// controls exist only for operators who can act.
function toggleAdminControls(...selectors) {
  selectors.forEach((selector) => {
    const el = $(selector);
    if (el) el.classList.toggle("admin-hidden", !me.can_act);
  });
}

function updateEventAdminControls() {
  toggleAdminControls("#event-clear", "#event-before");
}

function updateStateCompactControls() {
  toggleAdminControls("#state-compact-btn", "#state-before");
}

// ---- Panic mode ----
let panicOn = false;
let panicResolve = null;

// updatePanicControls shows the footer button only to operators who can act.
function updatePanicControls() {
  toggleAdminControls("#panic-btn");
}

// updatePanicView reflects the current panic state in the button, banner and
// internal flag. Called every refresh from renderStatus.
function updatePanicView(active) {
  panicOn = !!active;
  const btn = $("#panic-btn");
  if (btn) {
    btn.textContent = panicOn ? "exit panic mode" : "panic mode";
    btn.classList.toggle("active", panicOn);
    btn.title = panicOn
      ? "Resume hooks, alerts and automatic remediation"
      : "Suspend hooks, alerts and automatic remediation";
    btn.setAttribute("aria-label", panicOn
      ? "Exit panic mode and resume hooks, alerts and automatic remediation"
      : "Enter panic mode and suspend hooks, alerts and automatic remediation");
  }
  const banner = $("#panic-banner");
  if (banner) banner.classList.toggle("active", panicOn);
}

function panicConfirm(enable) {
  const dlg = $("#panic-confirm");
  const title = $("#panic-title");
  const msg = $("#panic-message");
  const okBtn = $("#panic-confirm-btn");
  if (title) title.textContent = enable ? "Enter panic mode?" : "Exit panic mode?";
  if (msg) {
    msg.innerHTML = enable
      ? "Sermo will <b>suspend all hooks, alerts and automatic remediation</b> across the daemon. Monitoring keeps running and manual actions stay available.<br><br>The daemon status will show <b>panic mode</b> until you turn it off."
      : "Sermo will <b>resume normal operation</b>: hooks, alerts and automatic remediation will fire again.";
  }
  if (okBtn) {
    okBtn.textContent = enable ? "enter panic mode" : "exit panic mode";
    okBtn.setAttribute("aria-label", enable ? "Enter panic mode" : "Exit panic mode");
  }
  if (!dlg || typeof dlg.showModal !== "function") {
    return promptConfirm({
      title: enable ? "Enter panic mode?" : "Exit panic mode?",
      message: enable
        ? "Suspend all hooks, alerts and automatic remediation?"
        : "Resume hooks, alerts and automatic remediation?",
      okLabel: enable ? "enter panic mode" : "exit panic mode",
      danger: enable,
    });
  }
  return new Promise((resolve) => {
    panicResolve = resolve;
    dlg.oncancel = () => closePanicConfirm(false);
    dlg.showModal();
  });
}

function closePanicConfirm(ok) {
  const dlg = $("#panic-confirm");
  if (dlg && dlg.open) dlg.close();
  const resolve = panicResolve;
  panicResolve = null;
  if (resolve) resolve(!!ok);
}

async function requestPanic(enable) {
  if (!me.can_act) return;
  const ok = await panicConfirm(enable);
  if (!ok) return;
  setStatus("");
  const btn = $("#panic-btn");
  if (btn) btn.disabled = true;
  try {
    const res = await fetch(panicAPI(enable), csrfPostOptions());
    const body = await jsonOrThrow(res);
    updatePanicView(enable);
    setStatus(body.message || (enable ? "panic mode enabled" : "panic mode disabled"), enable ? feedbackStatusErr : feedbackStatusOK);
    await load();
  } catch (e) {
    setStatus(`panic mode: ${e.message}`, feedbackStatusErr);
  } finally {
    if (btn) btn.disabled = false;
  }
}

async function clearEventLog(beforeValue) {
  if (!me.can_act) return;
  const before = (beforeValue != null ? beforeValue : ($("#event-before")?.value || "")).trim();
  const msg = before
    ? `Clear persisted events older than ${before}? This cannot be undone.`
    : "Clear all persisted events from the activity log? This cannot be undone.";
  if (!(await promptConfirm({ title: "Clear event log?", message: msg, okLabel: "clear log", danger: true }))) return;
  setStatus("");
  const btn = $("#event-clear");
  if (btn) btn.disabled = true;
  try {
    const q = before ? `?${apiQueryBefore}=${encodeURIComponent(before)}` : "";
    const res = await fetch(eventsClearAPI(q), csrfPostOptions());
    const body = await jsonOrThrow(res);
    const n = Number(body.pruned) || 0;
    setStatus(n ? `cleared ${n} event${n === 1 ? "" : "s"}` : "no events to clear", feedbackStatusOK);
    await load();
  } catch (e) {
    setStatus(`events clear: ${e.message}`, feedbackStatusErr);
  } finally {
    if (btn) btn.disabled = false;
  }
}

async function compactState() {
  if (!me.can_act) return;
  const before = ($("#state-before")?.value || "").trim();
  const msg = before
    ? `Compact state history older than ${before} and vacuum the database?`
    : "Compact state history using the normal retention window and vacuum the database?";
  if (!(await promptConfirm({ title: "Compact state?", message: msg, okLabel: "compact", danger: true }))) return;
  setStatus("");
  const btn = $("#state-compact-btn");
  if (btn) btn.disabled = true;
  try {
    const q = before ? `?${apiQueryBefore}=${encodeURIComponent(before)}` : "";
    const res = await fetch(stateCompactAPI(q), csrfPostOptions());
    const body = await jsonOrThrow(res);
    const n = Number(body.pruned) || 0;
    setStatus(n ? `compacted state: pruned ${n} row${n === 1 ? "" : "s"}` : (body.message || "state compact completed"), feedbackStatusOK);
    await load();
  } catch (e) {
    setStatus(`state compact: ${e.message}`, feedbackStatusErr);
  } finally {
    if (btn) btn.disabled = false;
  }
}

function eventKey(prefix, e, i) {
  return e.id ? `${prefix}:id:${e.id}` : `${prefix}:${i}:${e.time || ""}:${e.service || ""}:${e.watch || ""}:${e.kind || ""}:${e.action || ""}:${e.status || ""}`;
}

function toggleEventMsg(key) {
  if (eventExpanded.has(key)) eventExpanded.delete(key);
  else eventExpanded.add(key);
  renderGlobalEvents();
  expanded.forEach((expKey) => {
    if (isServiceExpansionKey(expKey)) loadServiceEvents(expansionName(expKey, expansionPrefixService));
    else if (isAppExpansionKey(expKey)) loadAppEvents(expansionName(expKey, expansionPrefixApp));
  });
}

function eventMessageHTML(e, key) {
  const msg = e.message || "";
  const msgId = key + "-msg";
  const msgOpen = eventExpanded.has(key);
  const truncated = msg.length > eventMessagePreviewChars && !msgOpen;
  const text = truncated
    ? tpl`<span id="${msgId}" class="event-msg">${msg.slice(0, eventMessagePreviewChars)}<span class="muted">…</span> <button type="button" data-event-toggle="${key}" aria-expanded="${domBoolFalse}" aria-controls="${msgId}" aria-label="Show full event message">more</button></span>`
    : tpl`<span id="${msgId}" class="event-msg">${msg}${msg.length > eventMessagePreviewChars ? tpl` <button type="button" data-event-toggle="${key}" aria-expanded="${domBoolTrue}" aria-controls="${msgId}" aria-label="Show less of event message">less</button>` : nothing}</span>`;
  // Bounded stdout/stderr of the failing command, collapsed behind an "output"
  // toggle so the multi-line blob does not clutter the row by default.
  const out = e.output || "";
  if (!out) return text;
  const okey = key + ":out";
  const outId = okey + "-panel";
  const outOpen = eventExpanded.has(okey);
  return outOpen
    ? tpl`${text} <button type="button" data-event-toggle="${okey}" aria-expanded="${domBoolTrue}" aria-controls="${outId}" aria-label="Hide command output">hide output</button><pre id="${outId}" class="event-output">${out}</pre>`
    : tpl`${text} <button type="button" data-event-toggle="${okey}" aria-expanded="${domBoolFalse}" aria-controls="${outId}" aria-label="Show command output">output</button>`;
}

function eventSubject(e) {
  return e.service || e.watch || e.app || "";
}

function eventGroupKey(e) {
  const action = e.action || e.kind || "";
  return `${eventSubject(e)}|${action}|${e.rule || ""}`;
}

function groupedEvents(events) {
  const map = new Map();
  for (const e of events || []) {
    const key = eventGroupKey(e);
    if (!map.has(key)) map.set(key, []);
    map.get(key).push(e);
  }
  return [...map.values()];
}

// Column sort for the global events feed: empty key keeps the API order (newest
// first); clicking a header sorts by it, clicking again flips direction. Mirrors
// the services/watches/apps panels via the shared toggleSort/sortedBy helpers.
let evSort = { key: "", dir: 1 };
const evSortKeys = {
  time: (e) => e.time || "",
  subject: (e) => eventSubject(e).toLowerCase(),
  kind: (e) => (e.kind || "").toLowerCase(),
  message: (e) => (e.message || "").toLowerCase(),
};
function setEvSort(key) { toggleSort(evSort, key, renderGlobalEvents); }
function updateEvSortIndicators() {
  updateSortIndicatorsFor("ei", evSort, ".events th.sortable[data-ev-sort]", "evSort");
}
function sortedEvents(events) {
  if (!evSort.key || !evSortKeys[evSort.key]) return events;
  return sortedBy(events.slice(), evSort, evSortKeys, "time");
}

// eventDateBounds reads the absolute from/until filters as epoch-ms bounds
// (0 when unset or unparsable); datetime-local values are the viewer's local
// time, which new Date() parses as such.
function eventDateBounds() {
  const read = (id) => {
    const el = $("#" + id);
    if (!el || !el.value) return 0;
    const ms = new Date(el.value).getTime();
    return Number.isFinite(ms) ? ms : 0;
  };
  return { fromMs: read("event-from"), toMs: read("event-to") };
}

function eventsWithinDateBounds(events) {
  const { fromMs, toMs } = eventDateBounds();
  if (!fromMs && !toMs) return events;
  return events.filter((e) => {
    const at = Date.parse(e.time);
    if (!Number.isFinite(at)) return true;
    return (!fromMs || at >= fromMs) && (!toMs || at <= toMs);
  });
}

function renderGlobalEvents() {
  const tbody = $("#events");
  if (!tbody) return;
  updateEvSortIndicators();
  const events = sortedEvents(eventsWithinDateBounds(allEvents || []));
  const cnt = $("#event-count");
  if (cnt) cnt.textContent = events.length ? `${events.length} shown` : "";
  const more = $("#event-more");
  if (more) more.hidden = !eventHasMore;
  syncEventTargetFilters();
  updateSectionNav();
  const grouped = $("#event-group") && $("#event-group").checked;
  if (!grouped) {
    // Loaded-but-filtered-out must not read as an empty log: the date bounds
    // (and server-side filters) may hide every fetched event.
    if (!events.length && (allEvents || []).length) {
      litRender(tpl`<tr><td class="muted">No events match the filter.</td></tr>`, tbody);
      return;
    }
    litRender(eventRows(events, true, { prefix: "global" }), tbody);
    return;
  }
  const groups = groupedEvents(events);
  if (!groups.length) {
    litRender(tpl`<tr><td class="muted">No events match the filter.</td></tr>`, tbody);
    return;
  }
  litRender(groups.map((g, gi) => {
    const head = g[0] || {};
    const who = eventSubject(head) || "system";
    const action = head.action || head.kind || "event";
    const statuses = [...new Set(g.map((e) => e.status).filter(Boolean))].join(", ");
    const groupKey = `grp:${eventGroupKey(head)}`;
    const panelId = `event-grp-panel-${gi}`;
    const open = eventExpanded.has(groupKey);
    return [
      tpl`<tr class="event-group">
        <td colspan="4"><button type="button" class="row-toggle" data-event-toggle="${groupKey}" aria-expanded="${open ? domBoolTrue : domBoolFalse}" aria-controls="${panelId}"><span class="exp" aria-hidden="true">${open ? "▾" : "▸"}</span>${who} <span class="muted">${action} · ${g.length} event${g.length === 1 ? "" : "s"}${statuses ? " · " + statuses : ""}</span></button></td>
      </tr>`,
      open ? eventRows(g, true, { prefix: "group" + gi, panelId }) : nothing,
    ];
  }), tbody);
}

function syncEventTargetFilter(id, allLabel, names) {
  const select = $(id);
  if (!select) return;
  const current = select.value;
  const values = [...new Set(names.filter(Boolean))].sort((a, b) => a.localeCompare(b));
  if (current && !values.includes(current)) values.push(current);
  select.replaceChildren(new Option(allLabel, ""), ...values.map((value) => new Option(value, value)));
  select.value = current;
}

function syncEventTargetFilters() {
  syncEventTargetFilter("#event-service", "all services", [
    ...(allServices || []).map((service) => service.name),
    ...(allEvents || []).map((event) => event.service),
  ]);
  syncEventTargetFilter("#event-watch", "all watches", [
    ...(allWatches || []).map((watch) => watch.name),
    ...(allEvents || []).map((event) => event.watch),
  ]);
}

// fmtLocalTime renders a timestamp in the viewer's local zone, for hover
// titles only — the visible value stays the daemon's canonical UTC.
function fmtLocalTime(value) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "" : date.toLocaleString();
}

// fmtTimeTitled is the canonical UTC timestamp with the viewer's local time as
// its hover title, so operators in other zones never convert mentally.
function fmtTimeTitled(value) {
  const local = fmtLocalTime(value);
  if (!local) return fmtTime(value);
  return tpl`<span title="${local + " local"}">${fmtTime(value)}</span>`;
}

function eventRows(events, withService, opts = {}) {
  const cols = withService ? 4 : 3;
  if (!events || !events.length) return tpl`<tr><td colspan="${cols}" class="muted">No events yet.</td></tr>`;
  const prefix = opts.prefix || "event";
  return events.map((e, i) => {
    const who = e.service || e.watch || e.app || "";
    const detail = [e.rule, e.action, e.status].filter(Boolean).join(" ");
    const key = eventKey(prefix, e, i);
    const rowId = opts.panelId && i === 0 ? opts.panelId : nothing;
    return tpl`<tr id="${rowId}">
      <td class="t">${fmtTimeTitled(e.time)}</td>
      ${withService && who ? tpl`<td>${who}</td>` : nothing}
      <td class="kind kind-${e.kind || ""}">${e.kind}</td>
      <td>${detail ? tpl`<span class="muted">${detail}</span> ` : nothing}${eventMessageHTML(e, key)}</td>
    </tr>`;
  });
}

function renderEventsLoading(target, cols = 3) {
  if (target && !target.childNodes.length) {
    litRender(tpl`<tr><td colspan="${cols}" class="muted">loading…</td></tr>`, target);
  }
}

function fmtMonitorSource(src) {
  switch (src) {
    case "cli": return "via sermoctl";
    case "web": return "via web UI";
    case "config": return "via config";
    case "daemon": return "via daemon";
    default: return src ? "via " + src : "";
  }
}

function unitCell(s) {
  // The init backend is system-wide (shown once in the daemon status), so the
  // per-row cell shows only the unit.
  const unit = s.unit ? tpl`<span class="mono" title="${s.unit}">${s.unit}</span>` : tpl`<span class="muted">—</span>`;
  return tpl`<div class="unit-cell">${unit}</div>`;
}

function policyStateClass(state) {
  switch (state) {
    case policyStateEligible: return "ok";
    case policyStateCooldown:
    case policyStateRateLimit:
    case policyStateBlocked: return "inactive";
    case targetStateDisabled:
    case targetStatePaused:
    case policyStatePending: return healthStatusMuted;
    default: return healthStatusMuted;
  }
}

function policyCell(s) {
  // remediation_state is always sent (decorateRemediation covers every path).
  const state = s.remediation_state || backendStatusUnknown;
  // A paused service shows its state in the State column; don't repeat it here.
  if (state === targetStatePaused) return tpl`<span class="muted">—</span>`;
  const cd = s.policy_cooldown ? tpl`<div class="muted mono">${s.policy_cooldown}</div>` : nothing;
  return tpl`<div class="policy-cell"><span class="${policyStateClass(state)}">${state}</span>${cd}</div>`;
}

// locksCell names the active locks, it does not just count them: they are what
// blocks every engine action on the service, so the operator needs to know which
// lock to go look at. A title alone would not do — it is unreachable on touch
// and invisible to a keyboard user.
function locksCell(s) {
  const locks = serviceLockNames(s);
  if (!locks.length) return tpl`<span class="muted count-badge">0</span>`;
  const label = locks.join(", ");
  return tpl`<span class="bad count-badge">${locks.length}</span> <span class="truncate lock-names" title="${label}">${label}</span>`;
}

function lastEventCell(s) {
  return activityDateCell(s && s.last_event);
}

function lastEventTime(item) {
  return (item && item.last_event && item.last_event.time) || "";
}

function activitySeverity(kind, status) {
  const k = String(kind || "").toLowerCase();
  const st = String(status || "").toLowerCase();
  if (activityCriticalStatuses.includes(st)) return healthStatusCriticalShort;
  if (st === policyStateBlocked) return healthStatusWarningShort;
  if (activityCriticalKinds.includes(k)) return healthStatusCriticalShort;
  if (activityWarningKinds.includes(k)) return healthStatusWarningShort;
  if (activityOKKinds.includes(k)) return healthStatusOK;
  if (activityInfoKinds.includes(k)) return healthStatusInfo;
  return healthStatusMuted;
}

function activityDateCell(e) {
  const time = e && e.time;
  if (!time) return tpl`<span class="muted">—</span>`;
  const kind = e.kind || "";
  const detail = [kind, e.action, e.status].filter(Boolean).join(" ");
  const local = fmtLocalTime(time);
  const title = [fmtTime(time), local && local + " local", detail, e.message || ""].filter(Boolean).join(" · ");
  const cls = "activity-time activity-" + activitySeverity(kind, e.status);
  const label = [fmtTime(time), detail || "activity"].filter(Boolean).join(" · ");
  return tpl`<div class="event-cell" title="${title}"><span class="${cls}" aria-label="${label}">${fmtTime(time)}</span></div>`;
}

function nextRemediationCell(s) {
  if (!s.enabled) return tpl`<span class="muted">disabled</span>`;
  const state = s.remediation_state || "";
  if (s.next_eligible_at) {
    return tpl`<span title="${fmtTime(s.next_eligible_at)}">${fmtUntilShort(s.next_eligible_at)}</span>`;
  }
  if (state === policyStateEligible) return tpl`<span class="ok">now</span>`;
  if (state === policyStatePending) return tpl`<span class="muted">${state}</span>`; // paused -> "—" (shown in State)
  return tpl`<span class="muted">—</span>`;
}

// Service list state: latest fetched data plus the active search/status filter,
// so typing or switching a filter re-renders from cache without a refetch.
let allServices = [];
let latestSessionInventory = {};
let sessionQuery = "";
let sessionFilter = filterAll;
let sessionSort = { key: "", dir: 1 };
let svcQuery = "";
let svcStatus = filterAll; // all | disabled | stopped | started | starting | collecting | monitored | failed
let svcCategory = filterAll;
let svcGrouped = false;
let svcCollapsedGroups = new Set();
// svcPinned lifts hand-picked services to the top of their panel (and group),
// persisted with the rest of the UI state.
let svcPinned = new Set();
let svcSort = { key: "", dir: 1 };
const splitServicePanels = {
  container: {
    query: "", status: filterAll, grouped: false, collapsedGroups: new Set(), sort: { key: "", dir: 1 },
    surface: "container", section: "#containers-section", rows: "#container-rows", count: "#containers-count",
    filterCount: "#container-count", filters: "#container-filters", search: "#container-search",
    filterDataset: "cf", sortIndicator: "ci", sortAttr: "container-sort", sortDataset: "containerSort",
    label: "containers", empty: "No containers.", emptyFiltered: "No containers match the filter.",
  },
  vm: {
    query: "", status: filterAll, grouped: false, collapsedGroups: new Set(), sort: { key: "", dir: 1 },
    surface: "vm", section: "#vms-section", rows: "#vm-rows", count: "#vms-count",
    filterCount: "#vm-count", filters: "#vm-filters", search: "#vm-search",
    filterDataset: "vf", sortIndicator: "vi", sortAttr: "vm-sort", sortDataset: "vmSort",
    label: "virtual machines", empty: "No virtual machines.", emptyFiltered: "No virtual machines match the filter.",
  },
};
let allMounts = [];
let mountQuery = "";
let mountStatus = filterAll;
let mountCategory = filterAll;
let mountGrouped = false;
let mountCollapsedGroups = new Set();
let mountSort = { key: "", dir: 1 };
let expanded = new Set(); // open expansions, keyed "svc:<name>" / "wat:<name>" / "app:<name>"
let allApps = [];
let appQuery = "";
let appCategory = filterAll;
let appStatus = filterAll;
let appGrouped = false;
let appCollapsedGroups = new Set();
let appSort = { key: "", dir: 1 };
let allLibraries = [];
let libraryQuery = "";
let libraryCategory = filterAll;
let libraryStatus = filterAll;
let libraryGrouped = false;
let libraryCollapsedGroups = new Set();
let librarySort = { key: "", dir: 1 };
const defaultMetricWindow = "24h";
const serviceMetricStates = new Map();
let daemonMetricWindow = defaultMetricWindow;
let allWatches = [];
let globalTargetsByValue = new Map();
let globalTargetSyncPending = false;
// watchPanelDescriptors is shared with the Go web builder: one descriptor owns
// static shell IDs, columns and text. The Watches panel uses semantic
// groups so a new check type does not need a parallel table or action layout.
const watchPanels = Object.fromEntries(watchPanelDescriptors.map((descriptor) => [descriptor.key, {
  ...descriptor,
  query: "",
  status: filterAll,
  type: filterAll,
  grouped: !!descriptor.grouped,
  collapsedGroups: new Set(),
  groupPrefix: descriptor.key,
  groupPanel: "watch-" + descriptor.key,
  groupLabel: descriptor.title.toLowerCase(),
  sort: { key: "", dir: 1 },
  typeSorts: {},
  typeFilters: {},
  section: "#" + descriptor.sectionId,
  rows: "#" + descriptor.rowsId,
  count: "#" + descriptor.countId,
  filterCount: "#" + descriptor.filterCountId,
  filters: "#" + descriptor.filtersId,
  search: "#" + descriptor.searchId,
  typeSelect: descriptor.typeId ? "#" + descriptor.typeId : undefined,
  cols: descriptor.columns.length,
}]));

const UI_STATE_KEY = "sermo-ui-state";
const KEYBOARD_SHORTCUTS_KEY = "sermo-keyboard-shortcuts";
const BROWSER_NOTIFY_KEY = "sermo-browser-notify";

function restoreUIState() {
  try {
    const raw = localStorage.getItem(UI_STATE_KEY);
    if (!raw) return;
    const s = JSON.parse(raw);
    if (typeof s.svcQuery === "string") svcQuery = s.svcQuery;
    if (typeof s.svcStatus === "string") svcStatus = normalizeServiceStatusFilter(s.svcStatus);
    if (typeof s.svcCategory === "string") svcCategory = s.svcCategory;
    if (typeof s.svcGrouped === "boolean") svcGrouped = s.svcGrouped;
    if (s.svcSort && typeof s.svcSort.key === "string") {
      svcSort = { key: s.svcSort.key, dir: s.svcSort.dir === -1 ? -1 : 1 };
    }
    if (s.sessionSort && typeof s.sessionSort.key === "string") {
      sessionSort = { key: s.sessionSort.key, dir: s.sessionSort.dir === -1 ? -1 : 1 };
    }
    if (s.splitServicePanels && typeof s.splitServicePanels === "object") {
      for (const [key, saved] of Object.entries(s.splitServicePanels)) {
        const panel = splitServicePanels[key];
        if (!panel || !saved) continue;
        if (typeof saved.query === "string") panel.query = saved.query;
        if (typeof saved.status === "string") panel.status = normalizeServiceStatusFilter(saved.status);
        if (saved.sort && typeof saved.sort.key === "string") {
          panel.sort = { key: saved.sort.key, dir: saved.sort.dir === -1 ? -1 : 1 };
        }
        if (saved.typeSorts && typeof saved.typeSorts === "object") panel.typeSorts = saved.typeSorts;
        if (saved.typeFilters && typeof saved.typeFilters === "object") panel.typeFilters = saved.typeFilters;
        if (typeof saved.grouped === "boolean") panel.grouped = saved.grouped;
        if (Array.isArray(saved.collapsedGroups)) panel.collapsedGroups = new Set(saved.collapsedGroups);
      }
    }
    if (typeof s.mountQuery === "string") mountQuery = s.mountQuery;
    if (typeof s.mountStatus === "string") mountStatus = normalizeMountStatusFilter(s.mountStatus);
    if (typeof s.mountCategory === "string") mountCategory = s.mountCategory;
    if (typeof s.mountGrouped === "boolean") mountGrouped = s.mountGrouped;
    if (s.mountSort && typeof s.mountSort.key === "string") {
      mountSort = { key: s.mountSort.key, dir: s.mountSort.dir === -1 ? -1 : 1 };
    }
    if (typeof s.appQuery === "string") appQuery = s.appQuery;
    if (typeof s.appStatus === "string") appStatus = s.appStatus;
    if (s.appSort && typeof s.appSort.key === "string") {
      appSort = { key: s.appSort.key, dir: s.appSort.dir === -1 ? -1 : 1 };
    }
    if (typeof s.libraryQuery === "string") libraryQuery = s.libraryQuery;
    if (typeof s.libraryStatus === "string") libraryStatus = s.libraryStatus;
    if (s.librarySort && typeof s.librarySort.key === "string") {
      librarySort = { key: s.librarySort.key, dir: s.librarySort.dir === -1 ? -1 : 1 };
    }
    if (s.watchPanels && typeof s.watchPanels === "object") {
      for (const [key, saved] of Object.entries(s.watchPanels)) {
        const panel = watchPanels[key];
        if (!panel || !saved) continue;
        if (typeof saved.query === "string") panel.query = saved.query;
        if (typeof saved.status === "string") panel.status = normalizeWatchStatusFilter(saved.status);
        if (typeof saved.type === "string") panel.type = saved.type;
        if (saved.sort && typeof saved.sort.key === "string") {
          panel.sort = { key: saved.sort.key, dir: saved.sort.dir === -1 ? -1 : 1 };
        }
        if (typeof saved.grouped === "boolean") panel.grouped = saved.grouped;
        if (Array.isArray(saved.collapsedGroups)) panel.collapsedGroups = new Set(saved.collapsedGroups);
      }
    }
    if (Array.isArray(s.expanded)) {
      expanded = new Set(s.expanded.filter((k) => typeof k === "string"));
    }
    if (s.serviceMetricStates && typeof s.serviceMetricStates === "object") {
      for (const [name, state] of Object.entries(s.serviceMetricStates)) {
        if (!state || typeof state !== "object") continue;
        serviceMetricStates.set(name, {
          window: typeof state.window === "string" ? state.window : defaultMetricWindow,
        });
      }
    }
    if (typeof s.daemonMetricWindow === "string") daemonMetricWindow = s.daemonMetricWindow;
    if (typeof s.appGrouped === "boolean") appGrouped = s.appGrouped;
    if (typeof s.libraryGrouped === "boolean") libraryGrouped = s.libraryGrouped;
    if (Array.isArray(s.svcCollapsedGroups)) svcCollapsedGroups = new Set(s.svcCollapsedGroups);
    if (Array.isArray(s.svcPinned)) svcPinned = new Set(s.svcPinned);
    if (Array.isArray(s.appCollapsedGroups)) appCollapsedGroups = new Set(s.appCollapsedGroups);
    if (Array.isArray(s.libraryCollapsedGroups)) libraryCollapsedGroups = new Set(s.libraryCollapsedGroups);
    if (Array.isArray(s.mountCollapsedGroups)) mountCollapsedGroups = new Set(s.mountCollapsedGroups);
    if (s.eventFilters && typeof s.eventFilters === "object") restoreEventFilters(s.eventFilters);
  } catch (_) {}
}

// restoreEventFilters reapplies the events panel's saved controls. Kept out of
// restoreUIState both for its size and for one rule of its own: a legacy saved
// range of "" predates the named any-time value, so it leaves the markup
// default (24h) instead of restoring an empty selection.
function restoreEventFilters(ef) {
  const setVal = (id, v) => {
    const el = $(id);
    if (!el || typeof v !== "string") return;
    if (el.tagName === "SELECT" && v && ![...el.options].some((option) => option.value === v)) {
      el.add(new Option(v, v));
    }
    el.value = v;
  };
  setVal("#event-service", ef.service);
  setVal("#event-watch", ef.watch);
  setVal("#event-kind", ef.kind);
  setVal("#event-status", ef.status);
  if (ef.range) setVal("#event-range", ef.range);
  setVal("#event-from", ef.from);
  setVal("#event-to", ef.to);
  const err = $("#event-errors");
  if (err && typeof ef.onlyErrors === "boolean") err.checked = ef.onlyErrors;
  const grp = $("#event-group");
  if (grp && typeof ef.group === "boolean") grp.checked = ef.group;
}

function saveUIState() {
  try {
    localStorage.setItem(UI_STATE_KEY, JSON.stringify({
      svcQuery, svcStatus, svcCategory, svcGrouped, svcSort, sessionSort,
      mountQuery, mountStatus, mountCategory, mountGrouped, mountSort,
      appQuery, appStatus, appSort, appGrouped,
      libraryQuery, libraryStatus, librarySort, libraryGrouped,
      serviceMetricStates: Object.fromEntries(serviceMetricStates), daemonMetricWindow,
      expanded: [...expanded],
      svcCollapsedGroups: [...svcCollapsedGroups],
      svcPinned: [...svcPinned],
      appCollapsedGroups: [...appCollapsedGroups],
      libraryCollapsedGroups: [...libraryCollapsedGroups],
      mountCollapsedGroups: [...mountCollapsedGroups],
      eventFilters: {
        service: ($("#event-service") || {}).value || "",
        watch: ($("#event-watch") || {}).value || "",
        kind: ($("#event-kind") || {}).value || "",
        status: ($("#event-status") || {}).value || "",
        range: ($("#event-range") || {}).value || "",
        from: ($("#event-from") || {}).value || "",
        to: ($("#event-to") || {}).value || "",
        onlyErrors: !!($("#event-errors") && $("#event-errors").checked),
        group: !($("#event-group") && !$("#event-group").checked),
      },
      watchPanels: Object.fromEntries(Object.entries(watchPanels).map(([k, p]) => [k, {
        query: p.query, status: p.status, type: p.type, grouped: p.grouped,
        collapsedGroups: [...p.collapsedGroups], sort: p.sort, typeSorts: p.typeSorts, typeFilters: p.typeFilters,
      }])),
      splitServicePanels: Object.fromEntries(Object.entries(splitServicePanels).map(([k, p]) => [k, {
        query: p.query, status: p.status, grouped: p.grouped,
        collapsedGroups: [...p.collapsedGroups], sort: p.sort,
      }])),
    }));
  } catch (_) {}
}

function applyUIStateToControls() {
  const svcSearch = $("#svc-search");
  if (svcSearch) svcSearch.value = svcQuery;
  syncFilterButtons("#svc-filters", "f", svcStatus);
  const svcCategorySelect = $("#svc-category");
  if (svcCategorySelect) svcCategorySelect.value = svcCategory;
  for (const key of Object.keys(splitServicePanels)) syncSplitServicePanelControls(key);
  const mountSearch = $("#mount-search");
  if (mountSearch) mountSearch.value = mountQuery;
  syncFilterButtons("#mount-filters", "mf", mountStatus);
  const mountCategorySelect = $("#mount-category");
  if (mountCategorySelect) mountCategorySelect.value = mountCategory;
  const appSearch = $("#app-search");
  if (appSearch) appSearch.value = appQuery;
  syncFilterButtons("#app-filters", "af", appStatus);
  const librarySearch = $("#library-search");
  if (librarySearch) librarySearch.value = libraryQuery;
  syncFilterButtons("#library-filters", "lf", libraryStatus);
  for (const key of Object.keys(watchPanels)) {
    const panel = watchPanels[key];
    const search = $(panel.search);
    if (search) search.value = panel.query;
    syncWatchFilterActive(key);
    const typeSelect = $(panel.typeSelect);
    if (typeSelect && panel.type) typeSelect.value = panel.type;
  }
}

restoreUIState();

let allEvents = [];
// Maps, not objects: both are indexed by an expansion key that ultimately
// comes from location.hash, and writing an attacker-shaped key into an object
// property is the prototype-pollution pattern. The previous null-prototype
// hardening was equivalent in effect, but a Map removes the property-write
// entirely (and is what static analysis can verify).
const expCache = new Map();       // last rendered expansion HTML per key (avoids flicker)
const expDetailCache = new Map(); // last /api/services/{name} JSON per svc expansion key
const expCellCache = new Map(); // live detail cells preserved across outer table renders
let eventExpanded = new Set();
const liveOps = new Map(); // operations started from this browser session, keyed by service
// Monitor/unmonitor requests in flight, keyed by expansionPrefixService /
// expansionPrefixWatch + name (serviceExpansionKey / watchExpansionKey). These
// actions are not tracked in liveOps, so this guards their buttons against a
// double click until the reply (and the follow-up load) lands.
const pendingMonitorToggles = new Set();
// Reap requests in flight, keyed by service name. A reap is an engine operation
// but not one of the tracked start/stop verbs, so liveOps does not cover it; this
// keeps the button from firing twice until the reply and the follow-up load land.
const pendingReaps = new Set();
const liveMountOps = new Map(); // mount operations started from this browser session, keyed by mount name
let liveOpsTimer = null;
let latestLocks = [];
let latestActivity = null;
let latestReady = null;
let latestHostMetrics = [];   // last /api/host readings (for process memory bars)
// Defer favicon/title until load() has the full dashboard snapshot. Avoids a
// green default flashing to red while panels hydrate.
let healthIconReady = false;
// Short host identity from GET /api/daemon (same source as the Basic realm).
// Used in the browser tab title so multi-host operators can tell tabs apart.
let dashboardHostname = "";
// The alert count and attention banner also need watches/apps, which load in a
// second phase after the aggregate dashboard. Stays false until the first
// complete snapshot so a reload never flashes a partial/false alert.
let firstSnapshotDone = false;

function targetStateClass(state) {
  return targetStateClasses[state] || healthStatusMuted;
}

function stateBadge(state) {
  return stateBadgeLabel(state, state || backendStatusUnknown);
}

function stateBadgeLabel(state, label) {
  const st = state || backendStatusUnknown;
  return tpl`<span class="target-state ${targetStateClass(st)}">${label || st}</span>`;
}

function stateRank(state) {
  return targetStateRanks[state] ?? 5;
}

// serviceState reads the server-computed state (app.ServiceState). The UI is
// embedded in the same binary, so the field is always present — deriving it
// again here would just be a second copy of that logic that could drift.
function serviceState(s) {
  return (s && s.state) || backendStatusUnknown;
}

function operationState(action) {
  return operationActionStates[action] || targetStateWorking;
}

function serviceDisplayState(s) {
  const op = s && liveOps.get(s.name);
  if (op && !op.finished) return operationState(op.action);
  return serviceState(s);
}

function serviceStateBadge(s) {
  const st = serviceDisplayState(s);
  const indicators = (s && Array.isArray(s.observability_missing) && s.observability_missing.length)
    ? s.observability_missing.join(", ")
    : "";
  const missing = (st === targetStateCollecting && indicators) ? `Collecting ${indicators}` : "";
  const staleBinary = st === targetStateWarning && s && s.warning_reason === warningReasonStaleBinary;
  const failedUnitLiveProcess = st === targetStateWarning && s && s.warning_reason === warningReasonFailedUnitLiveProcess;
  const blind = (st === targetStateWarning && indicators && !staleBinary && !failedUnitLiveProcess)
    ? `The unit is active and its checks pass, but the daemon attributes no process to it, so ${indicators} stay unavailable`
    : "";
  const degraded = failedUnitLiveProcess
    ? "The init unit is failed, but the verified service process and its functional checks are healthy"
    : "";
  const active = st === targetStateActive
    ? "Process confirmed; checks and runtime metrics are not available yet"
    : "";
  const title = missing || blind || degraded || active;
  // Except for the stale-binary diagnostic, a warning carries a reason the
  // operator must act on. A tooltip is invisible until hovered — and never
  // reachable on a touch screen — so show that reason next to the badge.
  const reason = st === targetStateWarning && !staleBinary
    ? tpl` <span class="muted state-reason" title="${title}">${serviceWarningReason(s)}</span>`
    : nothing;
  const badge = title ? tpl`<span title="${title}">${stateBadge(st)}</span>` : stateBadge(st);
  return reason ? tpl`${badge}${reason}` : badge;
}

// serviceWarningReason phrases a warning cause the dashboard needs to expose.
// The wording lives here, so the backend never has to agree on a sentence.
function serviceWarningReason(s) {
  if (s && s.warning_reason === warningReasonFailedUnitLiveProcess) {
    return "init unit failed; workload healthy";
  }
  return "no process attributed";
}

function serviceStateCell(s) {
  return tpl`${serviceStateBadge(s)}`;
}

const serviceSurfaceRegular = "service";
const serviceSurfaceContainer = "container";
const serviceSurfaceVM = "vm";
const dockerServiceCategory = "docker";
const vmServiceCategory = "virtual-machine";

function serviceSurfaceOf(s) {
  const category = categoryOf(s, defaultCategoryService).toLowerCase();
  if (category === dockerServiceCategory) return serviceSurfaceContainer;
  if (category === vmServiceCategory) return serviceSurfaceVM;
  return serviceSurfaceRegular;
}

function servicesForSurface(surface) {
  return (allServices || []).filter((s) => serviceSurfaceOf(s) === surface);
}

function defaultServicePanelTarget() {
  if (servicesForSurface(serviceSurfaceRegular).length) return "services-section";
  if (servicesForSurface(serviceSurfaceContainer).length) return "containers-section";
  if (servicesForSurface(serviceSurfaceVM).length) return "vms-section";
  return "services-section";
}

function splitServicePanelBySurface(surface) {
  return Object.values(splitServicePanels).find((panel) => panel.surface === surface) || null;
}

function serviceSectionFor(s) {
  const surface = serviceSurfaceOf(s);
  if (surface === serviceSurfaceRegular) return "#services-section";
  const panel = splitServicePanelBySurface(surface);
  return panel ? panel.section : "#services-section";
}

function openSectionForService(name) {
  const service = (allServices || []).find((s) => s && s.name === name);
  const sec = service ? $(serviceSectionFor(service)) : $("#services-section");
  if (sec) {
    setPanelVisible(sec, true);
    sec.open = true;
  }
  return sec;
}

function serviceSurfaceWithStatus(status) {
  for (const surface of [serviceSurfaceRegular, serviceSurfaceContainer, serviceSurfaceVM]) {
    if (servicesForSurface(surface).some((s) => serviceDisplayState(s) === status)) return surface;
  }
  for (const surface of [serviceSurfaceRegular, serviceSurfaceContainer, serviceSurfaceVM]) {
    if (servicesForSurface(surface).length) return surface;
  }
  return serviceSurfaceRegular;
}

function isFailing(s) { return serviceState(s) === targetStateFailed; }
function isServiceAttention(s) {
  const st = serviceState(s);
  return st === targetStateFailed;
}
function isWatchAttention(w) {
  const st = watchStateText(w);
  return st === targetStateFailed || st === targetStateMissing;
}

function isWatchSampleStale(w) {
  return watchStateText(w) === targetStateStale;
}

// isWatchWarning marks a watch whose operator graded its failures advisories. It
// still earns a place in the attention banner — a warning is worth seeing — but
// at warning level, so it never turns the favicon critical.
function isWatchWarning(w) {
  return watchStateText(w) === targetStateWarning;
}

function openServiceStatusTarget(status) {
  const normalized = normalizeServiceStatusFilter(status);
  const surface = serviceSurfaceWithStatus(normalized);
  const panel = surface === serviceSurfaceRegular ? null : splitServicePanelBySurface(surface);
  if (panel) {
    setSplitServiceStatus(surface, normalized);
  } else {
    setSvcStatus(normalized);
  }
  const sec = panel ? $(panel.section) : $("#services-section");
  if (sec) {
    setPanelVisible(sec, true);
    sec.open = true;
    scrollToSection(sec);
  }
}

// scrollToSection is the one spelling of "bring this panel into view".
function scrollToSection(el) {
  el && el.scrollIntoView({ block: scrollBlockStart, behavior: scrollBehaviorSmooth });
}

function openPanelTarget(target) {
  if (target === "failed-services") {
    openServiceStatusTarget(targetStateFailed);
    return;
  }
  if (target === "starting-services") {
    openServiceStatusTarget(targetStateStarting);
    return;
  }
  if (target === "collecting-services") {
    openServiceStatusTarget(targetStateCollecting);
    return;
  }
  if (target === "warning-services") {
    openServiceStatusTarget(targetStateWarning);
    return;
  }
  if (target === "monitored-services") {
    openServiceStatusTarget(targetStateMonitored);
    return;
  }

  if (target === "failed-apps") {
    const sec = $("#apps-section");
    if (sec) sec.open = true;
    setAppStatus(targetStateFailed);
    scrollToSection(sec);
    return;
  }
  if (target === "starting-apps") {
    const sec = $("#apps-section");
    if (sec) sec.open = true;
    setAppStatus(targetStateStarting);
    scrollToSection(sec);
    return;
  }
  if (target === "failed-watches") {
    // Each watch panel is its own <details>; a firing watch could be in any of
    // them, so open all and scroll to whichever actually holds one (in panel
    // declaration order, Watches as the fallback).
    openAllWatchPanels();
    setAllWatchStatuses(targetStateFailed);
    let dest = $(getWatchPanel(watchPanelKeyHost).section);
    for (const [key, panel] of Object.entries(watchPanels)) {
      const sec = $(panel.section);
      if (sec && panelVisible(sec) && (allWatches || []).some((w) => watchPanelKeyFor(w) === key && isWatchAttention(w))) {
        dest = sec;
        break;
      }
    }
    scrollToSection(dest);
    return;
  }
  const watchStatusTargets = {
    "starting-watches": targetStateStarting,
    "warning-watches": targetStateWarning,
    "stale-watches": targetStateStale,
  };
  if (watchStatusTargets[target]) {
    openAllWatchPanels();
    setAllWatchStatuses(watchStatusTargets[target]);
    scrollToSection($(getWatchPanel(watchPanelKeyHost).section));
    return;
  }
  const el = $("#" + target);
  if (!el) return;
  if (el.tagName === "DETAILS") el.open = true;
  scrollToSection(el);
}

function globalTargetValue(kind, name) {
  return `${kind}: ${name}`;
}

function globalTargetRecords() {
  const records = [];
  (allServices || []).forEach((item) => records.push({
    kind: globalTargetService, name: item.name, label: displayName(item), item,
    rowID: `svc-row-${item.name}`,
  }));
  (allWatches || []).forEach((item) => records.push({
    kind: globalTargetWatch, name: item.name, label: displayName(item), item,
    rowID: `wat-row-${item.name}`,
  }));
  (allApps || []).forEach((item) => records.push({
    kind: globalTargetApplication, name: item.name, label: displayName(item), item,
    rowID: `app-row-${item.name}`,
  }));
  (allLibraries || []).forEach((item) => records.push({
    kind: globalTargetLibrary, name: item.name, label: displayName(item), item,
    rowID: `library-row-${item.name}`,
  }));
  (allMounts || []).forEach((item) => records.push({
    kind: globalTargetMount, name: item.name, label: displayName(item), item,
    rowID: `mount-row-${detailDomKey(item.name || item.path || "mount")}`,
  }));
  records.forEach((record) => { record.value = globalTargetValue(record.kind, record.name); });
  return records.sort((a, b) => a.value.localeCompare(b.value));
}

function syncGlobalTargetSearch() {
  globalTargetSyncPending = false;
  const datalist = $("#target-search-options");
  if (!datalist) return;
  const records = globalTargetRecords();
  globalTargetsByValue = new Map(records.map((record) => [record.value, record]));
  const options = records.map((record) => {
    const option = new Option("", record.value);
    option.label = record.label === record.name ? record.kind : `${record.label} · ${record.kind}`;
    return option;
  });
  datalist.replaceChildren(...options);
}

function scheduleGlobalTargetSync() {
  if (globalTargetSyncPending) return;
  globalTargetSyncPending = true;
  queueMicrotask(syncGlobalTargetSearch);
}

function clearGlobalTargetFilters(target) {
  switch (target.kind) {
    case globalTargetService: {
      const surface = serviceSurfaceOf(target.item);
      const panel = splitServicePanelBySurface(surface);
      if (panel) {
        panel.query = "";
        panel.status = filterAll;
        panel.collapsedGroups.delete(categoryOf(target.item, defaultCategoryService));
      } else {
        svcQuery = "";
        svcStatus = filterAll;
        svcCategory = filterAll;
        svcCollapsedGroups.delete(categoryOf(target.item, defaultCategoryService));
      }
      renderServices();
      break;
    }
    case globalTargetWatch: {
      const panel = getWatchPanel(watchPanelKeyFor(target.item));
      panel.query = "";
      panel.status = filterAll;
      panel.type = filterAll;
      panel.collapsedGroups.delete(watchGroupOf(target.item));
      renderWatches();
      break;
    }
    case globalTargetApplication:
      appQuery = "";
      appStatus = filterAll;
      appCategory = filterAll;
      appCollapsedGroups.delete(categoryOf(target.item, defaultCategoryApp));
      renderApps();
      break;
    case globalTargetLibrary:
      libraryQuery = "";
      libraryStatus = filterAll;
      libraryCategory = filterAll;
      libraryCollapsedGroups.delete(categoryOf(target.item, defaultCategoryLibrary));
      renderLibraries();
      break;
    case globalTargetMount:
      mountQuery = "";
      mountStatus = filterAll;
      mountCategory = filterAll;
      mountCollapsedGroups.delete(categoryOf(target.item, defaultCategoryStorage));
      renderMounts();
      break;
  }
  applyUIStateToControls();
  saveUIState();
}

function focusGlobalTargetRow(rowID) {
  requestAnimationFrame(() => {
    const row = document.getElementById(rowID);
    if (!row) return;
    row.scrollIntoView({ block: "center", behavior: scrollBehaviorSmooth });
    const control = row.querySelector("button, [tabindex]") || row;
    if (typeof control.focus === "function") control.focus({ preventScroll: true });
  });
}

function openGlobalTarget(target) {
  if (!target) return;
  clearGlobalTargetFilters(target);
  hashScrolled = false;
  switch (target.kind) {
    case globalTargetService:
      history.replaceState(null, "", "#" + serviceExpansionKey(target.name));
      applyHash();
      break;
    case globalTargetWatch:
      history.replaceState(null, "", "#" + watchExpansionKey(target.name));
      applyHash();
      break;
    case globalTargetApplication:
      history.replaceState(null, "", "#" + appExpansionKey(target.name));
      applyHash();
      break;
    case globalTargetLibrary:
      history.replaceState(null, "", "#" + libraryExpansionKey(target.name));
      applyHash();
      break;
    case globalTargetMount: {
      const section = $("#mounts-section");
      if (section) section.open = true;
      history.replaceState(null, "", "#mounts-section");
      break;
    }
  }
  focusGlobalTargetRow(target.rowID);
}

function submitGlobalTargetSearch() {
  const input = $("#target-search");
  if (!input) return;
  const raw = input.value.trim();
  let target = globalTargetsByValue.get(raw);
  if (!target && raw) {
    const query = raw.toLowerCase();
    target = [...globalTargetsByValue.values()].find((record) =>
      record.value.toLowerCase().includes(query) || record.label.toLowerCase().includes(query));
  }
  if (!target) return;
  input.value = "";
  openGlobalTarget(target);
}

// themeHealthColor reads the active --ok/--warn/--crit tokens so the favicon and
// brand dot track light/dark scheme instead of hard-coded palette literals.
function themeHealthColor(status) {
  const root = getComputedStyle(document.documentElement);
  if (status === healthStatusCritical || status === healthStatusCriticalShort) return root.getPropertyValue("--crit").trim() || "#cf222e";
  if (status === healthStatusWarning || status === healthStatusWarningShort) return root.getPropertyValue("--warn").trim() || "#9a6700";
  if (status === targetStateStarting) return root.getPropertyValue("--text-2").trim() || "#8b96a5"; // neutral grey while the daemon settles
  return root.getPropertyValue("--ok").trim() || "#1a7f37";
}
// setFavicon reflects overall health in the browser tab: green = ok,
// warning = degraded health, critical = malfunctioning. Until healthIconReady,
// the neutral HTML/CSS placeholder stays — no optimistic green on first paint.
function setFavicon(status) {
  if (!healthIconReady) return;
  const color = themeHealthColor(status);
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="7" fill="${color}"/></svg>`;
  const link = $("#favicon");
  if (link) link.href = "data:image/svg+xml," + encodeURIComponent(svg);
  // The brand dot in the topbar mirrors the same overall health color.
  const dot = $("#brand-dot");
  if (dot) {
    dot.style.background = color;
    dot.style.boxShadow = `0 0 0 3px ${color}38`;
  }
}

// shortHostName keeps the first DNS label so FQDNs from older daemons still fit
// the tab title the same way config.ShortHostname does on the server.
function shortHostName(name) {
  if (!name) return "";
  const s = String(name).trim();
  if (!s) return "";
  return s.split(".")[0] || s;
}

// setDocumentTitle names the tab for multi-host operators:
//   Sermo - algieba
//   (3) Sermo - algieba
//   Sermo - algieba · starting
// Until healthIconReady, leave the bootstrap <title> alone.
function setDocumentTitle({ attentionCount = 0, starting = false } = {}) {
  if (!healthIconReady) return;
  const host = shortHostName(dashboardHostname);
  const base = host ? `Sermo - ${host}` : "Sermo";
  if (starting) {
    document.title = host ? `${base} · starting` : "Sermo · starting";
    return;
  }
  document.title = attentionCount > 0 ? `(${attentionCount}) ${base}` : base;
}

function rememberDashboardHostname(hostname) {
  const short = shortHostName(hostname);
  if (!short || short === dashboardHostname) return;
  dashboardHostname = short;
}
function browserNotifyEnabled() {
  try { return localStorage.getItem(BROWSER_NOTIFY_KEY) === storageBoolTrue; } catch (_) { return false; }
}

function setBrowserNotifyEnabled(enabled) {
  try { localStorage.setItem(BROWSER_NOTIFY_KEY, enabled ? storageBoolTrue : storageBoolFalse); } catch (_) {}
  syncNotifyToggle();
}

function syncNotifyToggle() {
  const btn = $("#notify-toggle");
  if (!btn) return;
  if (!(notificationAPIName in window)) {
    btn.hidden = true;
    return;
  }
  const on = browserNotifyEnabled() && Notification.permission === notifyPermissionGranted;
  btn.setAttribute("aria-pressed", on ? domBoolTrue : domBoolFalse);
}

const notificationAPIName = "Notification";
const notifyPermissionGranted = "granted";
const notifyPermissionDefault = "default";
const notifyPreviewNames = 4;
// notifiedAttention is the last cycle's failing-target set; null until the
// first render, which only sets the baseline (pre-existing failures are
// already on screen — announcing them on page load is noise).
let notifiedAttention = null;

// maybeNotifyAttention sends one browser notification (opt-in) for targets
// that newly started failing, and only while the tab is hidden — when visible
// the attention banner and favicon already carry the signal.
function maybeNotifyAttention(failingSvcs, firingWatches, failedApps) {
  if (!(notificationAPIName in window)) return;
  const current = new Map();
  failingSvcs.forEach((s) => current.set(expansionPrefixService + s.name, s.name));
  firingWatches.forEach((w) => current.set(expansionPrefixWatch + w.name, displayName(w) || w.name));
  failedApps.forEach((a) => current.set(expansionPrefixApp + a.name, displayName(a) || a.name));
  const previous = notifiedAttention;
  notifiedAttention = current;
  if (previous === null) return;
  if (!browserNotifyEnabled() || Notification.permission !== notifyPermissionGranted || !document.hidden) return;
  const fresh = [...current.keys()].filter((key) => !previous.has(key));
  if (!fresh.length) return;
  const names = fresh.map((key) => current.get(key));
  const title = fresh.length === 1 ? "Sermo: 1 target failing" : `Sermo: ${fresh.length} targets failing`;
  const body = names.slice(0, notifyPreviewNames).join(", ") + (names.length > notifyPreviewNames ? ` and ${names.length - notifyPreviewNames} more` : "");
  try {
    const note = new Notification(title, { body, tag: "sermo-attention" });
    note.addEventListener(domEventClick, () => {
      window.focus();
      note.close();
    });
  } catch (_) { /* notification constructors can throw in some embeds */ }
}

function renderAttention() {
  const box = $("#attention");
  if (!box) return;
  // Before the first complete snapshot the alert set is incomplete (watches and
  // apps still loading), so keep the banner empty and the tab icon neutral —
  // performLoad calls this again once the full snapshot lands.
  if (!firstSnapshotDone) {
    box.classList.add("live-hidden");
    setHTMLIfChanged(box, "");
    return;
  }
  const items = [];
  const failing = (allServices || []).filter(isServiceAttention);
  if (failing.length) {
    items.push({
      level: healthStatusCritical,
      title: failing.length === 1 ? "1 service needs attention" : `${failing.length} services need attention`,
      detail: failing.slice(0, 4).map((s) => s.name).join(", ") + (failing.length > 4 ? ` and ${failing.length - 4} more` : ""),
      target: "failed-services",
    });
  }
  const failingWatches = (allWatches || []).filter(isWatchAttention);
  if (failingWatches.length) {
    items.push({
      level: healthStatusCritical,
      title: failingWatches.length === 1 ? "1 watch firing" : `${failingWatches.length} watches firing`,
      detail: failingWatches.slice(0, 4).map((w) => displayName(w) || w.name).join(", ") + (failingWatches.length > 4 ? ` and ${failingWatches.length - 4} more` : ""),
      target: "failed-watches",
    });
  }
  const warningWatches = (allWatches || []).filter(isWatchWarning);
  if (warningWatches.length) {
    items.push({
      level: healthStatusWarning,
      title: warningWatches.length === 1 ? "1 watch warning" : `${warningWatches.length} watches warning`,
      detail: warningWatches.slice(0, 4).map((w) => displayName(w) || w.name).join(", ") + (warningWatches.length > 4 ? ` and ${warningWatches.length - 4} more` : ""),
      target: "warning-watches",
    });
  }
  const staleWatches = (allWatches || []).filter(isWatchSampleStale);
  if (staleWatches.length) {
    items.push({
      level: healthStatusWarning,
      title: staleWatches.length === 1 ? "1 watch sample is stale" : `${staleWatches.length} watch samples are stale`,
      detail: staleWatches.slice(0, 4).map((w) => displayName(w) || w.name).join(", ") + (staleWatches.length > 4 ? ` and ${staleWatches.length - 4} more` : ""),
      target: "stale-watches",
    });
  }
  const failingApps = (allApps || []).filter((a) => appStateText(a) === targetStateFailed);
  if (failingApps.length) {
    items.push({
      level: healthStatusCritical,
      title: failingApps.length === 1 ? "1 application failed" : `${failingApps.length} applications failed`,
      detail: failingApps.slice(0, 4).map((a) => displayName(a) || a.name).join(", ") + (failingApps.length > 4 ? ` and ${failingApps.length - 4} more` : ""),
      target: "failed-apps",
    });
  }
  maybeNotifyAttention(failing, failingWatches, failingApps);
  const activeLocks = (latestLocks || []).filter((l) => l.state === lockStateActive);
  if (activeLocks.length) {
    items.push({
      level: healthStatusCritical,
      title: activeLocks.length === 1 ? "1 active lock" : `${activeLocks.length} active locks`,
      detail: activeLocks.slice(0, 4).map((l) => [l.service, l.name].filter(Boolean).join(":")).join(", "),
      target: "locks-section",
    });
  }
  const staleLocks = (latestLocks || []).filter((l) => l.state === lockStateStale);
  if (staleLocks.length) {
    items.push({
      level: healthStatusWarning,
      title: staleLocks.length === 1 ? "1 stale lock" : `${staleLocks.length} stale locks`,
      detail: staleLocks.slice(0, 4).map((l) => [l.service, l.name].filter(Boolean).join(":")).join(", "),
      target: "locks-section",
    });
  }
  if (latestReady && latestReady.ready === false && latestReady.status === daemonStatusShuttingDown) {
    items.push({
      level: healthStatusWarning,
      title: "Daemon shutting down",
      detail: latestReady.message || latestReady.status || "",
      target: "daemon-section",
    });
  }
  // Recent errors are an advisory, not a critical signal: the rollup counts every
  // error event in the rolling activity window — including stale reload/config
  // failures and errors from now-unmonitored targets — so it must never drive the
  // overall status red. A currently-failing target turns the favicon
  // red through its own path (failed services/watches/apps, hook-failed/firing).
  if (latestActivity && (latestActivity.errors || 0) > 0) {
    items.push({
      level: healthStatusWarning,
      title: latestActivity.errors === 1 ? "1 recent error" : `${latestActivity.errors} recent errors`,
      detail: latestActivity.last_event_kind ? `last: ${latestActivity.last_event_kind}` : "see events",
      target: "events-section",
    });
  }
  // While the daemon is settling (starting), the tab favicon is neutral grey and
  // other health signals are premature, so it overrides the ok/warning/critical
  // colour. Startup progress lives in the status bar (`status: starting`).
  const startingNow = latestReady && latestReady.ready === false && latestReady.status === daemonStatusStarting;
  if (startingNow) {
    setFavicon(targetStateStarting);
    setDocumentTitle({ starting: true });
  } else if (!items.length) {
    setFavicon(healthStatusOK);
    setDocumentTitle({ attentionCount: 0 });
    box.classList.add("live-hidden");
    setHTMLIfChanged(box, "");
    return;
  } else {
    setFavicon(items.some((it) => it.level === healthStatusCritical) ? healthStatusCritical : healthStatusWarning);
    setDocumentTitle({ attentionCount: items.length });
  }
  box.classList.remove("live-hidden");
  const html = `
    <div class="attn-head">
      <b>Attention required</b>
      <span class="muted">${items.length} signal${items.length === 1 ? "" : "s"}</span>
    </div>
    <div class="attn-list">${items.map((it) => `
      <button class="attn-item ${esc(it.level)}" data-panel-target="${esc(it.target)}" aria-label="${esc(attnAriaLabel(it))}">
        <div class="attn-title ${it.level === healthStatusCritical ? "bad" : "inactive"}">${esc(it.title)}</div>
        ${it.detail ? `<div class="attn-detail">${esc(it.detail)}</div>` : ""}
      </button>
    `).join("")}</div>`;
  setHTMLIfChanged(box, html);
}
function isTrackedOperation(action) { return serviceTrackedActions.includes(action); }
function isMonitorToggle(action) { return action === actionMonitor || action === actionUnmonitor; }
// acquireMonitorToggle registers a monitor/unmonitor request as in flight.
// Returns the guard key, "" for non-toggle actions, or null when the same
// toggle is already pending (the caller must ignore the click).
function acquireMonitorToggle(prefix, name, action) {
  if (!isMonitorToggle(action)) return "";
  const key = prefix + name;
  if (pendingMonitorToggles.has(key)) return null;
  pendingMonitorToggles.add(key);
  return key;
}
// releaseMonitorToggleAfterRefresh triggers the post-action refresh and, for a
// monitor/unmonitor toggle, holds the pending guard until that refresh lands so
// an intermediate re-render cannot re-enable the button against stale state.
function releaseMonitorToggleAfterRefresh(toggleKey, render) {
  const refresh = load();
  if (!toggleKey) return;
  refresh.finally(() => {
    pendingMonitorToggles.delete(toggleKey);
    render();
  });
}
// serviceBusy reports an engine action in flight on a service: one this browser
// started (liveOps, which carries the action name and elapsed time) or one the
// daemon reports through operation_active — an action started from another tab,
// from sermoctl or by automatic remediation, and the operator's own action after
// a page reload dropped the liveOps entry.
function serviceBusy(s) {
  const op = liveOps.get(s && s.name);
  if (op) return !op.finished;
  return !!(s && s.operation_active);
}
function opElapsed(op) {
  const end = op.finished || Date.now();
  return Math.max(0, Math.floor((end - op.started) / millisecondsPerSecond));
}
function opStateText(op) {
  if (!op.finished) return operationStateRunning;
  return op.ok ? operationStateCompleted : targetStateFailed;
}
function beginOperation(name, action) {
  liveOps.set(name, {
    name,
    action,
    started: Date.now(),
    finished: 0,
    ok: false,
    message: "operation in progress",
  });
  ensureLiveOpsTimer();
  updateLiveOps();
}
function finishOperation(name, ok, message) {
  const op = liveOps.get(name) || { name, action: "operation", started: Date.now() };
  op.finished = Date.now();
  op.ok = !!ok;
  op.message = message || (ok ? operationStateCompleted : targetStateFailed);
  liveOps.set(name, op);
  renderOperationLive();
  renderServices();
  setTimeout(() => {
    if (liveOps.get(name) === op && op.finished) {
      liveOps.delete(name);
      renderOperationLive();
      renderServices();
      stopLiveOpsTimerIfIdle();
    }
  }, 8000);
}
function ensureLiveOpsTimer() {
  if (!liveOpsTimer) liveOpsTimer = setInterval(updateLiveOps, liveOpsTickMs);
}
function stopLiveOpsTimerIfIdle() {
  if (liveOpsTimer && liveOps.size === 0) {
    clearInterval(liveOpsTimer);
    liveOpsTimer = null;
  }
}
// updateLiveOps re-renders the browser-local in-flight operation cards so
// their elapsed times tick while an operation is active.
function updateLiveOps() {
  renderOperationLive();
  renderServices();
  if (liveOps.size === 0) stopLiveOpsTimerIfIdle();
}
function renderOperationLive() {
  const box = $("#op-live");
  if (!box) return;
  const ops = [...liveOps.values()].sort((a, b) => b.started - a.started);
  if (!ops.length) {
    box.classList.add("live-hidden");
    setHTMLIfChanged(box, "");
    return;
  }
  box.classList.remove("live-hidden");
  const html = ops.map((op) => {
    const state = opStateText(op);
    const cls = op.finished ? (op.ok ? targetStateOK : targetStateFailed) : "";
    const since = op.finished ? `${fmtDuration(opElapsed(op))} total` : `${fmtDuration(opElapsed(op))} elapsed`;
    return `<div class="op-card">
      <span class="op-dot ${cls}" aria-hidden="true"></span>
      <b>${esc(op.action)}</b>
      <span>${esc(op.name)}</span>
      <span class="${cls || 'inactive'}">${esc(state)}</span>
      <span class="muted">${esc(since)}</span>
      ${op.message ? `<span class="muted">${esc(op.message)}</span>` : ""}
    </div>`;
  }).join("");
  setHTMLIfChanged(box, html);
}

function serviceStatusMatches(s, status) {
  return serviceStatusFilterStates.includes(status) ? serviceDisplayState(s) === status : true;
}

function serviceQueryMatches(s, query, category) {
  if (!query) return true;
  const monitorText = s && s.monitored ? "monitoring enabled" : "monitoring paused";
  const missing = Array.isArray(s && s.observability_missing) ? s.observability_missing.join(" ") : "";
  const hay = `${displayName(s)} ${s.name || ""} ${s.display_name || ""} ${category} ${s.unit || ""} ${serviceDisplayState(s)} ${monitorText} ${missing}`.toLowerCase();
  return hay.includes(query);
}

function serviceMatches(s) {
  if (serviceSurfaceOf(s) !== serviceSurfaceRegular) return false;
  const category = categoryOf(s, defaultCategoryService);
  if (svcCategory !== filterAll && category !== svcCategory) return false;
  return serviceQueryMatches(s, svcQuery, category) && serviceStatusMatches(s, svcStatus);
}

function setSvcQuery(v) { svcQuery = (v || "").trim().toLowerCase(); renderServices(); saveUIState(); }
function setSvcCategory(v) { svcCategory = v || filterAll; renderServices(); saveUIState(); }

function getSplitServicePanel(panelKey) {
  return splitServicePanels[panelKey] || splitServicePanelBySurface(panelKey);
}

function splitServiceMatches(s, panel) {
  const category = categoryOf(s, defaultCategoryService);
  return serviceQueryMatches(s, panel.query, category) && serviceStatusMatches(s, panel.status);
}

function syncSplitServicePanelControls(panelKey) {
  const panel = getSplitServicePanel(panelKey);
  if (!panel) return;
  const search = $(panel.search);
  if (search) search.value = panel.query;
  syncFilterButtons(panel.filters, panel.filterDataset, panel.status);
}

function setSplitServiceQuery(panelKey, value) {
  const panel = getSplitServicePanel(panelKey);
  if (!panel) return;
  panel.query = (value || "").trim().toLowerCase();
  renderServices();
  saveUIState();
}

function setSplitServiceStatus(panelKey, value) {
  const panel = getSplitServicePanel(panelKey);
  if (!panel) return;
  panel.status = normalizeServiceStatusFilter(value);
  syncFilterButtons(panel.filters, panel.filterDataset, panel.status);
  renderServices();
  saveUIState();
}

function setSplitServiceSort(panelKey, key) {
  const panel = getSplitServicePanel(panelKey);
  if (!panel) return;
  toggleSort(panel.sort, key, renderServices);
}

function setSplitServiceGrouped(panelKey, grouped) {
  const panel = getSplitServicePanel(panelKey);
  if (!panel) return;
  panel.grouped = !!grouped;
  renderServices();
  saveUIState();
}

function toggleAllSplitServiceGroups(panelKey) {
  const panel = getSplitServicePanel(panelKey);
  if (!panel) return;
  const groups = sortedCategories(servicesForSurface(panel.surface).filter((s) => splitServiceMatches(s, panel)), "service");
  toggleAllGroups(groups, panel.collapsedGroups);
  renderServices();
  saveUIState();
}

function compareSortValues(a, b) {
  if (a == null) a = "";
  if (b == null) b = "";
  if (typeof a === "number" && typeof b === "number") {
    return a < b ? -1 : a > b ? 1 : 0;
  }
  if (typeof a === "boolean") a = a ? 1 : 0;
  if (typeof b === "boolean") b = b ? 1 : 0;
  if (typeof a === "number" && typeof b === "number") {
    return a < b ? -1 : a > b ? 1 : 0;
  }
  return String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: "base" });
}

function numericSortValue(v) {
  const n = Number(v);
  return Number.isFinite(n) ? n : 0;
}

function sortedBy(list, sort, sortKeys, fallbackKey) {
  const f = sortKeys[sort.key];
  if (!sort.key || !f) return list;
  const fallback = sortKeys[fallbackKey || "name"];
  list.sort((a, b) => {
    const primary = compareSortValues(f(a), f(b)) * sort.dir;
    if (primary !== 0) return primary;
    return fallback ? compareSortValues(fallback(a), fallback(b)) : 0;
  });
  return list;
}

function renderFilterButtonCounts(selector, counts) {
  document.querySelectorAll(`${selector} button`).forEach((b) => {
    const key = b.dataset.f || b.dataset.cf || b.dataset.vf || b.dataset.wf || b.dataset.af || b.dataset.mf || b.dataset.sf;
    if (counts[key] === undefined) return;
    b.innerHTML = `${key} <span class="muted">${counts[key]}</span>`;
    // Keep the filter row compact by hiding states with no matches — except
    // "all" and whichever filter is currently selected (so an emptied
    // selection stays visible and can be switched away from).
    b.hidden = counts[key] === 0 && key !== filterAll && !b.classList.contains("f-active");
  });
}

function stateCounts(items, stateOf, states) {
  const list = items || [];
  const counts = { [filterAll]: list.length };
  states.forEach((state) => { counts[state] = 0; });
  list.forEach((item) => {
    const state = stateOf(item);
    if (counts[state] !== undefined) counts[state]++;
  });
  return counts;
}

function normalizeServiceStatusFilter(v) {
  switch (v) {
    case targetStateRunning:
      return filterAll;
    case targetStatePaused:
      return targetStateStopped;
    default:
      return v || filterAll;
  }
}

function normalizeWatchStatusFilter(v) {
  return v || filterAll;
}

function normalizeMountStatusFilter(v) {
  return mountStatusFilterStates.includes(v) ? v : filterAll;
}

function syncFilterButtons(selector, datasetKey, activeValue) {
  document.querySelectorAll(`${selector} button`).forEach((b) => {
    const pressed = b.dataset[datasetKey] === activeValue;
    b.classList.toggle("f-active", pressed);
    b.setAttribute("aria-pressed", pressed ? domBoolTrue : domBoolFalse);
  });
}

// Column sort: null key keeps the default failing-first order; clicking a header
// sorts by it (ascending), and clicking the same header again flips direction.
const svcSortKeys = {
  name: (s) => displayName(s).toLowerCase(),
  category: (s) => categoryOf(s, defaultCategoryService).toLowerCase(),
  state: (s) => stateRank(serviceDisplayState(s)),
  uptime: (s) => numericSortValue(s && s.uptime_seconds),
  cpu: (s) => (s && s.cpu_ready) ? numericSortValue(s.cpu) : 0,
  memory: (s) => numericSortValue(s && s.rss),
  fds: (s) => numericSortValue(s && s.fds),
  io: (s) => numericSortValue(s && s.io_read) + numericSortValue(s && s.io_write),
  last: lastEventTime,
  strays: (s) => serviceStrayCount(s),
};
// toggleSort flips the direction when the same column is re-selected, otherwise
// selects the new column ascending, then re-renders. Shared by every sortable
// panel (sort is mutated in place; render is the panel's renderer).
function toggleSort(sort, key, render) {
  if (sort.key === key) sort.dir = -sort.dir;
  else { sort.key = key; sort.dir = 1; }
  render();
  saveUIState();
}

function setSvcSort(key) { toggleSort(svcSort, key, renderServices); }
// updateSortIndicatorsFor sets the ▲/▼ arrow on one panel's sortable headers:
// attr is the data-* key each header carries its sort key in, sort is that
// panel's {key, dir} state. Shared by the services/watches/apps panels.
function sortAriaValue(sort, key) {
  if (!key || key !== sort.key) return "none";
  return sort.dir > 0 ? "ascending" : "descending";
}

// updateSortIndicatorsFor is the one owner of the sort arrow (▲/▼) and
// aria-sort refresh. scope, when given, confines the indicator query to one
// panel's section so per-panel sorts (the watch panels) cannot repaint each
// other's arrows.
function updateSortIndicatorsFor(attr, sort, headerSelector, headerKey, scope = "") {
  document.querySelectorAll(`${scope} .sort-ind[data-${attr}]`.trim()).forEach((el) => {
    el.textContent = el.dataset[attr] === sort.key ? (sort.dir > 0 ? " ▲" : " ▼") : "";
  });
  if (!headerSelector || !headerKey) return;
  document.querySelectorAll(headerSelector).forEach((th) => {
    th.setAttribute("aria-sort", sortAriaValue(sort, th.dataset[headerKey] || ""));
  });
}

function updateSortIndicators() {
  updateSortIndicatorsFor("si", svcSort, "#services-section .services-table th.sortable[data-sort]", "sort");
  for (const panel of Object.values(splitServicePanels)) {
    updateSortIndicatorsFor(panel.sortIndicator, panel.sort, `${panel.section} .services-table th.sortable[data-${panel.sortAttr}]`, panel.sortDataset);
  }
}

function serviceStatusCounts(services) {
  return stateCounts(services, serviceDisplayState, serviceStatusFilterStates);
}

// renderFilterCounts annotates each status-filter button with how many services
// match it, for at-a-glance triage.
function renderFilterCounts(services) {
  renderFilterButtonCounts("#svc-filters", serviceStatusCounts(services));
}

function setSvcStatus(v) {
  svcStatus = normalizeServiceStatusFilter(v);
  syncFilterButtons("#svc-filters", "f", svcStatus);
  renderServices();
  saveUIState();
}

function setSvcGrouped(v) {
  svcGrouped = !!v;
  renderServices();
  saveUIState();
}

function toggleAllSvcGroups() {
  const list = (allServices || []).filter(serviceMatches);
  const categories = sortedCategories(list, defaultCategoryService);
  toggleAllGroups(categories, svcCollapsedGroups);
  renderServices();
  saveUIState();
}

// serviceLockNames returns the active named runtime locks the daemon reports for
// a service. While any of them is active it blocks every engine action on that
// service — the operation pipeline checks named locks right after taking the
// operation lock (docs/architecture.md) — so the dashboard must not offer an
// action the daemon is going to refuse. Monitoring toggles are not engine
// actions and stay available.
function serviceLockNames(s) {
  return (s && s.active_locks) || [];
}

function serviceLockedReason(s, action) {
  if (!isTrackedOperation(action)) return "";
  const names = serviceLockNames(s);
  if (!names.length) return "";
  return `blocked by active lock${names.length === 1 ? "" : "s"}: ${names.join(", ")}`;
}

function serviceRepairAvailable(s) {
  const st = (s.status || backendStatusUnknown).toLowerCase();
  return st === targetStateFailed || st === backendStatusInactive;
}

function serviceActionDisabled(s, action, busy) {
  const st = (s.status || backendStatusUnknown).toLowerCase();
  const paused = st === targetStatePaused;
  const stopped = st === backendStatusInactive || st === targetStateFailed;
  const locked = !!serviceLockedReason(s, action);
  switch (action) {
    case actionStart:
      return !!(busy || locked || st === backendStatusActive || paused);
    case actionRepair:
      return !!(busy || locked || !serviceRepairAvailable(s));
    case actionStop: return !!(busy || locked || stopped);
    case actionRestart: return !!(busy || locked);
    case actionResume: return !!(busy || locked || !paused);
    case actionReload: return !!(busy || locked || st !== backendStatusActive || !s.can_reload);
    case actionMonitor:
    case actionUnmonitor: return !!(busy || pendingMonitorToggles.has(serviceExpansionKey(s.name)));
    default: return false;
  }
}

function serviceActionDisabledReason(s, action, busy) {
  const st = (s.status || backendStatusUnknown).toLowerCase();
  if (busy) return "operation in progress";
  const locked = serviceLockedReason(s, action);
  if (locked) return locked;
  const paused = st === targetStatePaused;
  const stopped = st === backendStatusInactive || st === targetStateFailed;
  switch (action) {
    case actionStart:
      if (paused) return "service is paused";
      if (st === backendStatusActive) return "service is already running";
      return "";
    case actionRepair:
      return serviceRepairAvailable(s) ? "" : "repair is available only for a failed or inactive service";
    case actionStop: return stopped ? "service is already stopped" : "";
    case actionResume: return !paused ? "service is not paused" : "";
    case actionReload:
      if (!s.can_reload) return "service does not support reload";
      return st !== backendStatusActive ? "service is not running" : "";
    default: return "";
  }
}

function actionHintID(kind, name, action) {
  return `${kind}-${name}-${action}-hint`;
}

function actionHint(id, disabled, reason) {
  if (!disabled || !reason) return nothing;
  return tpl`<span id="${id}" class="visually-hidden">${reason}</span>`;
}

function actionDescribedBy(id, disabled, reason) {
  return disabled && reason ? id : nothing;
}

function servicePowerAction(s) {
  const st = (s.status || backendStatusUnknown).toLowerCase();
  // A failed or inactive unit normally needs a guarded restart. Repair remains
  // an explicit fallback for residual init/runtime state, never the default.
  if (serviceRepairAvailable(s)) return actionRestart;
  return st === backendStatusActive || st === targetStatePaused ? actionStop : actionStart;
}

function expandToggleAriaLabel(name, open, subject) {
  return `${open ? "Collapse" : "Expand"} ${subject} for ${name}`;
}

function groupToggleAriaLabel(category, count, collapsed) {
  return `${collapsed ? "Expand" : "Collapse"} ${category} group (${count} items)`;
}

// actionMeta is the one vocabulary of action buttons: the glyph and, per
// surface, the accessible label. A surface without an entry falls back to
// "<action> <surface> <name>", so a new action renders before it is named
// here — it just reads generically until it is.
const actionMeta = {
  [actionStart]: { glyph: "▶", service: (n) => `Start service ${n}` },
  [actionRepair]: { glyph: "⚒", service: (n) => `Repair residual service state ${n}` },
  [actionStop]: { glyph: "■", service: (n) => `Stop service ${n}` },
  [actionRestart]: { glyph: "↻", service: (n) => `Restart service ${n}` },
  [actionResume]: { glyph: "▶", service: (n) => `Resume service ${n}`, watch: (n) => `Resume RAID reconstruction for watch ${n}` },
  [actionReload]: { glyph: "⟳", service: (n) => `Reload service ${n}` },
  [actionMonitor]: { glyph: "◉", service: (n) => `Monitor service ${n}`, watch: (n) => `Monitor watch ${n}` },
  [actionUnmonitor]: { glyph: "⊘", service: (n) => `Unmonitor service ${n}`, watch: (n) => `Unmonitor watch ${n}` },
  [actionExpand]: { watch: (n) => `Expand storage for watch ${n}` },
  [actionProbe]: { watch: (n) => `Probe watch ${n}` },
  [actionPause]: { watch: (n) => `Pause RAID reconstruction for watch ${n}` },
  [actionReplicationStart]: { watch: (n) => `Start replication for watch ${n}` },
};

function actionAriaLabel(surface, target, action) {
  const name = displayName(target) || target.name || "";
  const label = (actionMeta[action] || {})[surface];
  return label ? label(name) : `${action} ${surface} ${name}`;
}

function svcActionAriaLabel(s, action) {
  return actionAriaLabel("service", s, action);
}

function serviceActionGlyph(action) {
  return (actionMeta[action] || {}).glyph || "";
}

// serviceCustomButtons renders the service's configured operator buttons:
// labeled admin commands defined in the service's buttons: block. They confirm
// like every state-changing action; the command itself stays server-side.
function serviceCustomButtons(s, busy) {
  return (s.buttons || []).map((b) => tpl`<button ?disabled=${!!busy}
    data-service="${s.name}" data-service-button="${b.name}"
    title="${b.label}" aria-label="${b.label} (${displayName(s)})">${b.label}</button>`);
}

async function pressServiceButton(name, button) {
  const s = (allServices || []).find((item) => item && item.name === name) || {};
  const meta = (s.buttons || []).find((b) => b && b.name === button) || {};
  const label = meta.label || button;
  if (!(await promptConfirm({
    title: `${label}?`,
    message: `Run the configured "${label}" command for ${displayName(s) || name} now?`,
    okLabel: label,
    danger: true,
  }))) return;
  setStatus("");
  try {
    const body = await jsonOrThrow(await fetch(serviceButtonAPI(name, button), targetPostOptions()));
    setStatus(body.message || `${label}: ok`, feedbackStatusOK);
  } catch (err) {
    setStatus(`${label}: ${err.message || err}`, feedbackStatusErr);
  }
  scheduleRefresh();
}

function serviceActionButton(s, action, busy, compact = false, title = "") {
  const label = svcActionAriaLabel(s, action);
  const glyph = compact ? serviceActionGlyph(action) : "";
  const disabled = serviceActionDisabled(s, action, busy);
  const reason = serviceActionDisabledReason(s, action, busy);
  const hintID = actionHintID("svc", s.name, action);
  return tpl`${actionHint(hintID, disabled, reason)}<button class="${compact ? "icon-btn" : ""}" ?disabled=${disabled} data-service="${s.name}" data-service-action="${action}" title="${title || (compact ? label : nothing)}" aria-label="${label}" aria-describedby="${actionDescribedBy(hintID, disabled, reason)}">${glyph ? tpl`<span aria-hidden="true">${glyph}</span>` : action}</button>`;
}

// serviceRowParts builds one service's main and optional expansion <tr> HTML.
// Shared by the full tbody rebuild and the large-fleet in-place patch path.
function serviceRowParts(s, opts = {}) {
  const state = serviceState(s);
  const category = categoryOf(s, defaultCategoryService);
  const label = displayName(s);
  const op = liveOps.get(s.name);
  const busy = serviceBusy(s);
  const showResume = !!opts.showResume;
  // With a liveOps entry the note names the action and its elapsed time. Without
  // one, operation_active still tells us the engine holds the lock, so the row
  // says so instead of looking idle while it churns.
  const busyText = op
    ? tpl`<div id="svc-${s.name}-busy" class="svc-busy ${op.finished ? (op.ok ? 'ok' : 'bad') : 'inactive'}" role="status" aria-live="polite">${op.action} ${opStateText(op)} · ${fmtDuration(opElapsed(op))}${op.message ? tpl` <span class="muted">${op.message}</span>` : nothing}</div>`
    : (s.operation_active
      ? tpl`<div id="svc-${s.name}-busy" class="svc-busy inactive" role="status" aria-live="polite">operation in progress</div>`
      : nothing);
  let actions;
  if (!s.enabled) {
    actions = tpl`<span class="muted">disabled in config</span>`;
  } else {
    const powerAction = servicePowerAction(s);
    const alsoApply = (s.also_apply || []).length;
    const powerTitle = alsoApply
      ? `${svcActionAriaLabel(s, powerAction)}; also applies to: ${s.also_apply.join(", ")}`
      : svcActionAriaLabel(s, powerAction);
    const overflowActions = [
      showResume ? serviceActionButton(s, actionResume, busy, true) : nothing,
      serviceActionButton(s, actionReload, busy, true),
      s.monitored
        ? serviceActionButton(s, actionUnmonitor, busy, true)
        : serviceActionButton(s, actionMonitor, busy, true),
      serviceRepairAvailable(s) ? serviceActionButton(s, actionRepair, busy, true) : nothing,
      ...serviceCustomButtons(s, busy),
    ];
    actions = me.can_act ? tpl`
        ${serviceActionButton(s, powerAction, busy, true, powerTitle)}
        ${powerAction !== actionRestart ? serviceActionButton(s, actionRestart, busy, true) : nothing}
        ${overflowActions}`
      : tpl`<span class="muted">read-only</span>`;
  }
  const key = serviceExpansionKey(s.name);
  const open = expanded.has(key);
  const chev = tpl`<span class="exp" aria-hidden="true">${open ? '▾' : '▸'}</span>`;
  const name = tpl`<button type="button" class="name row-toggle" data-service-expand="${s.name}" aria-expanded="${open}" aria-controls="${open ? "exp-" + key : nothing}" aria-label="${expandToggleAriaLabel(label, open, "service details")}">${label}</button>`;
  const pinned = svcPinned.has(s.name);
  const pin = tpl`<button type="button" class="icon-btn pin-btn${pinned ? " pinned" : ""}" data-service-pin="${s.name}" aria-pressed="${pinned ? domBoolTrue : domBoolFalse}" title="${pinned ? "Unpin from top" : "Pin to top"}" aria-label="${(pinned ? "Unpin " : "Pin ") + label + (pinned ? " from the top of the list" : " to the top of the list")}">${pinned ? "★" : "☆"}</button>`;
  const rowClass = state === targetStateFailed ? "row-failing" : (state === targetStateWarning ? "row-warning" : "");
  const main = tpl`<tr id="svc-row-${s.name}" class="clickable ${rowClass}" data-exp-key="${key}">
    <td><div class="svc-main">${chev}${name}${pin}</div>${busyText}</td>
    <td>${categoryBadge(category)}</td>
    <td>${serviceStateCell(s)}</td>
    <td>${serviceUptimeCell(s)}</td>
    <td>${serviceCpuCell(s)}</td>
    <td>${serviceMemCell(s)}</td>
    <td>${serviceFDsCell(s)}</td>
    <td>${serviceIoCell(s)}</td>
    <td>${lastEventCell(s)}</td>
    <td>${serviceStraysCell(s)}</td>
    <td class="actions">${actions}</td>
  </tr>`;
  const exp = open
    ? tpl`<tr class="exp-row" id="exp-${key}" data-exp="${key}"><td colspan="11"></td></tr>`
    : null;
  return { main, exp };
}

function serviceRowHTML(s, opts = {}) {
  const parts = serviceRowParts(s, opts);
  return parts.exp ? [parts.main, parts.exp] : [parts.main];
}

function finishSvcRender() {
  renderAttention();
  reassertExpansions();
}

// render receives fresh data on each refresh; cache it, then render through the
// active filter. Calls with no argument re-render the cache (filter changes).
function render(services) {
  if (services) allServices = services;
  scheduleGlobalTargetSync();
  renderServices();
  applyHash();
}

function servicePanelFilterActive(query, status, category) {
  return !!query || status !== filterAll || (category !== undefined && category !== filterAll);
}

function sortServiceList(list, sort) {
  if (sort.key && svcSortKeys[sort.key]) {
    sortedBy(list, sort, svcSortKeys, "name");
  } else {
    // Default: failing services first (stable sort keeps backend order in groups).
    list.sort((a, b) => (isFailing(b) ? 1 : 0) - (isFailing(a) ? 1 : 0));
  }
  // Pinned services lead regardless of the chosen order (stable sort keeps
  // the sorted order within the pinned and unpinned halves).
  if (svcPinned.size) {
    list.sort((a, b) => (svcPinned.has(b.name) ? 1 : 0) - (svcPinned.has(a.name) ? 1 : 0));
  }
}

function toggleServicePin(name) {
  if (!name) return;
  if (svcPinned.has(name)) svcPinned.delete(name);
  else svcPinned.add(name);
  saveUIState();
  renderServices();
}

function renderPrimaryServices() {
  const source = servicesForSurface(serviceSurfaceRegular);
  const total = source.length;
  const section = $("#services-section");
  const rows = $("#rows");
  const headCount = $("#services-count");
  if (!panelShell({ section, tbody: rows, count: headCount, filterCount: $("#svc-count"), total })) return;
  if (!rows) return;
  svcCategory = syncCategorySelect("#svc-category", source, defaultCategoryService, svcCategory);
  renderFilterCounts(source);
  const list = source.filter(serviceMatches);
  sortServiceList(list, svcSort);
  updateSortIndicators();
  const visibleCategories = sortedCategories(list, defaultCategoryService);
  svcCollapsedGroups.forEach((category) => { if (!visibleCategories.includes(category)) svcCollapsedGroups.delete(category); });
  if (visibleCategories.length < 2) svcGrouped = false;
  updateGroupButtons("svc", svcGrouped, visibleCategories, svcCollapsedGroups, "services");
  const cnt = $("#svc-count");
  if (cnt) cnt.textContent = servicePanelFilterActive(svcQuery, svcStatus, svcCategory) ? `showing ${list.length} of ${total}` : "";
  let content;
  if (!list.length) {
    content = source.length
      ? tpl`<tr><td colspan="11" class="muted">No services match the filter.</td></tr>`
      : tpl`<tr><td colspan="11" class="muted">No services.</td></tr>`;
  } else {
    content = svcGrouped
      ? renderGroupedRows(list, svcCollapsedGroups, "svc", "svc", (s) => categoryOf(s, defaultCategoryService), 11, (s) => serviceRowHTML(s), svcSort.key === "category" ? svcSort.dir : 1)
      : list.flatMap((s) => serviceRowHTML(s));
  }
  litRender(content, rows);
}

function renderSplitServicePanel(panelKey) {
  const panel = getSplitServicePanel(panelKey);
  if (!panel) return;
  const source = servicesForSurface(panel.surface);
  const total = source.length;
  const section = $(panel.section);
  const rows = $(panel.rows);
  const headCount = $(panel.count);
  const cnt = $(panel.filterCount);
  if (!panelShell({ section, tbody: rows, count: headCount, filterCount: cnt, total })) return;
  if (!rows) return;
  renderFilterButtonCounts(panel.filters, serviceStatusCounts(source));
  syncSplitServicePanelControls(panelKey);
  const list = source.filter((s) => splitServiceMatches(s, panel));
  sortServiceList(list, panel.sort);
  updateSortIndicatorsFor(panel.sortIndicator, panel.sort, `${panel.section} .services-table th.sortable[data-${panel.sortAttr}]`, panel.sortDataset);
  const groups = sortedCategories(list, defaultCategoryService);
  panel.collapsedGroups.forEach((group) => { if (!groups.includes(group)) panel.collapsedGroups.delete(group); });
  if (groups.length < 2) panel.grouped = false;
  updateGroupButtons(panelKey, panel.grouped, groups, panel.collapsedGroups, panel.label);
  if (cnt) cnt.textContent = servicePanelFilterActive(panel.query, panel.status) ? `showing ${list.length} of ${total}` : "";
  const filtered = servicePanelFilterActive(panel.query, panel.status);
  const content = list.length
    ? (panel.grouped
      ? renderGroupedRows(list, panel.collapsedGroups, panelKey, "svc", (s) => categoryOf(s, defaultCategoryService), 11, (s) => serviceRowHTML(s, { showResume: true }), panel.sort.key === "category" ? panel.sort.dir : 1)
      : list.flatMap((s) => serviceRowHTML(s, { showResume: true })))
    : tpl`<tr><td colspan="11" class="muted">${filtered ? panel.emptyFiltered : panel.empty}</td></tr>`;
  litRender(content, rows);
}

function renderServices() {
  preserveExpansionCells(isServiceExpansionKey);
  renderPrimaryServices();
  renderSplitServicePanel("container");
  renderSplitServicePanel("vm");
  finishSvcRender();
  updateSectionNav();
}

// renderExpansionTarget updates only the panel that owns an expansion. Keeping
// other lit-html roots untouched avoids invalidating a nested expansion while
// its own panel is rendering.
function renderExpansionTarget(key) {
  if (isServiceExpansionKey(key)) renderServices();
  else if (isWatchExpansionKey(key)) renderWatches();
  else if (isAppExpansionKey(key)) renderApps();
  else if (isLibraryExpansionKey(key)) renderLibraries();
}

// scheduleHashExpansion defers the render until the current lit-html render
// completes. applyHash can run while a panel is rendering after new data
// arrives; rendering that same panel recursively corrupts lit-html's parts.
function scheduleHashExpansion(key) {
  if (expanded.has(key)) return;
  expanded.add(key);
  queueMicrotask(() => {
    if (expanded.has(key)) renderExpansionTarget(key);
  });
}

// toggleExpand / loadExpansionFor drive inline expansion for services, host
// watches, applications and libraries.
function toggleExpand(key) {
  if (expanded.has(key)) {
    expanded.delete(key);
    expCache.delete(key);
    expDetailCache.delete(key);
    expCellCache.delete(key);
    if (location.hash === "#" + key) history.replaceState(null, "", location.pathname + location.search);
  } else {
    expanded.add(key);
    if (isShareableExpansionKey(key)) {
      history.replaceState(null, "", "#" + key); // shareable deep-link
    }
  }
  renderExpansionTarget(key);
  saveUIState();
}

function openServiceExpansion(name, scroll) {
  if (!name) return;
  const key = serviceExpansionKey(name);
  if (!expanded.has(key)) expanded.add(key);
  history.replaceState(null, "", "#" + key);
  openSectionForService(name);
  renderServices();
  if (scroll) {
    const el = document.getElementById("svc-row-" + name);
    if (el) el.scrollIntoView({ block: "center" });
  }
}

function toggleServiceExpansion(name) {
  if (!name) return;
  toggleExpand(serviceExpansionKey(name));
}

// preserveExpansionCells retains the live detail DOM (including SVG charts and
// event rows) before an outer lit-html table render removes its row. Reusing
// the original cell also preserves its nested lit-html render state.
function preserveExpansionCells(matches) {
  expanded.forEach((key) => {
    if (!matches(key)) return;
    const cell = expansionCell(key);
    if (cell) expCellCache.set(key, cell);
  });
}

// reassertExpansions restores open expansion cells after a
// structural table re-render (filter keystrokes, sorting, grouping, the 1s
// operations ticker), which can recreate rows and blank their detail cells.
// It performs no network requests: fresh data arrives once per dashboard poll
// via refreshExpandedServices / refreshExpandedWatches. Only a key with no
// cached content yet (an expansion that never loaded) falls back to a fetch.
function reassertExpansions() {
  expanded.forEach((k) => {
    if (!isServiceExpansionKey(k) && !isWatchExpansionKey(k)) return;
    const preserved = expCellCache.get(k);
    const cell = expansionCell(k);
    if (preserved && cell) {
      if (preserved !== cell) cell.replaceWith(preserved);
      expCellCache.delete(k);
      return;
    }
    if (!expCache.has(k)) {
      loadExpansionFor(k, dashboardGeneration);
      return;
    }
    if (cell) litRender(expCache.get(k), cell);
  });
}

// refreshExpandedServices reloads open service expansions once per dashboard
// refresh (called from load(), not from re-renders — see reassertExpansions).
// Every dashboard refresh fetches and fully renders the detail so checks,
// processes, rules and other non-chart fields cannot lag behind the row.
// Skipped while the tab is hidden unless opts.force is set.
async function refreshExpandedServices(opts = {}) {
  if (document.hidden && !opts.force) return true;
  if (opts.metricsOnly) {
    expanded.forEach((k) => {
      if (!isServiceExpansionKey(k)) return;
      const detail = expDetailCache.get(k);
      if (detail) hydrateServiceDetail(detail, opts.generation || dashboardGeneration);
    });
    return true;
  }
  const keys = [...expanded].filter(isServiceExpansionKey);
  const generation = opts.generation || dashboardGeneration;
  const results = await Promise.all(keys.map((key) => loadExpansionFor(key, generation)));
  return results.every(Boolean);
}

// refreshExpandedWatches refetches recent events once per dashboard refresh and
// re-renders every open watch expansion from that single response, instead of
// one events download per expanded watch per render.
async function refreshExpandedWatches(generation = dashboardGeneration) {
  if (document.hidden) return true;
  const keys = [...expanded].filter(isWatchExpansionKey);
  if (!keys.length) return true;
  try {
    const { stale, res } = await fetchPinned(apiEventsRecentPath, generation);
    if (stale) return false;
    if (!res.ok) return false;
    const page = await res.json();
    const events = eventPageEvents(page);
    keys.forEach((k) => renderWatchExpansionInto(k, events));
    return true;
  } catch (_) {
    return false; // keep the last content on a transient error
  }
}

// applyHash opens/scrolls to the target named in a #svc:|#wat:|#app: URL fragment
// or a section id such as #services-section.
// Runs after each render and on hashchange.
let hashScrolled = false;
function watchSectionFor(w) {
  return getWatchPanel(watchPanelKeyFor(w)).section;
}
function applyHash() {
  const h = decodeURIComponent(location.hash.slice(1));
  if (!h) return;
  const section = document.getElementById(h);
  if (section && (section.tagName === "DETAILS" || section.tagName === "SECTION")) {
    if (section.classList.contains("panel-hidden")) return;
    if (section.tagName === "DETAILS") section.open = true;
    if (!hashScrolled) {
      section.scrollIntoView({ block: scrollBlockStart });
      hashScrolled = true;
    }
    return;
  }
  if (isServiceExpansionKey(h)) {
    const name = expansionName(h, expansionPrefixService);
    if (!(allServices || []).some((s) => s.name === name)) return;
    openSectionForService(name);
    scheduleHashExpansion(h);
    if (!hashScrolled) {
      const el = document.getElementById("svc-row-" + name);
      if (el) el.scrollIntoView({ block: "center" });
      hashScrolled = true;
    }
    return;
  }
  if (isWatchExpansionKey(h)) {
    const name = expansionName(h, expansionPrefixWatch);
    const w = (allWatches || []).find((item) => item && item.name === name);
    if (!w) return;
    const sec = $(watchSectionFor(w));
    if (sec) { setPanelVisible(sec, true); sec.open = true; }
    scheduleHashExpansion(h);
    if (!hashScrolled) {
      const el = document.getElementById("wat-row-" + name);
      if (el) el.scrollIntoView({ block: "center" });
      hashScrolled = true;
    }
    return;
  }
  if (isAppExpansionKey(h)) {
    const name = expansionName(h, expansionPrefixApp);
    if (!(allApps || []).some((a) => a.name === name)) return;
    const sec = $("#apps-section");
    if (sec) { setPanelVisible(sec, true); sec.open = true; }
    scheduleHashExpansion(h);
    if (!hashScrolled) {
      const el = document.getElementById("app-row-" + name);
      if (el) el.scrollIntoView({ block: "center" });
      hashScrolled = true;
    }
    return;
  }
  if (isLibraryExpansionKey(h)) {
    const name = expansionName(h, expansionPrefixLibrary);
    if (!(allLibraries || []).some((library) => library.name === name)) return;
    const sec = $("#libraries-section");
    if (sec) { setPanelVisible(sec, true); sec.open = true; }
    scheduleHashExpansion(h);
    if (!hashScrolled) {
      const el = document.getElementById("library-row-" + name);
      if (el) el.scrollIntoView({ block: "center" });
      hashScrolled = true;
    }
  }
}
window.addEventListener(domEventHashChange, () => { hashScrolled = false; applyHash(); });

// rowClick expands a row from a click anywhere on it, except on interactive
// elements (action buttons and links) which keep their own behaviour.
function rowClick(event, key) {
  if (closestFrom(event, "button, a, input, select, summary")) return;
  toggleExpand(key);
}

// Expansion detail cells are rendered only through litRender into the cell
// found here (by loadExpansionFor, renderWatchExpansionInto and
// reassertExpansions): the row template leaves the <td> empty (no binding), so
// the outer #rows/watch render and these loaders never fight over the same cell.
function expansionCell(key) {
  const tr = [...document.querySelectorAll("tr.exp-row")].find((r) => r.dataset.exp === key);
  return tr ? tr.querySelector("td") : null;
}

// renderWatchExpansionInto renders one watch expansion cell from an
// already-fetched events response, shared by the first-open fetch and the
// per-poll refresh so expanded watches never each download their own copy.
function renderWatchExpansionInto(key, events) {
  const name = expansionName(key, expansionPrefixWatch);
  const html = renderWatchExpansion((allWatches || []).find((x) => x.name === name),
    (events || []).filter((e) => e.watch === name));
  expCache.set(key, html);
  const target = expansionCell(key);
  if (target) {
    litRender(html, target);
    const w = (allWatches || []).find((item) => item && item.name === name);
    loadSLAPanel(watchSLAKey(name));
    if (w) loadWatchMetrics(w);
  }
}

// sharedLoad deduplicates concurrent loads of one resource: while a load for
// this key is in flight every caller shares its promise, and the slot clears
// when it settles so the next refresh fetches fresh. One owner for the idiom
// the expansion, app-SLA and app-events loaders each hand-rolled.
function sharedLoad(inflight, loadingKey, start) {
  if (inflight.has(loadingKey)) return inflight.get(loadingKey);
  const pending = start().finally(() => inflight.delete(loadingKey));
  inflight.set(loadingKey, pending);
  return pending;
}

const expLoading = new Map(); // key -> shared in-flight detail fetch

function loadExpansionFor(key, generation = dashboardGeneration) {
  return sharedLoad(expLoading, `${generation}:${key}`, () => (async () => {
    const cell = expansionCell(key);
    if (cell && !expCache.has(key)) litRender(tpl`<span class="muted">loading…</span>`, cell);
    if (isServiceExpansionKey(key)) {
      const name = expansionName(key, expansionPrefixService);
      const { stale, res } = await fetchPinned(serviceAPI(name), generation);
      if (stale) return false;
      if (!res.ok) return false;
      const detailData = await res.json();
      expDetailCache.set(key, detailData);
      const html = renderServiceDetail(detailData);
      expCache.set(key, html);
      const target = expansionCell(key);
      if (target) litRender(html, target);
      return hydrateServiceDetail(detailData, generation);
    } else if (isWatchExpansionKey(key)) {
      const { stale, res } = await fetchPinned(apiEventsRecentPath, generation);
      if (stale) return false;
      const page = res.ok ? await res.json() : {};
      const events = eventPageEvents(page);
      renderWatchExpansionInto(key, events);
      return res.ok;
    }
    return true;
  })().catch(() => false));
}

// bucketize folds time-series points into cols buckets covering the last span
// ms of wall time: fold(bucket, point) accumulates each point into its bucket.
// Shared by the availability and latency charts.
function bucketize(points, span, cols, makeBucket, fold) {
  const startMs = Date.now() - span;
  const buckets = Array.from({ length: cols }, makeBucket);
  for (const p of points || []) {
    const t = Date.parse(p.start);
    if (isNaN(t)) continue;
    const i = Math.floor((t - startMs) / (span / cols));
    if (i >= 0 && i < cols) fold(buckets[i], p);
  }
  return { buckets, startMs };
}

// procCpuCell and procIoFdThreadCells render the per-process metric columns of
// the detail process table. Process CPU is already single-core normalized
// (100% = one core), so one cpuBarMini cell carries it the way every other CPU
// bar does: percentage inside the bar, precise value in the tooltip.
function procCpuCell(p) {
  if (!p.has_cpu) return tpl`<td>—</td>`;
  return tpl`<td>${cpuBarMini(Number(p.cpu) || 0)}</td>`;
}
// procMaxCoreCell renders the most any single core was used on this process's
// behalf: its busiest thread, against one core. It sits beside CPU because the two
// answer different questions — a process spread thinly over eight cores and one
// pegging a single core can report the same total.
//
// max_core_exact false means the daemon has not read this process's threads (it was
// below the sampling floor) and the value is the process's whole rate standing in as an
// upper bound. That distinction lives in the tooltip and not in the cell: below the
// floor a bound and a measurement are indistinguishable for any decision, and on an
// idle host every non-zero row is a bound — a marker present on every row of a column
// distinguishes nothing and only crowds the number.
function procMaxCoreCell(p) {
  // Gated on has_cpu alone, exactly like procCpuCell: both readings come from the
  // same live sample, so whenever one exists the other does. An absent max_core means
  // a measured zero (omitempty drops it), not an unknown — "—" would misreport it.
  if (!p.has_cpu) return tpl`<td>—</td>`;
  const peak = Number(p.max_core) || 0;
  const shown = fmtPct(peak);
  return tpl`<td>${cpuBarMini(peak, p.max_core_exact
    ? `${shown} of one core, this process's busiest thread`
    : `at most ${shown} of one core: not measured per thread, bounded by the process rate`)}</td>`;
}
function procIoFdThreadCells(p) {
  const io = (p.io_read || p.io_write) ? `${fmtBytes(p.io_read || 0)} / ${fmtBytes(p.io_write || 0)}` : '—';
  return tpl`<td>${io}</td><td class="muted">${p.fds ? fmtNum(p.fds, 0) : '—'}</td><td class="muted">${p.threads ? fmtNum(p.threads, 0) : '—'}</td>`;
}
function procCmd(p) {
  return (p.cmdline || []).join(" ").trim();
}
function procLabel(p) {
  const cmd = procCmd(p);
  if (p.exe_resolved && p.exe) {
    const label = cmd ? `${p.exe} ...` : p.exe;
    return tpl`<span class="truncate process-cmd" title="${cmd || p.exe}">${label}</span>`;
  }
  // A replaced binary resolves no exe, so without this the process reads as
  // "unknown" — hiding the one fact that explains it and says what to do.
  if (p.exe_previous) {
    const title = `${p.exe_previous} was replaced or removed on disk; this process still runs the previous version${cmd ? `\n${cmd}` : ""}`;
    return tpl`<span class="truncate process-cmd inactive" title="${title}">${p.exe_previous} (replaced on disk)</span>`;
  }
  if (cmd) {
    return tpl`<span class="truncate process-cmd" title="${cmd}">${cmd}</span>`;
  }
  if (p.exe) return tpl`<span class="truncate process-cmd inactive" title="${p.exe}">${p.exe}</span>`;
  return tpl`<span class="muted">unknown</span>`;
}

function processRows(procs) {
  const byPID = new Map();
  (procs || []).forEach((p) => byPID.set(Number(p.pid), { p, children: [] }));
  const roots = [];
  byPID.forEach((row, pid) => {
    const parent = byPID.get(Number(row.p.ppid));
    if (parent && Number(row.p.ppid) !== pid) parent.children.push(row);
    else roots.push(row);
  });
  roots.sort((a, b) => Number(a.p.pid || 0) - Number(b.p.pid || 0));
  const out = [];
  const seen = new Set();
  function visit(row, depth) {
    const pid = Number(row.p.pid);
    if (seen.has(pid)) return;
    seen.add(pid);
    out.push({ p: row.p, depth });
    row.children.forEach((child) => visit(child, depth + 1));
  }
  roots.forEach((row) => visit(row, 0));
  byPID.forEach((row) => visit(row, 0));
  return out;
}

function procRoleCell(p) {
  // A stray carries the backend seed's role "main"; showing that alone would say
  // this is the service's principal process, which is the opposite of the truth.
  if (!p.stray) return p.role || "";
  const title = "in the service's control group but claimed by no process selector, and not part of the principal process's tree; `sermoctl reap` lists it and, when the service declares reap.kill_only_if, signals it";
  return tpl`<span class="inactive" title="${title}">stray</span>`;
}

function procTreeLabel(row) {
  const depth = Number(row.depth || 0);
  const p = row.p || {};
  const branch = depth > 0
    ? tpl`<span class="proc-branch" title="child process of PID ${p.ppid || ""}" aria-label="child process of PID ${p.ppid || ""}"></span>`
    : nothing;
  return tpl`<span class="proc-tree${depth > 0 ? " proc-tree-child" : ""}" style="--proc-depth:${depth}">${branch}${procLabel(p)}</span>`;
}

function serviceUptimeCell(s) {
  const up = s ? fmtDuration(s.uptime_seconds) : "";
  if (!up) return tpl`<span class="muted">—</span>`;
  const title = s.started_at ? `started ${fmtTime(s.started_at)}` : nothing;
  return tpl`<span title="${title}">${up}</span>`;
}

function cpuInline(cpu, ready, numCPU, cpuThread) {
  if (!ready) return numCPU ? tpl`<span class="muted">measuring…</span>` : tpl`<span class="muted">—</span>`;
  const v = Number(cpu) || 0;
  // Same shape as every other CPU bar (cpuBarMini): the percentage lives inside
  // the bar and the precise value in the tooltip — no separate label prefix. The
  // tooltip also carries the daemon's cpu_thread reading (the busiest single
  // process against one core), which the whole-machine bar cannot show.
  return usageBarMini(pctClamp(v), fmtPct(v), `${fmtPct(v)} of ${numCPU || "?"} host CPUs${cpuThreadSuffix(cpuThread)}`);
}

// cpuThreadSuffix renders the cpu_thread reading as a tooltip clause, or "" when
// the daemon published no busiest-process rate for this sample.
function cpuThreadSuffix(cpuThread) {
  const peak = Number(cpuThread) || 0;
  if (peak <= 0) return "";
  return ` · busiest thread ${fmtPct(peak)} of one core`;
}

function serviceHasNoResidentProcess(s) {
  return !!(s && s.no_resident_process);
}

function serviceCpuCell(s) {
  if (serviceHasNoResidentProcess(s)) return tpl`<span class="muted">—</span>`;
  return cpuInline(s && s.cpu, !!(s && s.cpu_ready), s && s.num_cpu, s && s.cpu_thread);
}

function memoryInline(rss) {
  rss = Number(rss) || 0;
  if (!rss) return tpl`<span class="muted">—</span>`;
  const hostMem = hostMemTotalBytes();
  if (hostMem > 0) return usageBarMini(pctClamp(rss / hostMem * percentScale), fmtBytes(rss), `${fmtBytes(rss)} resident memory`);
  return tpl`<b>${fmtBytes(rss)}</b>`;
}

function serviceMemCell(s) {
  if (serviceHasNoResidentProcess(s)) return tpl`<span class="muted">—</span>`;
  return memoryInline(s && s.rss);
}

function serviceFDsCell(s) {
  if (serviceHasNoResidentProcess(s)) return tpl`<span class="muted">—</span>`;
  if (!(s && s.fds)) return tpl`<span class="muted">—</span>`;
  return tpl`<span title="open file descriptors">${fmtNum(s.fds, 0)}</span>`;
}

function ioRWInline(read, write) {
  read = Number(read) || 0;
  write = Number(write) || 0;
  if (!read && !write) return tpl`<span class="muted">—</span>`;
  return tpl`<span title="read / write">${fmtBytes(read)} / ${fmtBytes(write)}</span>`;
}

function serviceIoCell(s) {
  if (serviceHasNoResidentProcess(s)) return tpl`<span class="muted">—</span>`;
  return ioRWInline(s && s.io_read, s && s.io_write);
}

function serviceStrayCount(s) {
  return Number(s && s.strays) || 0;
}

const strayCountTitle = "processes in this service's control group that no configured selector claims; `sermoctl reap` lists them";

// serviceStraysCell shows the count and, for an admin, the button that clears it.
// A service with none reads as a dash like every other empty metric: the number is
// only interesting when it is not zero.
function serviceStraysCell(s) {
  const n = serviceStrayCount(s);
  if (!n) return tpl`<span class="muted">—</span>`;
  const count = tpl`<span class="bad" title="${strayCountTitle}">${fmtNum(n, 0)}</span>`;
  if (!me.can_act || !s.enabled) return count;
  return tpl`${count} ${serviceReapButton(s)}`;
}

function serviceReapButton(s) {
  const busy = serviceBusy(s);
  const pending = pendingReaps.has(s.name);
  const disabled = busy || pending;
  const label = `Reap the ${serviceStrayCount(s)} unaccounted-for process(es) of ${displayName(s)}`;
  // A disabled button must say why, through the same visually-hidden hint every
  // other service action uses, so the reason reaches a screen reader too.
  const reason = pending ? "reap in progress" : serviceActionDisabledReason(s, actionReap, busy);
  const hintID = actionHintID("svc", s.name, actionReap);
  return tpl`${actionHint(hintID, disabled, reason)}<button type="button" class="icon-btn danger-btn" data-service-reap="${s.name}" ?disabled=${disabled} title="${reason || label}" aria-label="${label}" aria-describedby="${actionDescribedBy(hintID, disabled, reason)}"><span aria-hidden="true">☠</span></button>`;
}

// slaDownPct is the share of a bucket's observed cycles that failed, in percent,
// or null when nothing was observed (a gap).
function slaDownPct(up, total) {
  const observed = Number(total) || 0;
  if (observed <= 0) return null;
  return (1 - (Number(up) || 0) / observed) * percentScale;
}

// slaDownBand maps that share onto one of five severity classes. Green is reserved
// for exactly zero, so no bucket containing a failure can read as healthy however
// little of the bucket it affected; the band then says how much of it was down,
// which is what separates a brief blip from a half-day outage.
//
// The class names carry the level and styles.css holds the colours (as the usagebar
// bands do), so the scale stays inside the token linter and a cell costs no
// getComputedStyle call to paint.
function slaDownBand(down) {
  if (down == null || down <= 0) return down == null ? "" : "sla-down-none";
  if (down <= slaDownBandLow) return "sla-down-low";
  if (down <= slaDownBandMid) return "sla-down-mid";
  if (down <= slaDownBandHigh) return "sla-down-high";
  return "sla-down-full";
}

// slaAffectedText names how much of a bucket was affected, so the colour is never
// the only carrier of that information (WCAG 2.2 1.4.1).
function slaAffectedText(down, downBuckets) {
  if (down == null) return "no data";
  if (down <= 0) return "no failures";
  const minutes = Number(downBuckets) || 0;
  const affected = minutes ? `, ${minutes} minute${minutes === 1 ? "" : "s"} affected` : "";
  return `${fmtPct(down)} down${affected}`;
}

// slaSegmentBounds is the wall-clock range of segment idx of n covering spanMs and
// ending at endMs.
function slaSegmentBounds(idx, n, spanMs, endMs) {
  const start = endMs - spanMs + (idx / n) * spanMs;
  const end = endMs - spanMs + ((idx + 1) / n) * spanMs;
  return `${fmtTime(start)} – ${fmtTime(end)}`;
}

function slaPointPct(p) {
  const total = Number(p && p.total || 0);
  if (total <= 0) return null;
  return pctClamp(Number(p.up || 0) / total * percentScale);
}

function slaPointTime(p) {
  const t = Date.parse(p && p.start);
  return isNaN(t) ? null : t;
}

function slaIncidentPoints(points, startMs, endMs) {
  return (points || []).map((p) => ({ p, t: slaPointTime(p), pct: slaPointPct(p) }))
    .filter((o) => o.t != null && o.t >= startMs && o.t <= endMs && o.pct != null && Number(o.p.up || 0) < Number(o.p.total || 0))
    .sort((a, b) => a.t - b.t);
}

function slaIncidentTime(t) {
  return fmtTime(new Date(t).toISOString());
}

// slaAffectedMinutes counts the one-minute buckets that saw a failure, summing over
// the incident points already selected for this window. It reads down_buckets rather
// than counting those points: past the per-minute retention one point covers hours
// or a whole day, so counting points would report three separate bad minutes inside
// one day as a single incident.
function slaAffectedMinutes(incidents) {
  return (incidents || []).reduce((n, o) => n + (Number(o.p.down_buckets) || 0), 0);
}

// slaTotals adds a series up into the window's observed availability: the
// figures the summary line and a check's SLA cell both state.
function slaTotals(points) {
  let up = 0, total = 0, affected = 0;
  (points || []).forEach((p) => {
    up += Number(p.up || 0);
    total += Number(p.total || 0);
    affected += Number(p.down_buckets) || 0;
  });
  return { up, total, affected };
}

function slaTimelineSummary(points) {
  const { up, total, affected } = slaTotals(points);
  if (total <= 0) return '<span class="muted">No data in this window.</span>';
  const pct = up / total * percentScale;
  const head = affected
    ? `<span class="bad">${affected} affected minute${affected === 1 ? "" : "s"}</span>`
    : '<span class="ok">No incidents</span>';
  return `${head} &middot; ${fmtPct(pct)}`;
}

function renderSLAIncidentList(incidents) {
  if (!incidents.length) return '<div class="sla-incident-list"><span class="ok">No incidents in this window.</span></div>';
  const shown = incidents.slice(-10);
  const hidden = incidents.length - shown.length;
  const chips = shown.map((o) => {
    const tip = `Incident ${fmtTime(new Date(o.t).toISOString())} · ${fmtPct(o.pct)} · ${Number(o.p.up || 0)}/${Number(o.p.total || 0)}`;
    return `<span class="sla-incident" title="${esc(tip)}">${esc(slaIncidentTime(o.t))}</span>`;
  }).join("");
  const more = hidden > 0 ? `<span class="muted">+${hidden} earlier</span>` : "";
  return `<div class="sla-incident-list"><span class="muted">Incidents</span>${chips}${more}</div>`;
}

// loadServiceWindowGraph runs the fetch protocol every service-detail graph
// shares, so its three staleness rules live in one place: a response from
// another daemon generation triggers a full reload; a response that arrives
// after the operator picked a different window is dropped instead of
// overwriting the fresh panel — on the error path too, which is easy to forget
// when the protocol is copied. url(win) builds the request, render(body, win)
// fills the panel and fail(error) paints the failure. Returns false only when
// the caller should treat its refresh as failed.
async function loadServiceWindowGraph(name, generation, url, render, fail) {
  const win = serviceMetricState(name).window;
  const windowChanged = () => serviceMetricState(name).window !== win;
  try {
    const { stale, res } = await fetchPinned(url(win), generation);
    if (stale) return false;
    if (!res.ok) throw new Error("HTTP " + res.status);
    const body = await res.json();
    if (windowChanged()) return true;
    render(body, win);
    return true;
  } catch (e) {
    if (windowChanged()) return true;
    fail(e);
    return false;
  }
}

// An availability panel is addressed by a key that names both its stored window
// and its DOM ids. A service uses its own name; a watch and an application take
// the prefixes their expansion keys already use, so a service, a watch and an
// application that happen to share a name never share a panel.
function watchSLAKey(name) { return expansionKey(expansionPrefixWatch, name); }
function appSLAKey(name) { return expansionKey(expansionPrefixApp, name); }

// Band panel keys. A state band is one metric of one target, so its key carries
// both; the metric is split off metric-last (lastIndexOf) because watch names
// may themselves contain ":" (service-scoped watches), while band metric keys
// never do. Prefixes deliberately distinct from every expansion prefix.
const bandKeyPrefixWatch = "watband:";
const bandKeyPrefixService = "svcband:";
function watchBandKey(name, metric) { return `${bandKeyPrefixWatch}${name}:${metric}`; }
function svcBandKey(service, check, metric) { return `${bandKeyPrefixService}${service}:${check}:${metric}`; }
function splitBandKey(rest) {
  const cut = rest.lastIndexOf(":");
  return [rest.slice(0, cut), rest.slice(cut + 1)];
}

// slaPanelAPI builds one panel's request from its key. A watch reads its own
// series; an application has none of its own and reads the series of the service
// it maps to, which is where its availability comes from in the first place.
function slaPanelAPI(key, win) {
  if (isWatchExpansionKey(key)) return watchSLAAPI(expansionName(key, expansionPrefixWatch), win);
  if (isAppExpansionKey(key)) return serviceSLAAPI(expansionName(key, expansionPrefixApp), win);
  if (key.startsWith(bandKeyPrefixWatch)) {
    const [name, metric] = splitBandKey(key.slice(bandKeyPrefixWatch.length));
    return watchSLAAPI(name, win, metric);
  }
  if (key.startsWith(bandKeyPrefixService)) {
    const [target, metric] = splitBandKey(key.slice(bandKeyPrefixService.length));
    const cut = target.indexOf(":");
    return serviceSLAAPI(target.slice(0, cut), win, target.slice(cut + 1), metric);
  }
  return serviceSLAAPI(key, win);
}

// slaChartPanel is the SLA timeline panel itself — one markup, whether it stands
// alone in a watch or application expansion, sits beside the other graphs of a
// service detail, or carries a state band. Availability and state must read the
// same wherever they are shown, and a second rendering of the same figure would
// be one more thing to keep in step. opts.title renames the head for a band
// panel; opts.tag stamps data-band-metric for selectors and tests.
function slaChartPanel(key, opts = {}) {
  return tpl`<div class="metric-panel metric-panel-wide" data-band-metric="${opts.tag || nothing}">
    <div class="sla-chart-head">
      <span class="metric-title">${opts.title || "SLA timeline"}</span>
      <span id="${detailDomId(key, "sla-summary")}" class="muted">loading...</span>
    </div>
    <div class="sla-panel">
      <div class="sla-chart-panel">
        <div id="${detailDomId(key, "sla-chart")}" class="muted chart-box-wide"></div>
      </div>
    </div>
  </div>`;
}

// renderSLASection is the whole Availability section for a surface whose only
// graph is this one: heading, window selector and panel. The service detail does
// not use it — its selector moves every graph it shows at once, so it places
// slaChartPanel in its own grid under the Graphs heading instead.
// watchMetricDomID names one watch metric panel's summary or chart. It is
// namespaced by the watch prefix like every other per-watch DOM id, so a watch
// and a service that publish the same metric never collide.
// hostCountHolders maps a host-wide count watch to the per-service field that
// composes it. The host number says how many; only this says who — and both come
// from data the dashboard already has, so naming the holders costs no request.
const hostCountHolders = {
  fds: { field: "fds", noun: "descriptors" },
  pids: { field: "threads", noun: "threads" },
};

// holdersTopCount is how many services a holder list names before folding the
// rest into a remainder. A leak is almost always one service, so the top few
// answer the question and a longer list would bury the answer it gives.
const holdersTopCount = 5;

// renderCountHoldersSection names the services that make up a host-wide count.
// A host with 879072 descriptors tells an operator nothing to act on; the same
// host with "libvirtd 872172 (99%)" names the thing to look at. The figures are
// the ones the service table already shows in its own column, and each name opens
// that service, so this adds an answer rather than another reading of the count.
function renderCountHoldersSection(w) {
  const holder = hostCountHolders[w && w.check_type];
  if (!holder) return nothing;
  const held = (allServices || [])
    .map((s) => ({ name: s.name, n: Number(s[holder.field]) || 0 }))
    .filter((s) => s.n > 0)
    .sort((a, b) => b.n - a.n);
  if (!held.length) return nothing;
  const total = held.reduce((sum, s) => sum + s.n, 0);
  const shown = held.slice(0, holdersTopCount);
  const rest = held.length - shown.length;
  const rows = shown.map((s) => tpl`<span class="count-holder">
    <button type="button" class="name row-toggle" data-service-open="${s.name}" aria-label="Open service ${s.name}">${s.name}</button>
    <b>${fmtNum(s.n, 0)}</b> <span class="muted">${fmtPct(s.n / total * percentScale)}</span>
  </span>`);
  const more = rest > 0 ? tpl`<span class="muted">+${rest} more</span>` : nothing;
  return tpl`<h3 class="expansion-heading">Held by</h3>
    <div class="sla-incident-list">${rows}${more}</div>
    <div class="muted">${fmtNum(total, 0)} ${holder.noun} attributed to monitored services.</div>`;
}

// watchMetricsWindowKey namespaces the window of a watch's Graphs section, apart
// from the one its Availability section keeps: the two read different series, and
// an operator comparing a threshold against uptime moves one without the other.
//
// Its prefix is deliberately not the "wat:" expansion one. slaPanelAPI decodes a
// panel key by that prefix, so a key beginning with it would decode to a watch
// named "graphs:<name>" if this ever reached an availability path.
function watchMetricsWindowKey(name) { return `watgraphs:${name}`; }

function watchMetricDomID(watch, metric, suffix) {
  return detailDomId(watchSLAKey(watch), `metric-${detailDomKey(metric)}-${suffix}`);
}

// renderWatchMetricsSection draws a watch's numeric readings as graphs, with the
// panel, selector and chart a service check's metric gets. The two differ only in
// the series they request: a watch has one check, so ?metric= alone names it.
// A state band renders through the availability panel instead of a line chart —
// a line through a boolean draws slopes that never happened — on the same Graphs
// window selector.
function renderWatchMetricsSection(w) {
  const list = (w && w.metrics) || [];
  if (!list.length) return nothing;
  const panels = list.map((metric) => metric.band
    ? slaChartPanel(watchBandKey(w.name, metric.name), { title: metric.label || watchMetricLabel(metric), tag: metric.name })
    : tpl`<div class="metric-panel" data-watch-metric="${metric.name}">
    ${metricPanelBody(watchMetricDomID(w.name, metric.name, "summary"), watchMetricDomID(w.name, metric.name, "chart"),
      watchMetricLabel(metric))}
  </div>`);
  const win = serviceMetricState(watchMetricsWindowKey(w.name)).window;
  return tpl`<h2>Graphs <span class="muted">${winButtons(metricWins, win, "setWatchMetricWin", "Graph time window", w.name)}</span></h2>
    <div class="metric-grid">${panels}</div>`;
}

// watchMetricLabel titles a watch metric panel. The check publishes the field
// name; the unit rides in the chart and the summary, so the title stays the name.
function watchMetricLabel(metric) {
  return String(metric.name || "").replace(/_/g, " ");
}

// loadWatchMetrics fills every panel of a watch's Graphs section through the one
// panel loader, exactly as a service detail fills its check metrics.
function loadWatchMetrics(w, generation = dashboardGeneration) {
  const list = (w && w.metrics) || [];
  if (!list.length) return Promise.resolve(true);
  const key = watchMetricsWindowKey(w.name);
  return Promise.all(list.map((metric) => metric.band
    ? loadSLAPanel(watchBandKey(w.name, metric.name), generation,
      { windowKey: key, warn: metric.severity === targetStateWarning })
    : loadMetricPanel(key,
      watchMetricDomID(w.name, metric.name, "summary"), watchMetricDomID(w.name, metric.name, "chart"),
      generation,
      (win) => watchMetricsAPI(w.name, metric.name, win),
      (body, win) => metricPanelContent(body, metric.unit, win, `${watchMetricLabel(metric)} chart`),
      watchMetricLabel(metric)))).then((results) => results.every(Boolean));
}

// setWatchMetricWin moves a watch's graph window. Its availability section keeps
// its own selector: the two read different series and an operator comparing a
// threshold against uptime wants to move one without moving the other.
function setWatchMetricWin(win, watch) {
  if (!watch) return;
  applyWindowChoice(watchMetricsWindowKey(watch), "setWatchMetricWin", watch, win, () => {
    const w = (allWatches || []).find((item) => item && item.name === watch);
    if (w) loadWatchMetrics(w);
  });
}

function renderSLASection(key) {
  const win = serviceMetricState(key).window;
  return tpl`<h2>Availability <span class="muted">${winButtons(metricWins, win, "setSLAWin", "Availability time window", key)}</span></h2>
    <div class="metric-grid">${slaChartPanel(key)}</div>`;
}

// loadSLAPanel fills one availability or state-band panel through the fetch
// protocol every windowed graph shares. The surfaces differ only in the request
// slaPanelAPI builds, so their panels cannot drift apart in what they draw.
// opts.windowKey lets a band panel follow another selector's window (a watch's
// Graphs selector moves its bands too); opts.warn caps the failing colour at
// amber for warning-severity bands.
function loadSLAPanel(key, generation = dashboardGeneration, opts = {}) {
  return loadMetricPanel(opts.windowKey || key, detailDomId(key, "sla-summary"), detailDomId(key, "sla-chart"), generation,
    (win) => slaPanelAPI(key, win),
    (body, win) => {
      const points = body.points || [];
      return { summary: slaTimelineSummary(points), chart: drawSLAChart(points, win, opts) };
    },
    "SLA");
}

// setSLAWin switches the window of a standalone Availability section. A service
// detail's selector is setMetricWin: it moves the whole detail, not this panel.
function setSLAWin(win, key) {
  if (!key) return;
  applyWindowChoice(key, "setSLAWin", key, win, () => loadSLAPanel(key));
}

// loadCheckSLA fills one check's SLA cell from the same endpoint and window
// selector the service panel uses, scoped with ?check=.
function loadCheckSLA(service, check, generation = dashboardGeneration) {
  const cell = document.getElementById(checkSLADomId(service, check));
  if (!cell) return Promise.resolve(true);
  return loadServiceWindowGraph(service, generation,
    (win) => serviceSLAAPI(service, win, check),
    (body, win) => { cell.innerHTML = drawCheckSLAStrip(body.points || [], win); },
    (e) => { cell.innerHTML = `<span class="bad">Failed: ${esc(e.message)}</span>`; });
}

// slaStrip builds the status-page availability band shared by the service SLA
// panel and every check's SLA cell: one bar per sub-span (oldest left), coloured
// by availability, hatched before the first observation. Returning null means
// the window holds no sample at all (slaNoWindowData).
//
// Both scopes render through here so a check can never present a window as a
// flat availability figure while the service shows the same window's gaps.
function slaStrip(points, win, opts = {}) {
  const cols = slaBarCount;
  const span = windowMs[win || defaultMetricWindow] || millisecondsPerDay;
  const endMs = Date.now();
  const startMs = endMs - span;
  const observed = (points || []).map((p) => ({ p, t: slaPointTime(p), pct: slaPointPct(p) }))
    .filter((o) => o.t != null && o.t >= startMs && o.t <= endMs && o.pct != null)
    .sort((a, b) => a.t - b.t);
  if (!observed.length) return null;

  const { buckets } = bucketize(points, span, cols,
    () => ({ up: 0, total: 0, downBuckets: 0 }),
    (b, p) => {
      b.up += Number(p.up || 0);
      b.total += Number(p.total || 0);
      b.downBuckets += Number(p.down_buckets || 0);
    });
  // The strip has a fixed bar count (slaBarCount) while the series is sampled
  // per cycle, so short windows have more bars than samples and every few bars
  // land on no sample — a continuously-up service looked striped. Carry the last
  // observed availability forward across those empty bars (the service held that
  // state between samples). Only bars before the first-ever sample stay hatched.
  let lastCell = null;
  const bars = buckets.map((b, i) => {
    const when = slaSegmentBounds(i, cols, span, endMs);
    let cell = null;
    let held = false;
    if (b.total) {
      cell = { pct: pctClamp(b.up / b.total * percentScale), down: slaDownPct(b.up, b.total), downBuckets: b.downBuckets };
      lastCell = cell;
    } else if (lastCell != null) { cell = lastCell; held = true; }
    if (cell == null) return `<span class="sla-bar-seg sla-gap" title="${esc(when + " · " + slaNoData)}" aria-label="${esc(when)}: ${slaNoData}"></span>`;
    const pctText = fmtPct(cell.pct);
    const affected = slaAffectedText(cell.down, cell.downBuckets);
    const tip = held
      ? `${when} · ${pctText} · held (no sample this sub-span)`
      : `${when} · ${pctText} · ${b.up}/${b.total} · ${affected}`;
    // A warning-severity state band caps its failing colour at amber
    // (sla-down-low is the warn token): a RAID array rebuilding is a thing to
    // watch, not an outage, and red is reserved for the bands that mean one.
    const band = opts.warn && cell.down > 0 ? "sla-down-low" : slaDownBand(cell.down);
    return `<span class="sla-bar-seg ${band}" title="${esc(tip)}" aria-label="${esc(when)}: ${esc(pctText)} available, ${esc(affected)}${held ? " (held)" : ""}"></span>`;
  }).join("");
  const incidents = slaIncidentPoints(points, startMs, endMs);
  const affectedMinutes = slaAffectedMinutes(incidents);
  const latestPct = observed[observed.length - 1].pct;
  const slaAria = `SLA timeline: latest ${fmtPct(latestPct)}, ${affectedMinutes} affected minute${affectedMinutes === 1 ? "" : "s"}`;
  return {
    observed, incidents, startMs,
    band: `<div class="sla-bars" role="img" aria-label="${esc(slaAria)}">${bars}</div>`,
  };
}

// drawSLAChart renders the service detail SLA panel: the shared band plus its
// axis, screen-reader table and incident list.
function drawSLAChart(points, win, opts = {}) {
  const strip = slaStrip(points, win, opts);
  if (strip == null) return slaNoWindowData;
  return `${slaChartDataTable(strip.observed)}${strip.band}` +
    `<div class="sla-bars-axis"><span>${esc(fmtTime(strip.startMs))}</span><span>now</span></div>` +
    renderSLAIncidentList(strip.incidents);
}

// drawCheckSLAStrip renders one check's SLA cell: the same band as the service,
// with the window's availability beside it. A cell is too small for the axis and
// incident list, so it carries them in the band's tooltips only.
function drawCheckSLAStrip(points, win) {
  const strip = slaStrip(points, win);
  if (strip == null) return slaNoWindowData;
  const { up, total } = slaTotals(points);
  const pctText = total > 0 ? fmtPct(up / total * percentScale) : "—";
  return `${strip.band}<div class="sla-cell-foot"><span class="sla-pct">${pctText}</span>` +
    `<span class="sla-count">${up}/${total}</span></div>`;
}

// totalsCpuCell renders the whole-tree machine-wide CPU rate for the General data
// grid, the same reading the table's CPU column shows.
//
// The busiest-thread figure is deliberately not spelled out here: it belongs to a
// process, and the process table now carries it per row (procMaxCoreCell). An
// aggregate maximum only restates whichever row is highest while hiding which one
// that is. cpu_thread still rides along so cpuInline can name it in the tooltip.
function totalsCpuCell(pt) {
  const { cpu, has_cpu: ready, num_cpu: cores, cpu_thread: thread } = pt || {};
  return cpuInline(cpu, !!ready, cores, thread);
}

function detailDomKey(name) {
  return Array.from(String(name || "")).map((ch) => ch.charCodeAt(0).toString(16).padStart(4, "0")).join("");
}

function detailDomId(name, suffix) {
  return "svc-detail-" + detailDomKey(name) + "-" + suffix;
}

function checkSLADomId(service, check) {
  return detailDomId(service, `sla-${detailDomKey(check)}`);
}

function serviceMeasuredChecks(d) {
  return (d.checks || []).filter((c) => metricTypes.includes(c.type));
}

// serviceSLAChecks are the checks whose SLA cell is loaded on demand: every one
// that records availability at all.
function serviceSLAChecks(d) {
  return (d.checks || []).filter((c) => !verdictlessCheck(c));
}

function serviceCheckMetrics(d) {
  return (d.checks || []).flatMap((check) => (check.metrics || []).map((metric) => ({
    check: check.name,
    name: metric.name,
    unit: metric.unit,
    band: !!metric.band,
    severity: metric.severity || "",
    label: metric.label || "",
  })));
}

function serviceCheckMetricDomID(service, check, metric, suffix) {
  return detailDomId(service, `metric-${detailDomKey(check + ":" + metric)}-${suffix}`);
}

function serviceLatencyDomID(service, check, suffix) {
  return detailDomId(service, `lat-${detailDomKey(check)}-${suffix}`);
}

function serviceCheckMetricLabel(metric) {
  return `${metric.check} · ${metric.name}`;
}

function serviceMetricState(name) {
  let state = serviceMetricStates.get(name);
  if (!state) {
    state = { window: defaultMetricWindow };
    serviceMetricStates.set(name, state);
  }
  return state;
}

// Reporting modes a check can declare (`reports:` in its configuration). Mirrors
// the Reports* constants in internal/checks.
const REPORTS_STATE = "state";
const REPORTS_VALUE = "value";

// verdictlessCheck reports whether a check passes no judgement, so it has no
// availability: the SLA column reads "n/a" rather than "no data yet", which
// would suggest a series that is merely still filling.
function verdictlessCheck(c) {
  return c.reports === REPORTS_STATE || c.reports === REPORTS_VALUE;
}

// checkStateHTML renders a check's state cell. A `reports: state` check is a
// state sensor with no verdict — "a backup is running" is neither healthy nor
// failing — so it reads active/inactive instead of ok/fail, in its own colours:
// informative for active, muted for inactive. Neither reuses the ok/fail green
// and red, which would imply a judgement, nor the warn-coloured `inactive`
// class used for stale.
function checkStateHTML(c, age) {
  if (c.stale) return tpl`<span class="inactive">stale</span>${age}`;
  if (!c.ran) return c.at ? tpl`<span class="muted">cached</span>${age}` : tpl`<span class="muted">not run yet</span>`;
  if (c.skipped) return tpl`<span class="muted">skipped</span>${age}`;
  if (c.reports === REPORTS_STATE) {
    return c.ok
      ? tpl`<span class="state-on">active</span>${age}`
      : tpl`<span class="state-off">inactive</span>${age}`;
  }
  if (c.reports === REPORTS_VALUE) return tpl`<span class="state-on">measured</span>${age}`;
  return c.ok ? tpl`<span class="ok">ok</span>${age}` : tpl`<span class="bad">fail</span>${age}`;
}

// checkSLAHTML renders the SLA cell, or an explicit n/a for a check that will
// never have one. The strip itself arrives from loadCheckSLA, on the window the
// detail's selector is on.
function checkSLAHTML(service, c) {
  if (verdictlessCheck(c)) return tpl`<span class="muted" title="This check reports no availability">n/a</span>`;
  return tpl`<div id="${checkSLADomId(service, c.name)}" class="muted sla-check-strip">loading…</div>`;
}

function sshSessionCloseButton(service, session) {
  if (!me.can_act) return tpl`<span class="muted">read-only</span>`;
  if (!session.can_close) return tpl`<span class="muted">unavailable</span>`;
  return tpl`<button class="danger-btn" data-ssh-session-close="1" data-ssh-service="${service}" data-ssh-session-pid="${session.pid}" data-ssh-session-start-ticks="${session.start_ticks}" data-ssh-session-terminal="${session.terminal}" data-ssh-session-user="${session.user || ""}" data-ssh-session-managed="${!!session.managed_by_logind}">close</button>`;
}

function terminalSessionCloseButton(session) {
  if (!me.can_act) return tpl`<span class="muted">read-only</span>`;
  if (!session.can_close) return tpl`<span class="muted">unavailable</span>`;
  return tpl`<button class="danger-btn" data-terminal-session-close="1"
    data-terminal-service="${session.service || ""}" data-terminal-check="${session.check || ""}"
    data-terminal-multiplexer="${session.multiplexer || ""}" data-terminal-session="${session.name || ""}"
    data-terminal-user="${session.user || ""}" data-terminal-identity="${session.identity || ""}">close</button>`;
}

function sessionStateCell(state) {
  const [badgeState, label] = sessionStateBadges[state] || [targetStateWarning, state || sessionStateUnknown];
  return stateBadgeLabel(badgeState, label);
}

function emptySessionCloseButton(source) {
  if (source.state !== sessionSourceAvailable || !source.can_close_empty) return nothing;
  if (!me.can_act) return tpl`<span class="muted">read-only</span>`;
  return tpl`<button class="danger-btn" data-empty-session-close="1"
    data-empty-session-service="${source.service || ""}"
    data-empty-session-check="${source.check || ""}">close</button>`;
}

function sourceHasSession(source, inventory) {
  if (source.kind === sessionKindSSH) {
    return (inventory.ssh || []).some((session) => session.service === source.service);
  }
  return (inventory.terminal || []).some((session) =>
    session.service === source.service && session.check === source.check);
}

function sessionUsageRow(session, idleReady) {
  return {
    idle: session.idle_seconds || 0, idleReady: !!idleReady,
    cpu: session.cpu || 0, cpuReady: !!session.cpu_ready,
    memory: session.rss || 0, memoryReady: !!session.memory_ready,
    ioRead: session.io_read || 0, ioWrite: session.io_write || 0, ioReady: !!session.io_ready,
    metricsExpected: true,
  };
}

function unmeasuredSessionUsageRow() {
  return {
    idle: 0, idleReady: false,
    cpu: 0, cpuReady: false,
    memory: 0, memoryReady: false,
    ioRead: 0, ioWrite: 0, ioReady: false,
    metricsExpected: false,
  };
}

function sessionRows(inventory) {
  const ssh = (inventory.ssh || []).map((session) => ({
    kind: sessionKindSSH, service: session.service || "", user: session.user || "",
    name: session.terminal || "", pid: session.pid || 0, pidSort: session.pid || 0, state: sessionStateActive,
    detail: nothing,
    ...sessionUsageRow(session, true),
    action: sshSessionCloseButton(session.service || "", session),
  }));
  const terminal = (inventory.terminal || []).map((session) => ({
    kind: session.multiplexer || "", service: session.service || "", user: session.user || "",
    name: session.name || "", pid: (session.pids || []).join(", "), pidSort: (session.pids || [0])[0] || 0,
    state: session.state || sessionStateUnknown,
    detail: Number.isInteger(session.windows) ? `${fmtNum(session.windows, 0)} window${session.windows === 1 ? "" : "s"}` : "—",
    ...sessionUsageRow(session, session.has_idle),
    action: terminalSessionCloseButton(session),
  }));
  const issues = (inventory.sources || []).flatMap((source) => (source.issues || []).map((issue) => ({
    kind: source.kind || "", service: source.service || "", user: issue.user || source.user || "",
    name: issue.terminal || source.check || "—", pid: issue.pid || 0, pidSort: issue.pid || 0, state: sessionSourceUnavailable,
    detail: issue.message || source.message || "Session attribution unavailable",
    ...unmeasuredSessionUsageRow(),
    action: sshSessionCloseButton(source.service || "", issue),
  })));
  // A sessionless source earns a row only when it has something to say: an
  // unavailable or collecting source is a problem or a wait worth seeing, and
  // an empty tmux server with a socket is a process an admin can close. An
  // available ssh or screen source with nobody connected says nothing — no row.
  const emptySources = (inventory.sources || [])
    .filter((source) => !(source.issues || []).length)
    .filter((source) => !sourceHasSession(source, inventory))
    .filter((source) => source.state !== sessionSourceAvailable || source.can_close_empty)
    .map((source) => ({
    kind: source.kind || "", service: source.service || "", user: source.user || "",
    name: source.check || "—", pid: 0, pidSort: 0,
    state: source.state === sessionSourceAvailable ? sessionStateEmpty : source.state || targetStateCollecting,
    detail: source.message || (source.state === sessionSourceAvailable ? "No active sessions" : "Waiting for a sample"),
    ...unmeasuredSessionUsageRow(),
    action: emptySessionCloseButton(source),
  }));
  return [...ssh, ...terminal, ...issues, ...emptySources];
}

function sessionFilterCounts(inventory, rows) {
  const sessions = [
    ...(inventory.ssh || []).map(() => ({ kind: sessionKindSSH })),
    ...(inventory.terminal || []),
  ];
  const counts = stateCounts(sessions, (session) => session.multiplexer || session.kind, sessionTypeFilterStates);
  counts[filterAll] = rows.length;
  return counts;
}

const sessionSortKeys = {
  type: (row) => row.kind,
  user: (row) => row.user,
  session: (row) => row.name,
  pid: (row) => row.pidSort,
  state: (row) => row.state,
  idle: (row) => row.idleReady ? row.idle : -1,
  cpu: (row) => row.cpuReady ? row.cpu : -1,
  memory: (row) => row.memoryReady ? row.memory : -1,
  io: (row) => row.ioReady ? row.ioRead + row.ioWrite : -1,
};
const sessionSortReadyKeys = {
  idle: "idleReady",
  cpu: "cpuReady",
  memory: "memoryReady",
  io: "ioReady",
};

function setSessionSort(key) { toggleSort(sessionSort, key, renderSessions); }

function sessionSortMissing(row, key) {
  const readyKey = sessionSortReadyKeys[key];
  return !!readyKey && !row[readyKey];
}

function sortSessionRows(rows) {
  const value = sessionSortKeys[sessionSort.key];
  if (!sessionSort.key || !value) return rows;
  rows.sort((a, b) => {
    const aMissing = sessionSortMissing(a, sessionSort.key);
    const bMissing = sessionSortMissing(b, sessionSort.key);
    if (aMissing !== bMissing) return aMissing ? 1 : -1;
    const primary = compareSortValues(value(a), value(b)) * sessionSort.dir;
    if (primary !== 0) return primary;
    return compareSortValues(sessionSortKeys.session(a), sessionSortKeys.session(b));
  });
  return rows;
}

function sessionIdleCell(row) {
  return row.idleReady ? fmtDuration(row.idle) : tpl`<span class="muted">—</span>`;
}

function sessionMetricPlaceholder(row) {
  return tpl`<span class="muted">${row.metricsExpected ? "measuring…" : "—"}</span>`;
}

function sessionCPUCell(row) {
  return row.cpuReady ? usageBarMini(pctClamp(row.cpu), fmtPct(row.cpu), `${fmtPct(row.cpu)} of host CPUs`) : sessionMetricPlaceholder(row);
}

function sessionMemoryCell(row) {
  return row.memoryReady ? memoryInline(row.memory) : tpl`<span class="muted">—</span>`;
}

function sessionIOCell(row) {
  if (!row.ioReady) return sessionMetricPlaceholder(row);
  return tpl`<span title="read / write">${fmtBytesPerSecond(row.ioRead)} / ${fmtBytesPerSecond(row.ioWrite)}</span>`;
}

function renderSessions(inventory = latestSessionInventory) {
  latestSessionInventory = inventory || {};
  const sources = latestSessionInventory.sources || [];
  setPanelVisible($("#sessions-section"), sources.length > 0);
  const rows = sessionRows(latestSessionInventory);
  renderFilterButtonCounts("#session-filters", sessionFilterCounts(latestSessionInventory, rows));
  const filtered = rows.filter((row) => {
    if (sessionFilter !== filterAll && row.kind !== sessionFilter) return false;
    return !sessionQuery || [row.kind, row.service, row.user, row.name, row.pid, row.state].join(" ").toLowerCase().includes(sessionQuery);
  });
  sortSessionRows(filtered);
  const body = $("#session-rows");
  if (body) {
    const content = filtered.length ? filtered.map((row) => tpl`<tr>
      <td>${row.kind || "—"}</td><td>${row.user || "—"}</td>
      <td>${row.name || "—"}${row.detail ? tpl`<br><span class="muted">${row.detail}</span>` : nothing}</td>
      <td class="mono">${row.pid || "—"}</td><td>${sessionStateCell(row.state)}</td>
      <td>${sessionIdleCell(row)}</td><td>${sessionCPUCell(row)}</td><td>${sessionMemoryCell(row)}</td>
      <td>${sessionIOCell(row)}</td><td>${row.action}</td>
    </tr>`) : tpl`<tr><td colspan="10" class="muted">No sessions match the filter.</td></tr>`;
    litRender(content, body);
  }
  const activeCount = (latestSessionInventory.ssh || []).length + (latestSessionInventory.terminal || []).length;
  const count = $("#sessions-count");
  if (count) count.textContent = String(activeCount);
  const filterCount = $("#session-filter-count");
  if (filterCount) filterCount.textContent = `${filtered.length}/${rows.length}`;
  updateSortIndicatorsFor("ssi", sessionSort, "#sessions-section .sessions-table th.sortable[data-session-sort]", "sessionSort");
  updateSectionNav();
}

function setSessionQuery(value) {
  sessionQuery = (value || "").trim().toLowerCase();
  renderSessions();
}

function setSessionFilter(value) {
  sessionFilter = value || filterAll;
  document.querySelectorAll("#session-filters button").forEach((button) => {
    const active = button.dataset.sf === sessionFilter;
    button.classList.toggle("f-active", active);
    button.setAttribute("aria-pressed", active ? domBoolTrue : domBoolFalse);
  });
  renderSessions();
}

function serviceCheckRows(d) {
  const checkRows = (d.checks || []).map((c) => {
    const age = c.at ? tpl` <span class="muted">· ${fmtAge(c.at)}</span>` : nothing;
    const state = checkStateHTML(c, age);
    const readings = (c.readings && c.readings.length) ? renderWatchReadings(c.readings) : nothing;
    const msg = c.message
      ? tpl`<span class="truncate check-message" title="${c.message || ""}">${c.message || ""}</span>`
      : nothing;
    const hasReadings = !!(c.readings && c.readings.length);
    const detailCell = (hasReadings || c.message) ? tpl`${readings}${msg}` : "—";
    return tpl`<tr><td>${c.name}</td><td class="muted">${c.type || ""}</td>
      <td>${state}${c.optional ? tpl` <span class="muted">(optional)</span>` : nothing}</td>
      <td class="sla-cell">${checkSLAHTML(d.name, c)}</td>
      <td class="muted">${detailCell}</td></tr>`;
  });
  return checkRows.length ? checkRows : tpl`<tr><td colspan="5" class="muted">No checks.</td></tr>`;
}

function serviceLockDetail(d) {
  const lockRowsArr = (d.locks || []).map((l) => {
    return tpl`<tr>
      <td>${lockName(l)}</td>
      <td>${lockStateHTML(l)}</td>
      <td>${lockTTL(l)}</td>
      <td>${lockOwner(l)}</td>
      <td>${lockCreated(l)}</td>
      <td>${lockBlocks(l)}</td>
      <td class="muted">${l.reason || l.stale_reason || ""}</td>
      <td>${lockReleaseButton(l)}</td>
    </tr>`;
  });
  return {
    rows: lockRowsArr.length ? lockRowsArr : tpl`<tr><td colspan="8" class="muted">No named runtime locks.</td></tr>`,
    warnings: (d.lock_warnings || []).map((warning) =>
      tpl`<div class="inactive detail-warn">warning: ${warning}</div>`),
  };
}

function serviceProcessTotals(d, procs) {
  return d.process_totals || {
    rss: procs.reduce((a, p) => a + (p.rss || 0), 0),
    io_read: procs.reduce((a, p) => a + (p.io_read || 0), 0),
    io_write: procs.reduce((a, p) => a + (p.io_write || 0), 0),
    fds: procs.reduce((a, p) => a + (p.fds || 0), 0),
    threads: procs.reduce((a, p) => a + (p.threads || 0), 0),
    count: procs.length,
  };
}

function serviceProcessDetail(d) {
  if (d.no_resident_process) return { general: nothing, section: nothing };
  const procs = d.processes || [];
  const procWarnings = d.process_warnings || [];
  const totals = serviceProcessTotals(d, procs);
  // When the host RAM total is known, show each process's resident memory as a
  // share of host RAM (a compact bar).
  const hostMem = hostMemTotalBytes();
  const memPct = (rss) => hostMem > 0 ? pctClamp((Number(rss) || 0) / hostMem * percentScale) : 0;
  // The whole-tree totals (memory, cpu, IO, fds, threads, process count) are the
  // General data grid's job; a summary line above the table would restate every
  // one of them. Discovery warnings are listed individually just below, so their
  // count would be restated too.
  const procWarns = procWarnings.map((w) => tpl`<div class="bad detail-warn">discovery warning: ${w}</div>`);
  const procRows = processRows(procs);
  const procTable = procs.length
    ? tpl`<table class="detail-compact-table">
        <caption class="visually-hidden">Service processes</caption>
        <thead><tr><th scope="col">PID</th><th scope="col">CMD</th><th scope="col">User</th><th scope="col">Role</th><th scope="col" title="CPU used by this process, normalized to one core">CPU</th><th scope="col" title="The most a single core was used by this process: its busiest thread. Each cell's own tooltip says whether that figure was measured per thread or bounded by the process rate.">Max core</th><th scope="col">Mem</th><th scope="col">IO r/w</th><th scope="col">FDs</th><th scope="col">Threads</th></tr></thead>
        <tbody>${procRows.map((row) => { const p = row.p; return tpl`<tr>
          <td>${p.pid}</td>
          <td>${procTreeLabel(row)}</td>
          <td class="muted">${p.user || ""}</td>
          <td class="muted">${procRoleCell(p)}</td>
          ${procCpuCell(p)}
          ${procMaxCoreCell(p)}
          <td>${p.rss ? (hostMem > 0 ? usageBarMini(memPct(p.rss), fmtBytes(p.rss)) : fmtBytes(p.rss)) : '—'}</td>
          ${procIoFdThreadCells(p)}
        </tr>`; })}</tbody></table>`
    : tpl`<div class="${procWarnings.length ? "bad" : "muted"}">${procWarnings.length ? "No processes discovered; check discovery warnings." : "No processes found."}</div>`;
  const general = tpl`<div><span class="muted">Processes</span><br>${totals.count} process${totals.count === 1 ? "" : "es"}</div>
    <div class="${colDupWide}"><span class="muted">CPU total</span><br>${totalsCpuCell(totals)}</div>
    <div class="${colDupWide}"><span class="muted">Memory</span><br>${memoryInline(totals.rss)}</div>
    <div class="${colDupWide}"><span class="muted">IO R/W</span><br>${ioRWInline(totals.io_read, totals.io_write)}</div>
    <div><span class="muted">FDs / Threads</span><br>${fmtNum(totals.fds || 0, 0)} / ${fmtNum(totals.threads || 0, 0)}</div>`;
  return {
    general,
    section: tpl`<h2>Processes</h2>${procWarns}${procTable}`,
  };
}

// metricPanelBody is what every graph panel of a detail grid holds: its title, a
// summary line and a chart box, addressed by one id pair. The wrapper stays with
// the caller because that is the part that genuinely differs — a latency or named
// metric panel names on it the check it belongs to, for tests and selectors.
function metricPanelBody(summaryID, chartID, title) {
  return tpl`<div class="metric-title">${title}</div>
    <div id="${summaryID}" class="muted">loading…</div>
    <div id="${chartID}" class="muted chart-box"></div>`;
}

// runtimeMetricPanels is the CPU/memory/IO trio, built from the one list that
// already defines those three metrics everywhere else (runtimeMetricDefs). The
// service detail and the daemon panel both render it, so process metrics look the
// same in both places instead of the daemon keeping an untitled variant of its
// own. idFor(key, suffix) names the pair of elements each panel is filled through.
function runtimeMetricPanels(idFor) {
  return runtimeMetricDefs.map(({ key, label }) => tpl`<div class="metric-panel">
    ${metricPanelBody(idFor(key, "summary"), idFor(key, "chart"), label)}
  </div>`);
}

// paintMetricPanel writes one panel's two elements, and failMetricPanel reports
// into that same pair. Every graph panel in the dashboard is filled through them,
// so a failure is announced the same way everywhere instead of each loader
// choosing its own wording and its own severity class.
function paintMetricPanel(summaryID, chartID, painted) {
  const summary = document.getElementById(summaryID);
  const chart = document.getElementById(chartID);
  if (summary) summary.innerHTML = painted.summary;
  if (chart) chart.innerHTML = painted.chart;
}

function failMetricPanel(summaryID, chartID, what, err) {
  paintMetricPanel(summaryID, chartID,
    { summary: `<span class="bad">Failed to load ${esc(what)}: ${esc(err.message)}</span>`, chart: "" });
}

// metricPanelContent turns one measured series into what a panel shows: its
// summary line and its chart, both on the unit the series carries, falling back
// to the caller's when the payload names none. Latency, named check metrics,
// service runtime, daemon runtime and watch metrics all paint through it, so a
// series can never be summarised in one unit and plotted in another — which is
// exactly what the latency panel used to do.
function metricPanelContent(series, fallbackUnit, win, chartLabel) {
  const s = series || {};
  const unit = s.unit || fallbackUnit || "";
  return {
    summary: metricSeriesSummary({ ...s, unit }),
    chart: drawMetricChart(s.points || [], unit, win, chartLabel),
  };
}

// loadMetricPanel fills one summary-plus-chart panel: it finds the pair, runs the
// window protocol every graph shares and paints what content(body, win) returns as
// {summary, chart}. The panels differ only in the request they make and the two
// strings they produce, so they cannot drift apart in how they handle a stale
// response, a missing element or a failure.
function loadMetricPanel(windowKey, summaryID, chartID, generation, url, content, what) {
  if (!document.getElementById(summaryID) || !document.getElementById(chartID)) return Promise.resolve(true);
  return loadServiceWindowGraph(windowKey, generation, url,
    (body, win) => paintMetricPanel(summaryID, chartID, content(body, win)),
    (e) => failMetricPanel(summaryID, chartID, what, e));
}

// serviceRuntimeMetricDomID names one service runtime panel's summary or chart.
function serviceRuntimeMetricDomID(service, key, suffix) {
  return detailDomId(service, `runtime-${key}-${suffix}`);
}

// daemonMetricDomID names one daemon process-metric panel's summary or chart.
function daemonMetricDomID(key, suffix) {
  return `daemon-${key}-${suffix}`;
}

function serviceGraphDetail(d) {
  const noResidentProcess = !!d.no_resident_process;
  const measured = serviceMeasuredChecks(d);
  const checkMetrics = serviceCheckMetrics(d);
  // One latency graph per measured check (health, port, service, …) instead of a
  // single chart switched by buttons; these lead the graph grid.
  const latencyPanels = noResidentProcess
    ? []
    : measured.length
    ? measured.map((c) => tpl`<div class="metric-panel" data-latency-check="${c.name}">
        ${metricPanelBody(serviceLatencyDomID(d.name, c.name, "summary"), serviceLatencyDomID(d.name, c.name, "chart"),
          tpl`Latency <span class="muted">${c.name}</span>`)}
      </div>`)
    : [tpl`<div class="metric-panel">
        <div class="metric-title">Latency</div>
        <div class="muted">No latency checks configured for this service.</div>
      </div>`];
  const runtimeGraphPanels = noResidentProcess
    ? nothing
    : runtimeMetricPanels((key, suffix) => serviceRuntimeMetricDomID(d.name, key, suffix));
  const checkMetricPanels = checkMetrics.map((metric) => metric.band
    ? slaChartPanel(svcBandKey(d.name, metric.check, metric.name),
      { title: metric.label || serviceCheckMetricLabel(metric), tag: metric.name })
    : tpl`<div class="metric-panel" data-service-metric-check="${metric.check}" data-service-metric-name="${metric.name}">
    ${metricPanelBody(serviceCheckMetricDomID(d.name, metric.check, metric.name, "summary"),
      serviceCheckMetricDomID(d.name, metric.check, metric.name, "chart"), serviceCheckMetricLabel(metric))}
  </div>`);
  const win = serviceMetricState(d.name).window;
  return tpl`<h2>Graphs <span class="muted">${winButtons(metricWins, win, "setMetricWin", "Graph time window", d.name)}</span></h2>
    <div class="metric-grid">
      ${slaChartPanel(d.name)}
      ${latencyPanels}
      ${checkMetricPanels}
      ${runtimeGraphPanels}
    </div>`;
}

function serviceGeneralDetail(d, processGeneral) {
  // Name leads the grid: the row's own heading was dropped as a repeat of the
  // component, and an expansion scrolled away from its row otherwise has nothing
  // identifying it. Name and State stay put at every width for that reason; every
  // other field that also exists as a column is deduplicated — see colDupWide.
  return tpl`<div class="runtime-grid">
      <div><span class="muted">Name</span><br><b>${displayName(d)}</b></div>
      <div><span class="muted">State</span><br>${serviceStateCell(d)}</div>
      <div class="${colDupWide}"><span class="muted">Category</span><br>${categoryBadge(categoryOf(d, defaultCategoryService))}</div>
      <div><span class="muted">Unit</span><br>${unitCell(d)}</div>
      <div><span class="muted">Backend</span><br>${d.backend || "—"}</div>
      <div class="${colDupWide}"><span class="muted">Uptime</span><br>${serviceUptimeCell(d)}</div>
      <div><span class="muted">Interval</span><br>${d.interval ? d.interval : tpl`<span class="muted">—</span>`}</div>
      <div><span class="muted">Dry run</span><br><b>${d.dry_run ? "yes" : "no"}</b></div>
      <div><span class="muted">Policy</span><br>${policyCell(d)}</div>
      <div><span class="muted">Locks</span><br>${locksCell(d)}</div>
      <div class="${colDupPhone}"><span class="muted">Last event</span><br>${lastEventCell(d)}</div>
      <div class="${colDupPhone}"><span class="muted">Strays</span><br>${serviceStraysCell(d)}</div>
      <div><span class="muted">Next remediation</span><br>${nextRemediationCell(d)}</div>
      <div><span class="muted">Remediation</span><br>${renderRemediation(d.remediation)}</div>
      ${processGeneral}
    </div>`;
}

function renderServiceDetail(d) {
  const checks = serviceCheckRows(d);
  const locks = serviceLockDetail(d);
  const processes = serviceProcessDetail(d);
  const disabledNote = !d.enabled
    ? tpl`<p class="muted bad">This service is disabled in configuration (enabled: false). Edit its YAML file and reload the daemon to activate it.</p>`
    : nothing;
  return tpl`<div class="service-detail" data-service-detail="${d.name}">
    ${disabledNote}
    ${serviceGeneralDetail(d, processes.general)}
    ${serviceGraphDetail(d)}
    ${processes.section}
    <h2>Checks</h2>
    <table>
      <caption class="visually-hidden">Service checks</caption>
      <thead><tr><th scope="col">Check</th><th scope="col">Type</th><th scope="col">State</th><th scope="col">SLA</th><th scope="col">Message</th></tr></thead>
      <tbody>${checks}</tbody></table>
    <h2>Named locks</h2>
    <table>
      <caption class="visually-hidden">Service named locks</caption>
      <thead><tr><th scope="col">Name</th><th scope="col">State</th><th scope="col">TTL</th><th scope="col">Owner</th><th scope="col">Created</th><th scope="col">Blocks</th><th scope="col">Reason</th><th scope="col"><span class="visually-hidden">Actions</span></th></tr></thead>
      <tbody>${locks.rows}</tbody></table>${locks.warnings}
    <h2>Rules</h2>
    ${renderRules(d.rules)}
    <h2>Preflight ${servicePreflightButton(d)}</h2>
    <div id="${detailDomId(d.name, "preflight")}" class="muted">not run yet</div>
    <h2>Recent events</h2>
    <table class="events">
      <caption class="visually-hidden">Recent service events</caption>
      <thead><tr><th scope="col">Time</th><th scope="col">Kind</th><th scope="col">Message</th></tr></thead>
      <tbody id="${detailDomId(d.name, "events")}"></tbody></table>
  </div>`;
}

async function hydrateServiceDetail(d, generation = dashboardGeneration) {
  const results = await Promise.all([refreshServiceGraphs(d, generation), loadServiceEvents(d.name, generation)]);
  return results.every(Boolean);
}

async function refreshServiceGraphs(d, generation = dashboardGeneration) {
  const measured = serviceMeasuredChecks(d);
  const checkMetrics = serviceCheckMetrics(d);
  syncWindowButtons("setMetricWin", serviceMetricState(d.name).window, d.name);
  const pending = [loadSLAPanel(d.name, generation)];
  pending.push(...serviceSLAChecks(d).map((c) => loadCheckSLA(d.name, c.name, generation)));
  pending.push(...checkMetrics.map((metric) => metric.band
    ? loadSLAPanel(svcBandKey(d.name, metric.check, metric.name), generation,
      { windowKey: d.name, warn: metric.severity === targetStateWarning })
    : loadCheckMetric(d.name, metric, generation)));
  if (!d.no_resident_process) {
    if (measured.length) pending.push(loadMetrics(d.name, measured, generation));
    pending.push(loadServiceRuntimeMetrics(d.name, generation));
  }
  const results = await Promise.all(pending);
  return results.every(Boolean);
}

function usageLevel(pct) {
  pct = pctClamp(pct);
  if (pct <= 0) return "usage-empty";
  if (pct >= usageCriticalPct) return "usage-crit";
  if (pct >= usageHighPct) return "usage-high";
  if (pct >= usageWarnPct) return "usage-warn";
  return "usage-ok";
}

// usageBarSpan is the shared markup for both bars: a coloured fill sized to the
// clamped percentage with a centered label. extraClass adds a size modifier
// (e.g. " usagebar-sm"); ariaLabel, when non-empty, sets the aria-label
// attribute (omitted otherwise). label/title are bound as text/attribute, so
// lit-html escapes them — callers pass plain strings.
function usageBarSpan(p, extraClass, label, title, ariaLabel, elId) {
  return tpl`<span id="${elId || nothing}" class="usagebar${extraClass} ${usageLevel(p)}" style="--usage-pct:${p.toFixed(2)}%" title="${title}" aria-label="${ariaLabel || nothing}"><span class="usagebar-fill"></span><span class="usagebar-label">${label}</span></span>`;
}

// usageBar renders the full-width host gauge. The visible in-bar label defaults
// to "X% used"; pass `label` to override it (the overview tiles show just the
// percentage since the tile value already says "used"). The tooltip/aria keep
// the full "used · free" breakdown regardless.
function usageBar(pct, label, elId) {
  const p = pctClamp(pct);
  const used = fmtPct(p);
  const freeLabel = fmtPct(percentMax - p);
  return usageBarSpan(p, "", label != null ? label : used, `${used} used · ${freeLabel} free`, `${used} used, ${freeLabel} free`, elId);
}

// usageBarMini is the compact bar used inside dense tables (the process list).
// Same colour scale as usageBar but narrower, and the caller supplies the label
// (e.g. the byte value) so the cell stays readable.
function usageBarMini(pct, label, title) {
  const p = pctClamp(pct);
  const lbl = label != null ? label : fmtPct(p);
  const tip = title != null ? title : `${fmtPct(p)} of host RAM`;
  return usageBarSpan(p, " usagebar-sm", lbl, tip, "");
}

// hostMemTotalBytes returns the host's total RAM in bytes from the last
// /api/host snapshot, or 0 when unknown (so callers can skip the bar).
function hostMemTotalBytes() {
  const m = (latestHostMetrics || []).find((x) => x.name === hostMetricTotalMemory);
  return m && m.total > 0 ? Number(m.total) : 0;
}

// cpuBarMini renders a single-core-normalized CPU% as a compact bar (100% = one
// full core). A multithreaded process can exceed 100%; the bar caps at full but
// the label keeps the true value.
// title overrides the default tooltip for callers whose bar is not "this process's
// own rate" — the busiest-thread cell, for one.
function cpuBarMini(pct, title) {
  const v = Number(pct) || 0;
  return usageBarMini(pctClamp(v), fmtPct(v), title != null ? title : `${fmtPct(v)} of one core used by this process`);
}

// storageUsedPct returns the used percentage 0..100, or null when the volume
// reports no usable figures — so callers render "—" instead of a misleading
// 0% (empty/healthy-looking) bar for missing data.
function storageUsedPct(d) {
  if (!d) return null;
  const used = Number(d.used_bytes);
  const total = Number(d.total_bytes);
  if (Number.isFinite(used) && Number.isFinite(total) && total > 0) return pctClamp((used / total) * percentScale);
  const free = Number(d.free_bytes);
  if (Number.isFinite(free) && Number.isFinite(total) && total > 0) return pctClamp(((total - free) / total) * percentScale);
  return Number.isFinite(Number(d.used_pct)) ? pctClamp(d.used_pct) : null;
}

function notifierNames(w) {
  return (w && Array.isArray(w.notifiers)) ? w.notifiers.filter(Boolean) : [];
}

// meterParts returns the [title, detail] strings for a generic usage meter
// (memory/load/fds/pids/conntrack), shared by the summary cell, the search
// index, and the detail panel so the wording can't drift.
function meterParts(m) {
  if (!m) return null;
  switch (m.kind) {
    case metricNameMemory:
      return [`${fmtBytes(m.total_bytes)} total`,
        `${fmtBytes(m.used_bytes)} used · ${fmtBytes(m.free_bytes)} free`];
    case "load":
      return [`${m.num_cpu || 0} CPU${(m.num_cpu === 1) ? "" : "s"}`,
        `load1 ${fmtNum(m.load || 0, 2)} · ${fmtPct(m.used_pct)} of capacity`];
    case "fds":
      return [`${fmtNum(m.max || 0, 0)} file descriptors max`,
        `${fmtNum(m.count || 0, 0)} allocated · ${fmtNum((m.max || 0) - (m.count || 0), 0)} free`];
    case "pids":
      return [`${fmtNum(m.max || 0, 0)} max`,
        `${fmtNum(m.count || 0, 0)} in use · ${fmtNum((m.max || 0) - (m.count || 0), 0)} free`];
    case "conntrack":
      return [`${fmtNum(m.max || 0, 0)} max`,
        `${fmtNum(m.count || 0, 0)} entries · ${fmtNum((m.max || 0) - (m.count || 0), 0)} free`];
    default:
      return null;
  }
}

function watchReadings(w) {
  return (w && Array.isArray(w.readings)) ? w.readings.filter(Boolean) : [];
}

function readingText(reading) {
  if (!reading) return "";
  const label = reading.label || reading.field || "sample";
  const bad = reading.error || reading.warning;
  return bad ? `${label}: ${bad}` : `${label} ${reading.value || ""}`.trim();
}

function watchMonitorHint(w) {
  const bits = [];
  if (w.monitor_source) bits.push(fmtMonitorSource(w.monitor_source));
  if (w.monitor_changed_at) bits.push(fmtAge(w.monitor_changed_at));
  return bits.length ? tpl` <span class="muted">${bits.join(" · ")}</span>` : nothing;
}

function watchMonitoringCell(w) {
  if (!w || !w.enabled) return stateBadgeLabel(targetStateDisabled, "disabled in config");
  if (!w.monitored) return tpl`${stateBadgeLabel(targetStateDisabled, "monitoring paused")}${watchMonitorHint(w)}`;
  return tpl`${stateBadgeLabel(targetStateOK, "active")}${watchMonitorHint(w)}`;
}

function watchMonitorMode(w) {
  return w && w.monitor ? w.monitor : monitorModeEnabled;
}

function watchHasNotify(w) {
  return notifierNames(w).length > 0 || (w && Number(w.notifier_count || 0) > 0);
}

function watchHasExpand(w) {
  return !!(w && w.expand && Number(w.expand.by_bytes) > 0);
}

// watchStateText reads the server-computed health state, including stale
// daemon-published samples. Monitor state remains available to actions and
// search, but the State column renders one state badge.
function watchStateText(w) {
  return (w && w.state) || backendStatusUnknown;
}

function watchSampleState(w) {
  return (w && w.sample_state) || "";
}

function watchStateRank(w) {
  return stateRank(watchStateText(w));
}

function watchStateCell(w) {
  if (watchProbeRunning(w)) {
    const startedAt = w.probe.started_at;
    return tpl`${stateBadgeLabel(targetStateCollecting, "checking")} <span class="watch-probe" data-probe-started-at="${startedAt}" role="status" aria-live="polite"></span> <span class="muted">previously ${watchStateText(w)}</span>`;
  }
  if (!w || !w.enabled || !w.monitored) return watchMonitoringCell(w);
  return stateBadge(watchStateText(w));
}

function watchProbeRunning(w) {
  return !!(w && w.probe && w.probe.state === operationStateRunning && w.probe.started_at);
}

function watchProbeElapsed(startedAt) {
  const started = new Date(startedAt);
  if (Number.isNaN(started.getTime())) return "";
  return fmtSince(Date.now() - started.getTime());
}

function watchSummaryText(w) {
  if (!w) return "";
  const sw = w.swap;
  if (sw) {
    return ["swap", fmtBytes(sw.used_bytes) + " used", fmtBytes(sw.free_bytes) + " free",
      fmtPct(sw.used_pct) + " used"].join(" ");
  }
  const d = w.storage;
  if (d) {
    return [
      d.path,
      d.filesystem,
      d.mount_point,
      d.device,
      d.mounted ? "mounted" : "not mounted",
      d.sample_error,
      d.mount_sample_error,
      d.free_bytes != null ? fmtBytes(d.free_bytes) + " free" : "",
      storageUsedPct(d) == null ? "" : fmtPct(storageUsedPct(d)) + " used",
    ].filter(Boolean).join(" ");
  }
  const parts = meterParts(w.meter);
  if (parts) return parts.join(" ") + " " + fmtPct(w.meter.used_pct) + " used";
  const readings = watchReadings(w);
  if (readings.length) return [w.summary, ...readings.map(readingText)].filter(Boolean).join(" ");
  return w.summary || "";
}

function watchSearchText(w) {
  const conditions = (w.conditions || []).map((c) => `${c.field || ""} ${c.op || ""} ${c.value || ""}`).join(" ");
  const category = categoryOf(w, defaultCategoryWatch);
  return [
    displayName(w),
    w.name,
    watchScope(w),
    category,
    w.check_type,
    watchSummaryText(w),
    w.interval,
    w.fire_on_fail ? "on fail" : "on threshold",
    w.has_hook ? "hook" : "",
    (w.hook_command || []).join(" "),
    notifierNames(w).join(" "),
    watchHasNotify(w) ? "notify notifiers" : "",
    watchHasExpand(w) ? actionExpand : "",
    w.dry_run ? "dry run dry-run" : "",
    watchStateText(w),
    w && w.monitored ? "monitoring enabled" : "monitoring paused",
    watchMonitorMode(w),
    w.last_activity_kind,
    conditions,
  ].filter(Boolean).join(" ").toLowerCase();
}

function getWatchPanel(panel) {
  return watchPanels[panel] || watchPanels.host;
}

// watchTypeValue is the value a panel's type dropdown filters on. Most panels
// filter by check_type; a panel can override with typeOf (e.g. Storage filters
// by filesystem type since all its watches share one check_type).
function watchTypeValue(panel, w) {
  if (panel.key === watchPanelKeyHost) return watchGroupOf(w);
  return (panel.typeOf ? panel.typeOf(w) : w.check_type) || "";
}

function watchMatches(w, panelKey) {
  const panel = getWatchPanel(panelKey);
  if (panel.query && !watchSearchText(w).includes(panel.query)) return false;
  if (panel.type !== filterAll && watchTypeValue(panel, w) !== panel.type) return false;
  return watchStatusFilterStates.includes(panel.status) ? watchStateText(w) === panel.status : true;
}

function syncWatchFilterActive(panelKey) {
  const panel = getWatchPanel(panelKey);
  syncFilterButtons(panel.filters, "wf", panel.status);
}

function setWatchQuery(panelKey, v) {
  const panel = getWatchPanel(panelKey);
  panel.query = (v || "").trim().toLowerCase();
  renderWatches();
  saveUIState();
}

function setWatchStatus(panelKey, v) {
  const panel = getWatchPanel(panelKey);
  panel.status = normalizeWatchStatusFilter(v);
  syncWatchFilterActive(panelKey);
  renderWatches();
  saveUIState();
}

function setAllWatchStatuses(v) {
  Object.keys(watchPanels).forEach((key) => {
    watchPanels[key].status = v || filterAll;
    syncWatchFilterActive(key);
  });
  renderWatches();
  saveUIState();
}

function openAllWatchPanels() {
  Object.values(watchPanels).forEach((panel) => {
    const sec = $(panel.section);
    if (sec) sec.open = true;
  });
}

function setWatchType(panelKey, v) {
  const panel = getWatchPanel(panelKey);
  panel.type = v || filterAll;
  renderWatches();
  saveUIState();
}

function setWatchGrouped(panelKey, grouped) {
  const panel = getWatchPanel(panelKey);
  panel.grouped = !!grouped;
  renderWatches();
  saveUIState();
}

function toggleAllWatchGroups(panelKey) {
  const panel = getWatchPanel(panelKey);
  const watches = (allWatches || []).filter((watch) => watchPanelKeyFor(watch) === panelKey && watchMatches(watch, panelKey));
  const groups = sortedGroupValues(watches, watchGroupOf);
  toggleAllGroups(groups, panel.collapsedGroups);
  renderWatches();
  saveUIState();
}

// syncWatchTypeSelect repopulates one watch panel's type dropdown from the
// distinct check types currently present in that panel (with per-type counts).
// A single type cannot filter anything, so it is hidden and the selection is
// reset to all to prevent an invisible stale filter.
function syncWatchTypeSelect(panelKey, watches) {
  const panel = getWatchPanel(panelKey);
  const select = $(panel.typeSelect);
  if (!select) return filterAll;
  const counts = new Map();
  (watches || []).forEach((w) => {
    const t = watchTypeValue(panel, w);
    if (t) counts.set(t, (counts.get(t) || 0) + 1);
  });
  const types = [...counts.keys()].sort((a, b) =>
    a.localeCompare(b, undefined, { numeric: true, sensitivity: "base" }));
  const visible = types.length > 1;
  select.hidden = !visible;
  select.disabled = !visible;
  const next = visible && panel.type !== filterAll && counts.has(panel.type) ? panel.type : filterAll;
  select.innerHTML = `<option value="${filterAll}">${esc(panel.allTypesLabel)}</option>` + types.map((t) =>
    `<option value="${esc(t)}">${esc(t)} (${counts.get(t)})</option>`).join("");
  select.value = next;
  return next;
}

function renderWatchFilterCounts(panelKey, watches) {
  const w = watches || allWatches || [];
  renderFilterButtonCounts(getWatchPanel(panelKey).filters, stateCounts(w, watchStateText, watchStatusFilterStates));
}

function watchPanelFilterActive(panel) {
  return !!(panel.query || panel.status !== filterAll || panel.type !== filterAll);
}

function parseDurationSeconds(raw) {
  const s = String(raw || "").trim();
  if (!s) return 0;
  let total = 0;
  let matched = false;
  // The input is a duration this dashboard formatted itself (fmtDuration),
  // never operator or network text, so the backtracking below is bounded by a
  // short fixed shape.
  // eslint-disable-next-line sonarjs/super-linear-regex -- bounded fmtDuration input
  const re = /(\d+(?:\.\d+)?)(ms|s|m|h)/g;
  let m;
  while ((m = re.exec(s)) !== null) {
    matched = true;
    const n = parseFloat(m[1]);
    switch (m[2]) {
      case "ms": total += n / millisecondsPerSecond; break;
      case "s": total += n; break;
      case "m": total += n * secondsPerMinute; break;
      case "h": total += n * secondsPerHour; break;
    }
  }
  if (matched) return total;
  const n = parseFloat(s);
  return Number.isFinite(n) ? n : 0;
}

const watchSortKeys = {
  name: (w) => displayName(w).toLowerCase(),
  group: (w) => watchGroupOf(w).toLowerCase(),
  type: (w) => (w.check_type || "").toLowerCase(),
  indicator: (w) => watchPrimaryMetricText(w).toLowerCase(),
  summary: (w) => watchSummaryText(w).toLowerCase(),
  interval: (w) => parseDurationSeconds(w.interval),
  polarity: (w) => w.fire_on_fail ? "fail" : "threshold",
  hook: (w) => w.has_hook ? 1 : 0,
  notifiers: (w) => notifierNames(w).join(" ").toLowerCase() || Number(w.notifier_count || 0),
  last: (w) => w.last_activity || "",
  state: watchStateRank,
};

function setWatchSort(panelKey, key) { toggleSort(getWatchPanel(panelKey).sort, key, renderWatches); }

function updateWatchSortIndicators(panelKey) {
  const panel = getWatchPanel(panelKey);
  updateSortIndicatorsFor("wi", panel.sort, `${panel.section} th.sortable[data-watch-sort]`, "watchSort", panel.section);
}

function watchPanelKeyFor(_watch) {
  return watchPanelKeyHost;
}

// watchPanelKeyForElement reads the panel key straight from the enclosing
// <details data-panel="..."> attribute, so panel markup names its own key.
function watchPanelKeyForElement(el) {
  const details = el && el.closest("details[data-panel]");
  const key = details ? details.dataset.panel : "";
  return watchPanels[key] ? key : watchPanelKeyHost;
}

function renderConditionRows(conditions) {
  const list = conditions || [];
  if (!list.length) return tpl`<div class="muted condition-empty">No configured predicates.</div>`;
  const rows = list.map((c) => tpl`<tr>
    <td><code>${c.field || ""}</code></td>
    <td>${c.op || ""}</td>
    <td><code>${c.value || ""}</code></td>
  </tr>`);
  return tpl`<div class="muted condition-heading">Check predicates</div>
    <table class="detail-compact-table condition-table">
      <caption class="visually-hidden">Check predicates</caption>
      <thead><tr><th scope="col">Field</th><th scope="col">Op</th><th scope="col">Value</th></tr></thead>
      <tbody>${rows}</tbody>
    </table>`;
}

function renderStorageWatch(d) {
  if (!d) return nothing;
  const usedPct = storageUsedPct(d);
  const fs = d.filesystem || "unknown";
  const mount = d.mounted
    ? tpl`<code>${d.mount_point || ""}</code>`
    : tpl`<span class="bad">not found</span>`;
  const device = d.device ? tpl`<code>${d.device}</code>` : tpl`<span class="muted">unknown</span>`;
  const options = (d.options || []).length
    ? (d.options || []).map((o, i) => i ? [" ", tpl`<code>${o}</code>`] : tpl`<code>${o}</code>`)
    : tpl`<span class="muted">none</span>`;
  const inodes = d.inodes_total
    ? `${fmtNum(Number(d.inodes_total) - Number(d.inodes_free || 0), 0)} used / ${fmtNum(d.inodes_total, 0)} total (${fmtPct(d.inodes_used_pct)} used)`
    : tpl`<span class="muted">not reported</span>`;
  const errors = [
    d.sample_error ? `statfs: ${d.sample_error}` : "",
    d.mount_sample_error ? `mounts: ${d.mount_sample_error}` : "",
  ].filter(Boolean).map((m) => tpl`<div class="bad">${m}</div>`);
  return tpl`<div class="watch-grid">
    <div><span class="muted">Path</span><br><code>${d.path || ""}</code></div>
    <div><span class="muted">Mount point</span><br>${mount}</div>
    <div><span class="muted">Filesystem</span><br><code>${fs}</code></div>
    <div><span class="muted">Device</span><br>${device}</div>
    <div><span class="muted">Total</span><br><b>${fmtBytes(d.total_bytes)}</b></div>
    <div><span class="muted">Used</span><br>${usedPct == null ? tpl`<span class="muted">—</span>` : usageBar(usedPct)} <b>${fmtBytes(d.used_bytes)}</b></div>
    <div><span class="muted">Free</span><br><b>${fmtBytes(d.free_bytes)}</b> (${fmtPct(d.free_pct)})</div>
    <div><span class="muted">Inodes</span><br>${inodes}</div>
    <div><span class="muted">Options</span><br>${options}</div>
  </div>${errors}`;
}

// renderMeterWatch shows a memory/load/fds/pids/conntrack watch's live gauge in
// the expansion detail, reusing the watch-grid layout that renderStorageWatch uses.
function renderMeterWatch(m) {
  if (!m) return nothing;
  const usedPct = pctClamp(m.used_pct || 0);
  const cells = [];
  if (m.kind === metricNameMemory) {
    cells.push(
      tpl`<div><span class="muted">Total</span><br><b>${fmtBytes(m.total_bytes)}</b></div>`,
      tpl`<div><span class="muted">Used</span><br>${usageBar(usedPct)} <b>${fmtBytes(m.used_bytes)}</b></div>`,
      tpl`<div><span class="muted">Free</span><br><b>${fmtBytes(m.free_bytes)}</b></div>`);
  } else if (m.kind === "load") {
    cells.push(
      tpl`<div><span class="muted">Load 1m</span><br><b>${fmtNum(m.load || 0, 2)}</b></div>`,
      tpl`<div><span class="muted">CPUs</span><br><b>${m.num_cpu || 0}</b></div>`,
      tpl`<div><span class="muted">Capacity</span><br>${usageBar(usedPct)} <b>${fmtPct(m.used_pct)}</b></div>`);
  } else { // fds | pids | conntrack
    const label = m.kind === "fds" ? "Allocated" : (m.kind === "conntrack" ? "Entries" : "In use");
    cells.push(
      tpl`<div><span class="muted">${label}</span><br><b>${fmtNum(m.count || 0, 0)}</b></div>`,
      tpl`<div><span class="muted">Max</span><br><b>${fmtNum(m.max || 0, 0)}</b></div>`,
      tpl`<div><span class="muted">Used</span><br>${usageBar(usedPct)} <b>${fmtPct(m.used_pct)}</b></div>`,
      tpl`<div><span class="muted">Free</span><br><b>${fmtNum((m.max || 0) - (m.count || 0), 0)}</b></div>`);
  }
  return tpl`<div class="watch-grid">${cells}</div>`;
}

function renderWatchReadings(readings) {
  const list = (readings || []).filter(Boolean);
  if (!list.length) return nothing;
  const cells = list.map((r) => {
    const label = r.label || r.field || "Sample";
    const longValue = ["issuer", "dns_names"].includes(r.field || "");
    // An advisory reading uses the same amber the SMART/LVM health cell and the
    // preflight "warn" row use, so one colour means one thing across the panel.
    const value = r.error
      ? tpl`<span class="watch-reading-value bad">${r.error}</span>`
      : r.warning
        ? tpl`<span class="watch-reading-value inactive">${r.warning}</span>`
        : r.good
          ? tpl`<b class="watch-reading-value good">${r.value || "—"}</b>`
          : tpl`<b class="watch-reading-value">${r.value || "—"}</b>`;
    return tpl`<div class="watch-reading${longValue ? " watch-reading-long" : ""}"><span class="muted">${label}</span><br>${value}</div>`;
  });
  return tpl`<div class="watch-grid">${cells}</div>`;
}

const storageWatchTypes = new Set(["diskio", "hdparm", "lvm", "raid", "smart", "storage"]);
const networkWatchTypes = new Set(["conntrack", "firewall", "icmp", "net"]);
const securityWatchTypes = new Set(["cert", "file"]);
const summaryFileWatchType = "file-summary";

const watchScopeHost = "host";
const watchScopeService = "service";

function watchScope(w) {
  return w && w.scope === watchScopeService ? watchScopeService : watchScopeHost;
}

// watchScopeSuffix is the scope marker appended to a watch name in the row. Host
// scope is the default for the panel, so only the service scope is worth the
// pixels; the full value stays available in the expansion and in search text.
function watchScopeSuffix(w) {
  return watchScope(w) === watchScopeService ? tpl` <span class="muted watch-scope">${watchScopeService}</span>` : nothing;
}

// watchGroupOf is the presentation taxonomy for watches. It deliberately
// groups stable operator concepts instead of creating a new table per check.
function watchGroupOf(w) {
  const type = String((w && w.check_type) || "").toLowerCase();
  if (storageWatchTypes.has(type)) return "Storage";
  if (networkWatchTypes.has(type) || categoryOf(w, defaultCategoryWatch).toLowerCase() === "network") return "Network";
  if (securityWatchTypes.has(type) || categoryOf(w, defaultCategoryWatch).toLowerCase() === "security") return "Security";
  return "System";
}

function watchTypeKey(w) {
  if (w && w.check_type === "file" && w.summary_configured) return summaryFileWatchType;
  return (w && w.check_type) || "";
}

function watchActionDisabled(w, action) {
  if (!w || !w.enabled) return true;
  if (watchStateText(w) === targetStateStarting) return true;
  switch (action) {
    case actionMonitor: return !!w.monitored || pendingMonitorToggles.has(watchExpansionKey(w.name));
    case actionUnmonitor: return !w.monitored || pendingMonitorToggles.has(watchExpansionKey(w.name));
    case actionExpand: return !watchHasExpand(w);
    case actionProbe: return !w.can_probe || watchProbeRunning(w);
    case actionPause: return !w.can_control_raid;
    case actionResume: return !w.can_control_raid;
    case actionReplicationStart: return !w.can_control_replication;
    default: return false;
  }
}

function watchActionDisabledReason(w, action) {
  if (watchStateText(w) === targetStateStarting) return "watch is starting";
  switch (action) {
    case actionMonitor:
      if (w.monitored) return "watch is already monitored";
      return "";
    case actionUnmonitor:
      if (!w.monitored) return "watch is paused";
      return "";
    case actionExpand:
      if (!watchHasExpand(w)) return "expand is not configured";
      return "";
    case actionProbe:
      if (!w.can_probe) return "manual probe is not supported";
      return watchProbeRunning(w) ? "manual probe is already running" : "";
    case actionPause:
    case actionResume:
      return w.can_control_raid ? "" : "RAID control is not configured";
    case actionReplicationStart:
      return w.can_control_replication ? "" : "replication control is not configured";
    default: return "";
  }
}

function watchActionAccessibility(w, action) {
  const disabled = watchActionDisabled(w, action);
  const reason = watchActionDisabledReason(w, action);
  const hintID = actionHintID("wat", w.name, action);
  return { disabled, hint: actionHint(hintID, disabled, reason), describedBy: actionDescribedBy(hintID, disabled, reason) };
}

function watchActionButton(w, action, content, compact = false) {
  const label = watchActionAriaLabel(w, action);
  const accessibility = watchActionAccessibility(w, action);
  const className = compact ? "icon-btn" : nothing;
  return tpl`${accessibility.hint}<button class="${className}" ?disabled=${accessibility.disabled} data-watch="${w.name}" data-watch-action="${action}" title="${compact ? label : nothing}" aria-label="${label}" aria-describedby="${accessibility.describedBy}">${content}</button>`;
}

function watchActionAriaLabel(w, action) {
  return actionAriaLabel("watch", w, action);
}

// readingValue returns the formatted value of a named live reading (as shipped
// in /api/watches) for display in a type-specific column, or "—" when absent.
// Used by the Certificate and Disk I/O panels. Field names match checkreadings.go.
function readingValue(w, field) {
  const r = ((w && w.readings) || []).find((x) => x && x.field === field);
  if (!r) return "—";
  if (r.error) return tpl`<span class="bad">${r.error}</span>`;
  if (r.warning) return tpl`<span class="inactive">${r.warning}</span>`;
  return r.value != null && r.value !== "" ? r.value : "—";
}

// readingRaw returns a reading's raw string value (no error markup) for sorting.
function readingRaw(w, field) {
  const r = ((w && w.readings) || []).find((x) => x && x.field === field);
  return r && r.value != null ? String(r.value) : "";
}

// watchLastCell renders the shared "Last activity" cell content.
function watchLastCell(w) {
  return activityDateCell({
    time: w && w.last_activity,
    kind: w && w.last_activity_kind,
  });
}

function watchLastCheckedCell(w) {
  const checked = activityDateCell({ time: w && w.last_checked_at });
  if (watchSampleState(w) !== targetStateStale) return checked;
  return tpl`${checked}<span class="watch-sample-note" title="The latest completed watch sample is older than its freshness limit.">${stateBadgeLabel(targetStateStale, targetStateStale)}</span>`;
}

// watchNameCell renders the shared expandable name cell (chevron + toggle).
function watchNameCell(w, key, open) {
  const chev = tpl`<span class="exp" aria-hidden="true">${open ? '▾' : '▸'}</span>`;
  return tpl`<td>${chev}<button type="button" class="row-toggle" data-exp-toggle="${key}" aria-expanded="${open}" aria-controls="${open ? "exp-" + key : nothing}" aria-label="${expandToggleAriaLabel(displayName(w), open, "watch details")}">${displayName(w)}${watchScopeSuffix(w)}</button></td>`;
}

// watchActionsCell renders the shared actions cell (expand / monitor / unmonitor).
function watchActionsCell(w) {
  const probeBtn = (w.can_probe && me.can_act && w.enabled)
    ? watchActionButton(w, actionProbe, tpl`<span aria-hidden="true">◎</span>`, true)
    : nothing;
  const raidButtons = (w.can_control_raid && me.can_act && w.enabled)
    ? tpl`${watchActionButton(w, actionPause, "pause RAID")} ${watchActionButton(w, actionResume, "resume RAID")}`
    : nothing;
  const replicationBtn = (w.can_control_replication && me.can_act && w.enabled)
    ? watchActionButton(w, actionReplicationStart, "start replication")
    : nothing;
  const expandBtn = (w.expand && Number(w.expand.by_bytes) > 0 && me.can_act && w.enabled)
    ? watchActionButton(w, actionExpand, `${actionExpand} ${fmtBytes(w.expand.by_bytes)}`)
    : nothing;
  const monitorBtn = !w.enabled
    ? tpl`<span class="muted">disabled in config</span>`
    : (me.can_act
      ? (w.monitored
        ? watchActionButton(w, actionUnmonitor, tpl`<span aria-hidden="true">⊘</span>`, true)
        : watchActionButton(w, actionMonitor, tpl`<span aria-hidden="true">◉</span>`, true))
      : tpl`<span class="muted">read-only</span>`);
  const actions = !w.enabled
    ? tpl`<span class="muted">disabled in config</span>`
    : tpl`${probeBtn} ${raidButtons} ${replicationBtn} ${expandBtn} ${monitorBtn}`;
  return tpl`<td class="actions">${actions}</td>`;
}

// watchRowClass mirrors the service/app row highlight: a firing watch (state
// "failed") or one whose device went missing paints the row red, a warning
// amber, matching serviceRowParts so certificate and every other watch type
// follow the same visual line.
function watchRowClass(state) {
  const failing = state === targetStateFailed || state === targetStateMissing;
  return failing ? "row-failing" : (state === targetStateWarning || state === targetStateStale ? "row-warning" : "");
}

// watchExpansionRow returns the inline expansion row when open. Its colspan must
// match the number of columns in the panel's table — 9 for most, but the
// Certificate panel passes 10 for its extra Key type column.
function watchExpansionRow(key, open, cols = 9) {
  return open
    ? tpl`<tr class="exp-row" id="exp-${key}" data-exp="${key}"><td colspan="${cols}"></td></tr>`
    : null;
}

// watchRowParts builds the shared watch row shell and its optional expansion.
// Callers supply only the cells that vary between the generic and typed views.
function watchRowParts(w, cells, colCount) {
  const state = watchStateText(w);
  const key = watchExpansionKey(w.name);
  const open = expanded.has(key);
  const row = tpl`<tr id="wat-row-${w.name}" class="clickable ${watchRowClass(state)}" data-exp-key="${key}">
    ${watchNameCell(w, key, open)}
    ${cells}
    <td>${watchLastCheckedCell(w)}</td>
    <td>${watchLastCell(w)}</td>
    <td>${watchStateCell(w)}</td>
    ${watchActionsCell(w)}
  </tr>`;
  return { row, expRow: watchExpansionRow(key, open, colCount) };
}

// storageUsageCell renders the occupied-space progress bar (with used/total
// byte breakdown) for a storage watch, or a clear error/placeholder.
function storageUsageCell(w) {
  const d = w.storage;
  if (!d) return tpl`<span class="muted">—</span>`;
  if (d.sample_error) return tpl`<span class="bad">${d.sample_error}</span>`;
  const usedPct = storageUsedPct(d);
  if (usedPct == null) return tpl`<span class="muted">—</span>`;
  return tpl`${usageBar(usedPct)} <span class="muted">${fmtBytes(d.used_bytes)} / ${fmtBytes(d.total_bytes)}</span>`;
}

// storageRowHTML renders a Storage-panel row, surfacing the occupied-space bar,
// filesystem and mount point in place of the generic type/summary columns.
// Health readings arrive in two vocabularies: the LVM check normalises its own
// to ok/error, while the SMART check reports the drive's verdict in smartctl's
// words (PASSED/FAILED) plus `unknown` for a drive that answered without one and
// `missing` for one that did not answer at all. watchHealthCell maps both, so a
// PASSED drive is not painted like a failing one and an absent verdict is not
// mistaken for a bad one.
const healthValuesOK = new Set([healthStatusOK, "passed"]);
const healthValueUnknown = "unknown";

function watchHealthCell(w) {
  const health = readingRaw(w, "health");
  if (!health) return tpl`<span class="muted">—</span>`;
  const cls = healthCellClass(health.toLowerCase());
  return tpl`<span class="${cls}">${health}</span>`;
}

function healthCellClass(health) {
  if (healthValuesOK.has(health)) return healthStatusOK;
  // A verdict the drive never gave is not a failing verdict: it reads as the
  // same warning an unknown backend status does.
  if (health === healthValueUnknown) return healthValueUnknown;
  return "bad";
}

function watchPrimaryMetricText(w) {
  const type = String((w && w.check_type) || "").toLowerCase();
  if (type === "storage" && w.storage) return fmtPct(storageUsedPct(w.storage));
  const fields = {
    cert: "days_left",
    diskio: "util_pct",
    lvm: "health",
    raid: "degraded",
    smart: "health",
  };
  const field = fields[type];
  if (field) return readingRaw(w, field);
  const first = watchReadings(w)[0];
  return first && first.value != null ? String(first.value) : "";
}

function watchPrimaryMetric(w) {
  const type = String((w && w.check_type) || "").toLowerCase();
  if (type === "storage") return storageUsageCell(w);
  if (type === "lvm") return watchHealthCell(w);
  const text = watchPrimaryMetricText(w);
  return text ? tpl`${text}` : tpl`<span class="muted">—</span>`;
}

function watchConditionValue(w, field) {
  const condition = (w.conditions || []).find((item) => item && item.field === field);
  if (!condition) return "—";
  return [condition.op, condition.value].filter(Boolean).join(" ") || "—";
}

function readingSortValue(w, field) {
  const raw = readingRaw(w, field);
  const number = Number.parseFloat(raw);
  return Number.isFinite(number) ? number : raw.toLowerCase();
}

function storageFilesystemCell(w) {
  const filesystem = w.storage && w.storage.filesystem;
  return filesystem ? tpl`<code>${filesystem}</code>` : tpl`<span class="muted">—</span>`;
}

function storageMountCell(w) {
  const mount = w.storage && w.storage.mount_point;
  return mount ? tpl`<code>${mount}</code>` : tpl`<span class="muted">—</span>`;
}

function typedReadingCell(w, field) {
  return readingValue(w, field);
}

function meterColumn(key, label, format) {
  return {
    key, label,
    cell: (w) => {
      const value = w.meter && w.meter[key];
      return value == null ? tpl`<span class="muted">—</span>` : tpl`${format(value)}`;
    },
    sort: (w) => numericSortValue(w.meter && w.meter[key]),
  };
}

// meterUsageColumns are the count-vs-ceiling columns of pids and conntrack,
// whose live sample lives on the meter rather than in readings.
const meterUsageColumns = [
  meterColumn("count", "In use", (v) => fmtNum(v, 0)),
  meterColumn("max", "Ceiling", (v) => fmtNum(v, 0)),
  meterColumn("used_pct", "Usage", fmtPct),
];

function swapColumn(key, label, format) {
  return {
    key, label,
    cell: (w) => {
      const value = w.swap && w.swap[key];
      return value == null ? tpl`<span class="muted">—</span>` : tpl`${format(value)}`;
    },
    sort: (w) => numericSortValue(w.swap && w.swap[key]),
  };
}

function textReadingColumn(key, label) {
  return { key, label, cell: (w) => typedReadingCell(w, key), sort: (w) => readingRaw(w, key).toLowerCase() };
}

function numericReadingColumn(key, label) {
  return { key, label, cell: (w) => typedReadingCell(w, key), sort: (w) => readingSortValue(w, key) };
}

const fileAgeColumn = {
  key: "age",
  label: "Age",
  cell: (w) => typedReadingCell(w, "age"),
  sort: (w) => parseDurationSeconds(readingRaw(w, "age")),
};

// watchTypeProfiles is the single presentation owner for every host-watch
// subtype. A profile owns its useful live columns, sortable values and optional
// subtype filter; generic summaries are deliberately not used in this view.
const watchTypeProfiles = {
  storage: {
    label: "Filesystems",
    filter: { label: "Filesystem", value: (w) => (w.storage && w.storage.filesystem) || "unknown" },
    columns: [
      { key: "usage", label: "Usage", cell: storageUsageCell, sort: (w) => numericSortValue(storageUsedPct(w.storage)) },
      { key: "filesystem", label: "Filesystem", cell: storageFilesystemCell, sort: (w) => ((w.storage && w.storage.filesystem) || "").toLowerCase() },
      { key: "mount", label: "Mount point", cell: storageMountCell, sort: (w) => (w.storage && w.storage.mount_point) || "" },
    ],
  },
  file: {
    label: "File checks",
    columns: [
      textReadingColumn("path", "Path"),
      numericReadingColumn("size", "Size"),
      fileAgeColumn,
      { key: "older_than", label: "Limit", cell: (w) => watchConditionValue(w, "older_than"), sort: (w) => parseDurationSeconds(watchConditionValue(w, "older_than")) },
    ],
  },
  net: {
    label: "Network interfaces",
    columns: [
      textReadingColumn("interface", "Interface"),
      textReadingColumn("driver", "Driver"),
      numericReadingColumn("speed", "Speed"),
      textReadingColumn("addresses", "IP"),
      textReadingColumn("state", "Link"),
      numericReadingColumn("errors", "Errors"),
    ],
  },
  hdparm: {
    label: "Disk speed",
    columns: [
      textReadingColumn("device", "Device"),
      textReadingColumn("bus", "Bus"),
      numericReadingColumn("read", "Buffered read"),
      numericReadingColumn("cached", "Cached read"),
    ],
  },
  lvm: {
    label: "LVM",
    columns: [
      { key: "health", label: "Health", cell: watchHealthCell, sort: (w) => readingRaw(w, "health").toLowerCase() },
      textReadingColumn("volume_group", "VG"),
      textReadingColumn("logical_volume", "LV"),
      numericReadingColumn("vg_size_bytes", "VG size"),
      numericReadingColumn("vg_free_bytes", "VG free"),
      textReadingColumn("lvm_reasons", "Reasons"),
    ],
  },
  smart: {
    label: "SMART",
    columns: [
      textReadingColumn("device", "Device"),
      textReadingColumn("bus", "Interface"),
      { key: "health", label: "Health", cell: watchHealthCell, sort: (w) => readingRaw(w, "health").toLowerCase() },
      numericReadingColumn("temperature", "Temperature"),
      numericReadingColumn("wear", "Wear"),
      // A drive that has started remapping sectors is spending its spare pool;
      // the count is the earliest number that moves before a verdict flips.
      numericReadingColumn("reallocated", "Reallocated"),
      numericReadingColumn("power_on_hours", "Power-on time"),
    ],
  },
  diskio: {
    label: "Disk I/O",
    columns: [
      textReadingColumn("device", "Device"),
      textReadingColumn("bus", "Bus"),
      numericReadingColumn("util_pct", "Utilization"),
      numericReadingColumn("read_bytes", "Read"),
      numericReadingColumn("write_bytes", "Write"),
      numericReadingColumn("await_ms", "Await"),
      // Totals since boot. A window of zeroes is the same for a disk that is idle
      // right now and one nothing ever touches; these tell the two apart.
      numericReadingColumn("read_total_bytes", "Read total"),
      numericReadingColumn("write_total_bytes", "Written total"),
    ],
  },
  cert: {
    label: "Certificates",
    columns: [
      textReadingColumn("source", "Source"),
      numericReadingColumn("days_left", "Days left"),
      { key: "expires", label: "Expires", cell: (w) => typedReadingCell(w, "not_after"), sort: (w) => readingRaw(w, "not_after") },
      textReadingColumn("issuer", "Issuer"),
    ],
  },
  raid: {
    label: "RAID",
    columns: [
      textReadingColumn("array", "Array"),
      numericReadingColumn("total_bytes", "Size"),
      numericReadingColumn("degraded", "Degraded"),
      numericReadingColumn("recovering", "Recovering"),
    ],
  },
  clock: {
    label: "Clock",
    columns: [
      numericReadingColumn("offset_seconds", "Offset"),
      numericReadingColumn("stratum", "Stratum"),
    ],
  },
  conntrack: { label: "Conntrack", columns: meterUsageColumns },
  failed_units: {
    label: "Failed units",
    columns: [
      numericReadingColumn("count", "Failed"),
      textReadingColumn("units", "Units"),
    ],
  },
  fds: {
    label: "File descriptors",
    columns: [numericReadingColumn("allocated", "Allocated")],
  },
  firewall_rules: {
    label: "Firewall rules",
    columns: [
      numericReadingColumn("rules", "Rules"),
      numericReadingColumn("min_rules", "Min rules"),
    ],
  },
  icmp: {
    label: "ICMP",
    columns: [numericReadingColumn("latency_ms", "Latency")],
  },
  inotify: {
    label: "Inotify",
    columns: [
      numericReadingColumn("instances", "Instances"),
      numericReadingColumn("watches", "Watches"),
    ],
  },
  load: {
    label: "Load",
    columns: [numericReadingColumn("value", "Load")],
  },
  memory: {
    label: "Memory",
    columns: [
      numericReadingColumn("used_pct", "Used"),
      numericReadingColumn("available_bytes", "Available"),
    ],
  },
  nfs: {
    label: "NFS",
    columns: [textReadingColumn("result", "Result")],
  },
  oom: {
    label: "OOM",
    columns: [numericReadingColumn("kills", "OOM kills")],
  },
  pids: { label: "PIDs", columns: meterUsageColumns },
  pressure: {
    label: "Pressure",
    columns: [
      textReadingColumn("resource", "Resource"),
      numericReadingColumn("some_avg60", "Some avg60"),
      numericReadingColumn("full_avg60", "Full avg60"),
    ],
  },
  process_policy: {
    label: "Process policies",
    columns: [
      textReadingColumn("user", "User"),
      numericReadingColumn("matches", "Processes"),
      numericReadingColumn("violation_count", "Violations"),
    ],
  },
  route: {
    label: "Routes",
    columns: [
      textReadingColumn("family", "Family"),
      textReadingColumn("interface", "Interface"),
      textReadingColumn("gateway", "Gateway"),
    ],
  },
  swap: {
    label: "Swap",
    columns: [
      swapColumn("used_pct", "Used", fmtPct),
      swapColumn("used_bytes", "Used bytes", fmtBytes),
      swapColumn("free_bytes", "Free bytes", fmtBytes),
    ],
  },
  zombies: {
    label: "Zombies",
    columns: [numericReadingColumn("zombies", "Zombies")],
  },
  replication: {
    label: "Replication",
    columns: [
      textReadingColumn("source_host", "Source"),
      { key: "io_stopped", label: "IO thread", cell: (w) => readingValue(w, "io_stopped"), sort: (w) => readingRaw(w, "io_stopped") },
      { key: "sql_stopped", label: "SQL thread", cell: (w) => readingValue(w, "sql_stopped"), sort: (w) => readingRaw(w, "sql_stopped") },
      numericReadingColumn("behind_seconds", "Behind"),
    ],
  },
};

function watchTypeProfile(type) {
  if (type === summaryFileWatchType) {
    return {
      label: "File summaries",
      columns: [
        textReadingColumn("path", "Path"),
        fileAgeColumn,
        { key: "summary", label: "Summary", cell: (w) => w.summary || "—", sort: (w) => w.summary || "" },
      ],
    };
  }
  return watchTypeProfiles[type] || {
    label: type || "Other",
    columns: [{ key: "value", label: "Value", cell: watchPrimaryMetric, sort: watchPrimaryMetricText }],
  };
}

function watchTypeLabel(type) { return watchTypeProfile(type).label; }

function watchTypeSort(panel, type) {
  const saved = panel.typeSorts[type];
  return saved && typeof saved.key === "string" ? saved : { key: "name", dir: 1 };
}

function setWatchTypeSort(panelKey, type, key) {
  const panel = getWatchPanel(panelKey);
  const sort = watchTypeSort(panel, type);
  if (sort.key === key) sort.dir = -sort.dir;
  else { sort.key = key; sort.dir = 1; }
  panel.typeSorts[type] = sort;
  renderWatches();
  saveUIState();
}

function setWatchTypeFilter(panelKey, type, value) {
  getWatchPanel(panelKey).typeFilters[type] = value || filterAll;
  renderWatches();
  saveUIState();
}

function watchTypeRows(type, watches, panel, profile) {
  const filter = profile.filter;
  const selected = panel.typeFilters[type] || filterAll;
  const list = filter && selected !== filterAll ? watches.filter((w) => filter.value(w) === selected) : [...watches];
  const sort = watchTypeSort(panel, type);
  const column = profile.columns.find((item) => item.key === sort.key);
  const sharedSorts = {
    name: (w) => displayName(w).toLowerCase(),
    checked: (w) => w.last_checked_at || "",
    last: (w) => w.last_activity || "",
    state: watchStateRank,
  };
  const sortValue = sharedSorts[sort.key] || (column && column.sort);
  if (sortValue) {
    list.sort((a, b) => {
      const primary = compareSortValues(sortValue(a), sortValue(b)) * sort.dir;
      return primary || compareSortValues(displayName(a), displayName(b));
    });
  }
  return list;
}

function watchTypeFilterControl(panel, type, watches, profile) {
  if (!profile.filter) return nothing;
  const counts = new Map();
  watches.forEach((w) => {
    const value = profile.filter.value(w);
    counts.set(value, (counts.get(value) || 0) + 1);
  });
  if (counts.size < 2) return nothing;
  const selected = counts.has(panel.typeFilters[type]) ? panel.typeFilters[type] : filterAll;
  return tpl`<label class="watch-type-filter">${profile.filter.label}
    <select data-watch-type-filter-panel="${panel.key}" data-watch-type-filter="${type}" aria-label="Filter ${watchTypeLabel(type)} by ${profile.filter.label}">
      <option value="${filterAll}" ?selected=${selected === filterAll}>all</option>
      ${[...counts.keys()].sort().map((value) => tpl`<option value="${value}" ?selected=${selected === value}>${value} (${counts.get(value)})</option>`)}
    </select>
  </label>`;
}

function typedWatchRowHTML(w, profile) {
  const colCount = profile.columns.length + 5;
  const parts = watchRowParts(w, profile.columns.map((column) => tpl`<td data-watch-type-column="${column.key}">${column.cell(w)}</td>`), colCount);
  return parts.expRow ? [parts.row, parts.expRow] : [parts.row];
}

function renderWatchTypeTable(panel, type, watches) {
  const profile = watchTypeProfile(type);
  const list = watchTypeRows(type, watches, panel, profile);
  const sort = watchTypeSort(panel, type);
  const columns = [{ key: "name", label: "Name" }, ...profile.columns, { key: "checked", label: "Last checked" }, { key: "last", label: "Last activity" }, { key: "state", label: "State" }, { label: "Actions" }];
  const rows = list.flatMap((watch) => typedWatchRowHTML(watch, profile));
  return tpl`<section class="watch-type-group">
    <div class="watch-type-heading"><h3>${watchTypeLabel(type)} <span class="muted">(${watches.length})</span></h3>${watchTypeFilterControl(panel, type, watches, profile)}</div>
    <table class="watch-table watch-type-table">
      <thead><tr>${columns.map((column) => column.key
        ? tpl`<th scope="col" class="sortable" tabindex="0" data-watch-type-column="${column.key}" data-watch-type-sort-panel="${panel.key}" data-watch-type-sort-type="${type}" data-watch-type-sort="${column.key}" aria-sort="${sortAriaValue(sort, column.key)}">${column.label}<span class="sort-ind" data-watch-type-sort-ind="${type}:${column.key}">${sort.key === column.key ? (sort.dir > 0 ? " ▲" : " ▼") : ""}</span></th>`
        : tpl`<th scope="col">${column.label}</th>`)}</tr></thead>
      <tbody>${rows.length ? rows : tpl`<tr><td colspan="${columns.length}" class="muted">No ${watchTypeLabel(type).toLowerCase()} watches match this filter.</td></tr>`}</tbody>
    </table>
  </section>`;
}

function renderWatchGroups(panel, watches) {
  const groups = sortedGroupValues(watches, watchGroupOf);
  return groups.flatMap((group) => {
    const groupWatches = watches.filter((watch) => watchGroupOf(watch) === group);
    const collapsed = panel.collapsedGroups.has(group);
    const types = sortedGroupValues(groupWatches, watchTypeKey);
    return [tpl`<tr class="group-row"><td colspan="${panel.cols}"><button type="button" class="row-toggle group-toggle" data-group-panel="${panel.groupPanel}" data-group-name="${group}" aria-expanded="${collapsed ? domBoolFalse : domBoolTrue}"><span class="exp" aria-hidden="true">${collapsed ? "▸" : "▾"}</span>${group} <span class="muted">${groupWatches.length}</span></button></td></tr>`,
      collapsed ? nothing : tpl`<tr><td colspan="${panel.cols}">${types.map((type) => renderWatchTypeTable(panel, type, groupWatches.filter((watch) => watchTypeKey(watch) === type)))}</td></tr>`];
  });
}

function renderWatches(watches) {
  if (watches) allWatches = watches;
  scheduleGlobalTargetSync();
  preserveExpansionCells(isWatchExpansionKey);
  renderWatchPanel(watchPanelKeyHost, allWatches || []);
  reassertExpansions();
  applyHash();
  updateSectionNav();
}

// renderWatchPanel fills the watch table from its already-classified subset,
// using the same search/type/status filters, visible count and column sorting.
function renderWatchPanel(panelKey, watches) {
  const panel = getWatchPanel(panelKey);
  const section = $(panel.section);
  const tbody = $(panel.rows);
  const cnt = $(panel.count);
  const filterCount = $(panel.filterCount);
  if (!section || !tbody) return;
  const outerHead = section.querySelector(".watch-table > thead");
  if (outerHead) outerHead.hidden = true;
  const total = (watches || []).length;
  if (!panelShell({ section, tbody, count: cnt, filterCount, total })) return;
  renderWatchFilterCounts(panelKey, watches);
  panel.type = syncWatchTypeSelect(panelKey, watches);
  syncWatchFilterActive(panelKey);
  const list = (watches || []).filter((w) => watchMatches(w, panelKey));
  if (panel.sort.key && watchSortKeys[panel.sort.key]) {
    sortedBy(list, panel.sort, watchSortKeys, "name");
  } else if (panel.defaultSortByName) {
    sortedBy(list, { key: "name", dir: 1 }, watchSortKeys, "name");
  }
  updateWatchSortIndicators(panelKey);
  const groupOf = watchGroupOf;
  const groups = sortedGroupValues(list, groupOf);
  panel.collapsedGroups.forEach((group) => { if (!groups.includes(group)) panel.collapsedGroups.delete(group); });
  if (groups.length < 2) panel.grouped = false;
  updateGroupButtons(panel.groupPrefix, panel.grouped, groups, panel.collapsedGroups, panel.groupLabel, "group");
  const filtered = watchPanelFilterActive(panel);
  if (filterCount) filterCount.textContent = filtered ? `showing ${list.length} of ${total}` : "";
  const content = list.length
    ? renderWatchGroups(panel, list)
    : tpl`<tr><td colspan="${panel.cols || 9}" class="muted">${filtered ? panel.emptyFiltered : panel.empty}</td></tr>`;
  litRender(content, tbody);
  // The elapsed probe timer owns this plain span's text. Keeping it outside a
  // lit binding prevents the timer from deleting lit-html's part markers.
  updateWatchProbeElapsed();
}

// ---- Installed applications ----------------------------------------------
const appSortKeys = {
  name: (a) => displayName(a).toLowerCase(),
  category: (a) => categoryOf(a, defaultCategoryApp).toLowerCase(),
  state: appStateRank,
  version: (a) => (a.version_short || a.version || "").toLowerCase(),
  last: lastEventTime,
};
const appStateLabels = {
  [targetStateOK]: "Ok",
  [targetStateStarting]: "Starting",
  [targetStateWarning]: "Warning",
  [targetStateFailed]: "Failed",
};
function setAppSort(key) { toggleSort(appSort, key, renderApps); }
function setAppQuery(q) { appQuery = q || ""; renderApps(); saveUIState(); }
function setAppCategory(v) { appCategory = v || filterAll; renderApps(); saveUIState(); }
function setAppStatus(v) {
  appStatus = v || filterAll;
  syncFilterButtons("#app-filters", "af", appStatus);
  renderApps();
  saveUIState();
}
function renderAppFilterCounts() {
  renderFilterButtonCounts("#app-filters", stateCounts(allApps, appStateText, appStatusFilterStates));
}
function updateAppSortIndicators() {
  updateSortIndicatorsFor("ai", appSort, ".apps-table th.sortable[data-app-sort]", "appSort");
}
function appStateText(a) {
  if (a && a.state === targetStateStarting) return targetStateStarting;
  const status = String((a && a.status) || "").trim().toLowerCase();
  if (!status || status === targetStateOK) return targetStateOK;
  if (status.startsWith("error:") || status === "not installed" || status === "no binary configured") return targetStateFailed;
  return targetStateWarning;
}
function appStateRank(a) {
  switch (appStateText(a)) {
    case targetStateOK: return 0;
    case targetStateStarting: return 1;
    case targetStateWarning: return 2;
    case targetStateFailed: return 3;
    default: return 4;
  }
}
function appStatusLabel(a) {
  return appStateLabels[appStateText(a)] || "Unknown";
}
function appStatusCell(a) {
  const state = appStateText(a);
  const detail = (a && a.status && a.status !== targetStateOK) ? a.status : appStatusLabel(a);
  return tpl`<td class="status-cell status-${state}" title="${detail}">${stateBadgeLabel(state, appStatusLabel(a))}</td>`;
}

function artifactMatches(item, surface, categoryFilter, statusFilter, statusFilterStates, query) {
  const category = categoryOf(item, surface);
  if (categoryFilter !== filterAll && category !== categoryFilter) return false;
  if (statusFilterStates.includes(statusFilter) && appStateText(item) !== statusFilter) return false;
  if (!query) return true;
  const needle = query.toLowerCase();
  return displayName(item).toLowerCase().includes(needle)
    || (item.name || "").toLowerCase().includes(needle)
    || (item.display_name || "").toLowerCase().includes(needle)
    || category.toLowerCase().includes(needle)
    || appStateText(item).includes(needle)
    || (item.status || "").toLowerCase().includes(needle)
    || (item.version || "").toLowerCase().includes(needle)
    || (item.user || "").toLowerCase().includes(needle)
    || (item.group || "").toLowerCase().includes(needle);
}

function appMatches(a) {
  return artifactMatches(a, "app", appCategory, appStatus, appStatusFilterStates, appQuery);
}

function setAppGrouped(v) {
  appGrouped = !!v;
  renderApps();
  saveUIState();
}

function toggleAllAppGroups() {
  toggleAllArtifactGroups(allApps, appMatches, "app", appCollapsedGroups);
  renderApps();
  saveUIState();
}

function toggleAllArtifactGroups(items, matches, surface, collapsedGroups) {
  const categories = sortedCategories((items || []).filter(matches), surface);
  const allCollapsed = categories.length > 0 && categories.every((category) => collapsedGroups.has(category));
  if (allCollapsed) {
    categories.forEach((category) => collapsedGroups.delete(category));
  } else {
    categories.forEach((category) => collapsedGroups.add(category));
  }
}

function toggleGroup(panel, group) {
  if (!group) return;
  let collapsedGroups;
  let rerender;
  if (panel === "svc") {
    collapsedGroups = svcCollapsedGroups;
    rerender = renderServices;
  } else if (panel === "app") {
    collapsedGroups = appCollapsedGroups;
    rerender = renderApps;
  } else if (panel === "library") {
    collapsedGroups = libraryCollapsedGroups;
    rerender = renderLibraries;
  } else if (panel === "mount") {
    collapsedGroups = mountCollapsedGroups;
    rerender = renderMounts;
  } else if (splitServicePanels[panel]) {
    collapsedGroups = splitServicePanels[panel].collapsedGroups;
    rerender = renderServices;
  } else if (panel.startsWith("watch-")) {
    const key = panel.slice("watch-".length);
    const watchPanel = watchPanels[key];
    if (!watchPanel) return;
    collapsedGroups = watchPanel.collapsedGroups;
    rerender = renderWatches;
  }
  if (!collapsedGroups || !rerender) return;
  if (collapsedGroups.has(group)) collapsedGroups.delete(group);
  else collapsedGroups.add(group);
  rerender();
  saveUIState();
}

// renderApps lists the installed applications below the services table. The
// version column shows the short version; expanding a row reveals the full
// version string, binary location, permissions, user and group (all already in
// hand, so no extra request is needed).
function renderApps(apps) {
  renderArtifactPanel(appArtifactPanel, apps);
}

function renderArtifactExpansionDetails(artifact, surface) {
  const bin = artifact.binary ? tpl`<code>${artifact.binary}</code>` : tpl`<span class="muted">unknown</span>`;
  const perm = artifact.permissions ? tpl`<code>${artifact.permissions}</code>` : tpl`<span class="muted">—</span>`;
  const usr = artifact.user || tpl`<span class="muted">—</span>`;
  const grp = artifact.group || tpl`<span class="muted">—</span>`;
  const source = artifact.version_source
    ? tpl`<code>${artifact.version_source}</code>`
    : (artifact.version ? tpl`<span class="muted">local</span>` : tpl`<span class="muted">—</span>`);
  const state = appStateText(artifact);
  const statusClass = state === targetStateFailed ? "lvl-error" : (state === targetStateWarning ? "lvl-warning" : "");
  const status = artifact.status ? tpl`<span class="${statusClass}">${artifact.status}</span>` : "—";
  return tpl`<div class="watch-grid">
    <div><span class="muted">Version</span><br>${artifact.version || "—"}</div>
    <div><span class="muted">Version source</span><br>${source}</div>
    <div><span class="muted">Category</span><br>${categoryOf(artifact, surface)}</div>
    <div><span class="muted">Location</span><br>${bin}</div>
    <div><span class="muted">Permissions</span><br>${perm}</div>
    <div><span class="muted">User</span><br>${usr}</div>
    <div><span class="muted">Group</span><br>${grp}</div>
    <div><span class="muted">Status</span><br>${status}</div>
  </div>`;
}

// renderAppExpansion shows one application's full version, binary location and
// permissions, reusing the watch-grid layout. An application that maps to a
// monitored service also carries that service's Availability section: the figure
// is the service's own, so it is drawn by the panel that owns it rather than
// restated in a second, smaller form.
function renderAppExpansion(a) {
  const eventsId = detailDomId(a.name, "app-events");
  return tpl`${renderArtifactExpansionDetails(a, "app")}
  ${a.keeps_sla ? renderSLASection(appSLAKey(a.name)) : nothing}
  <h3 class="expansion-heading">Recent events</h3>
  <table class="events">
    <caption class="visually-hidden">Recent application events</caption>
    <thead><tr><th scope="col">Time</th><th scope="col">Kind</th><th scope="col">Message</th></tr></thead>
    <tbody id="${eventsId}"></tbody></table>`;
}

// loadAppSLA fills an expanded application's availability panel, memoized per
// dashboard generation. An application expansion is part of the rows template, so
// every artifact render repaints and refills it; the poll that triggered the
// render then awaits this same request instead of issuing a second one. A window
// change calls loadSLAPanel directly, so picking 7d always refetches.
const appSLALoads = new Map();
function loadAppSLA(name, generation = dashboardGeneration) {
  return sharedLoad(appSLALoads, `${generation}:${name}`, () => loadSLAPanel(appSLAKey(name), generation));
}

// loadAppEvents fills an expanded application's "Recent events" table with its
// monitoring history (errors/recoveries), mirroring loadServiceEvents.
const appEventLoads = new Map();
function loadAppEvents(name, generation = dashboardGeneration) {
  return sharedLoad(appEventLoads, `${generation}:${name}`, () => loadEventRows(
    detailDomId(name, "app-events"),
    applicationEventsAPI(name, eventDetailLimit),
    generation,
  ));
}

async function refreshExpandedApplications(generation = dashboardGeneration) {
  if (document.hidden) return true;
  const open = (allApps || []).filter((app) => expanded.has(appExpansionKey(app.name)));
  const results = await Promise.all(open.flatMap((app) => {
    // The availability panel plots the service's own series, so it goes stale on
    // the same poll the events do and is refreshed with them.
    const pending = [loadAppEvents(app.name, generation)];
    if (app.keeps_sla) pending.push(loadAppSLA(app.name, generation));
    return pending;
  }));
  return results.every(Boolean);
}

// ---- Installed libraries --------------------------------------------------
const librarySortKeys = {
  name: (library) => displayName(library).toLowerCase(),
  category: (library) => categoryOf(library, defaultCategoryLibrary).toLowerCase(),
  state: appStateRank,
  version: (library) => (library.version_short || library.version || "").toLowerCase(),
};
const libraryStatusFilterStates = [targetStateOK, targetStateWarning, targetStateFailed];

function setLibrarySort(key) { toggleSort(librarySort, key, renderLibraries); }
function setLibraryQuery(q) { libraryQuery = q || ""; renderLibraries(); saveUIState(); }
function setLibraryCategory(v) { libraryCategory = v || filterAll; renderLibraries(); saveUIState(); }
function setLibraryStatus(v) {
  libraryStatus = v || filterAll;
  syncFilterButtons("#library-filters", "lf", libraryStatus);
  renderLibraries();
  saveUIState();
}
function renderLibraryFilterCounts() {
  renderFilterButtonCounts("#library-filters", stateCounts(allLibraries, appStateText, libraryStatusFilterStates));
}
function updateLibrarySortIndicators() {
  updateSortIndicatorsFor("li", librarySort, ".libraries-table th.sortable[data-library-sort]", "librarySort");
}
function libraryMatches(library) {
  return artifactMatches(library, "library", libraryCategory, libraryStatus, libraryStatusFilterStates, libraryQuery);
}
function setLibraryGrouped(v) {
  libraryGrouped = !!v;
  renderLibraries();
  saveUIState();
}
function toggleAllLibraryGroups() {
  toggleAllArtifactGroups(allLibraries, libraryMatches, "library", libraryCollapsedGroups);
  renderLibraries();
  saveUIState();
}

function renderLibraries(libraries) {
  renderArtifactPanel(libraryArtifactPanel, libraries);
}

function renderLibraryExpansion(library) {
  return renderArtifactExpansionDetails(library, "library");
}

const appArtifactPanel = {
  surface: "app",
  section: "#apps-section",
  rows: "#app-rows",
  count: "#apps-count",
  filterCount: "#app-count",
  categorySelect: "#app-category",
  groupLabel: "applications",
  cols: 5,
  empty: "No applications match the filter.",
  detailLabel: "application details",
  rowPrefix: "app-row-",
  items: () => allApps || [],
  setItems: (items) => { allApps = items; },
  category: () => appCategory,
  setCategory: (category) => { appCategory = category; },
  renderFilterCounts: renderAppFilterCounts,
  matches: appMatches,
  sort: () => appSort,
  sortKeys: appSortKeys,
  updateSortIndicators: updateAppSortIndicators,
  grouped: () => appGrouped,
  setGrouped: (grouped) => { appGrouped = grouped; },
  collapsedGroups: () => appCollapsedGroups,
  filterActive: () => appQuery || appCategory !== filterAll || appStatus !== filterAll,
  expansionKey: appExpansionKey,
  renderExpansion: renderAppExpansion,
  extraCell: lastEventCell,
  loadExpanded: (items) => {
    items.forEach((app) => {
      if (!expanded.has(appExpansionKey(app.name))) return;
      loadAppEvents(app.name);
      if (app.keeps_sla) loadAppSLA(app.name);
    });
  },
};

const libraryArtifactPanel = {
  surface: "library",
  section: "#libraries-section",
  rows: "#library-rows",
  count: "#libraries-count",
  filterCount: "#library-count",
  categorySelect: "#library-category",
  groupLabel: "libraries",
  cols: 4,
  empty: "No libraries match the filter.",
  detailLabel: "library details",
  rowPrefix: "library-row-",
  items: () => allLibraries || [],
  setItems: (items) => { allLibraries = items; },
  category: () => libraryCategory,
  setCategory: (category) => { libraryCategory = category; },
  renderFilterCounts: renderLibraryFilterCounts,
  matches: libraryMatches,
  sort: () => librarySort,
  sortKeys: librarySortKeys,
  updateSortIndicators: updateLibrarySortIndicators,
  grouped: () => libraryGrouped,
  setGrouped: (grouped) => { libraryGrouped = grouped; },
  collapsedGroups: () => libraryCollapsedGroups,
  filterActive: () => libraryQuery || libraryCategory !== filterAll || libraryStatus !== filterAll,
  expansionKey: libraryExpansionKey,
  renderExpansion: renderLibraryExpansion,
};

function renderArtifactPanel(panel, items) {
  if (items) panel.setItems(items);
  scheduleGlobalTargetSync();
  const section = $(panel.section);
  const tbody = $(panel.rows);
  const count = $(panel.count);
  const filterCount = $(panel.filterCount);
  if (!section || !tbody) return;
  const inventory = panel.items();
  const total = inventory.length;
  if (!panelShell({ section, count, filterCount, total, onEmpty: updateSectionNav })) return;
  panel.setCategory(syncCategorySelect(panel.categorySelect, inventory, panel.surface, panel.category()));
  panel.renderFilterCounts();
  const list = inventory.filter(panel.matches);
  const sort = panel.sort();
  if (sort.key && panel.sortKeys[sort.key]) sortedBy(list, sort, panel.sortKeys, "name");
  panel.updateSortIndicators();
  const visibleCategories = sortedCategories(list, panel.surface);
  const collapsedGroups = panel.collapsedGroups();
  collapsedGroups.forEach((category) => { if (!visibleCategories.includes(category)) collapsedGroups.delete(category); });
  if (visibleCategories.length < 2) panel.setGrouped(false);
  updateGroupButtons(panel.surface, panel.grouped(), visibleCategories, collapsedGroups, panel.groupLabel);
  if (filterCount) filterCount.textContent = panel.filterActive() ? `showing ${list.length} of ${total}` : "";
  const row = (item) => renderArtifactRow(item, panel);
  const content = list.length
    ? (panel.grouped()
      ? renderGroupedRows(list, collapsedGroups, panel.surface, panel.surface, (item) => categoryOf(item, panel.surface), panel.cols, row, sort.key === "category" ? sort.dir : 1)
      : list.flatMap(row))
    : tpl`<tr><td colspan="${panel.cols}" class="muted">${panel.empty}</td></tr>`;
  litRender(content, tbody);
  if (panel.loadExpanded) panel.loadExpanded(inventory);
  applyHash();
  updateSectionNav();
}

function renderArtifactRow(item, panel) {
  const category = categoryOf(item, panel.surface);
  const state = appStateText(item);
  const rowClass = state === targetStateFailed ? "row-failing" : (state === targetStateWarning ? "row-warning" : "");
  const label = displayName(item);
  const key = panel.expansionKey(item.name);
  const open = expanded.has(key);
  const chevron = tpl`<span class="exp" aria-hidden="true">${open ? "▾" : "▸"}</span>`;
  const version = item.version_short || item.version || "—";
  const row = tpl`<tr id="${panel.rowPrefix}${item.name}" class="clickable ${rowClass}" data-exp-key="${key}">
    <td>${chevron}<button type="button" class="row-toggle" data-exp-toggle="${key}" aria-expanded="${open}" aria-controls="${open ? "exp-" + key : nothing}" aria-label="${expandToggleAriaLabel(label, open, panel.detailLabel)}">${label}</button></td>
    <td>${categoryBadge(category)}</td>
    ${appStatusCell(item)}
    <td>${version}</td>
    ${panel.extraCell ? panel.extraCell(item) : nothing}
  </tr>`;
  const expansion = open
    ? tpl`<tr class="exp-row" id="exp-${key}" data-exp="${key}"><td colspan="${panel.cols}">${panel.renderExpansion(item)}</td></tr>`
    : null;
  return expansion ? [row, expansion] : [row];
}

// renderWatchExpansion shows a watch's config summary and its recent
// activity (hooks/notifies fired), reusing the inline expansion mechanism.
function renderWatchExpansion(w, events) {
  w = w || {};
  const mode = watchMonitorMode(w);
  const polarity = w.fire_on_fail ? "on fail" : "on threshold";
  const names = notifierNames(w);
  const notifiers = names.length
    ? names.map((n, i) => i ? [" ", tpl`<code>${n}</code>`] : tpl`<code>${n}</code>`)
    : tpl`<span class="muted">none</span>`;
  const hook = (w.hook_command || []).length
    ? tpl`<code>${(w.hook_command || []).join(" ")}</code>`
    : (w.has_hook ? tpl`<span class="muted">configured</span>` : tpl`<span class="muted">none</span>`);
  const category = categoryOf(w, defaultCategoryWatch);
  const cfg = tpl`<div class="watch-grid">
    <div><span class="muted">Scope</span><br><b>${watchScope(w)}</b></div>
    <div><span class="muted">Type</span><br><b>${w.check_type || ""}</b></div>
    <div><span class="muted">Category</span><br>${categoryBadge(category)}</div>
    <div><span class="muted">Interval</span><br><b>${w.interval || ""}</b></div>
    <div><span class="muted">Fires</span><br><b>${polarity}</b></div>
    <div><span class="muted">State</span><br>${watchStateCell(w)}</div>
    <div><span class="muted">Monitoring</span><br>${watchMonitoringCell(w)}</div>
    <div><span class="muted">Configured monitor</span><br><code>${mode}</code></div>
    <div><span class="muted">Last checked</span><br>${watchLastCheckedCell(w)}</div>
    <div><span class="muted">Hook</span><br>${hook}</div>
    <div><span class="muted">Notifies</span><br>${notifiers}</div>
    <div><span class="muted">Dry run</span><br><b>${w.dry_run ? "yes" : "no"}</b></div>
  </div>`;
  const live = tpl`${renderStorageWatch(w.storage)}${renderMeterWatch(w.meter)}${renderWatchReadings(w.readings)}${renderCountHoldersSection(w)}${renderWatchMetricsSection(w)}${w.keeps_sla ? renderSLASection(watchSLAKey(w.name)) : nothing}`;
  const conditions = renderConditionRows(w.conditions || []);
  if (!events || !events.length) return tpl`${cfg}${live}${conditions}<div class="muted">No recent activity.</div>`;
  const rows = events.slice(0, 50).map((e) => {
    const detail = [e.action, e.status].filter(Boolean).join(" ");
    return tpl`<tr>
      <td class="t">${fmtTimeTitled(e.time)}</td>
      <td class="kind kind-${e.kind}">${e.kind}</td>
      <td>${detail ? tpl`<span class="muted">${detail}</span> ` : nothing}${e.message || ""}</td>
    </tr>`;
  });
  return tpl`${cfg}${live}${conditions}<table class="events events-compact-table">
    <caption class="visually-hidden">Recent watch activity</caption>
    <thead><tr><th scope="col">Time</th><th scope="col">Kind</th><th scope="col">Message</th></tr></thead>
    <tbody>${rows}</tbody></table>`;
}

function mountStateClass(state, mounted) {
  if (state === mountStateMounting || state === mountStateUnmounting) return mountStateClasses[state];
  if (mounted) return mountStateClasses[mountStateActive];
  return mountStateClasses[state] || mountStateClasses[mountStateInactive];
}

function mountOperationState(action) {
  return action === actionMount ? mountStateMounting : mountStateUnmounting;
}

function startMountOperation(name, action) {
  liveMountOps.set(name, {
    action,
    state: mountOperationState(action),
    started_at: new Date().toISOString(),
  });
  renderMounts();
}

function finishMountOperation(name) {
  liveMountOps.delete(name);
  renderMounts();
}

function mountOperation(m) {
  if (!m) return null;
  const local = liveMountOps.get(m.name);
  if (local) return local;
  return m.operation || null;
}

function mountBaseStateText(m) {
  const state = String((m && m.state) || "").toLowerCase();
  if (state === mountStateError) return mountStateError;
  if ((m && m.mounted) || state === mountStateActive) return mountStateActive;
  return mountStateInactive;
}

function mountStateText(m) {
  const op = mountOperation(m);
  if (op) return op.state || mountOperationState(op.action);
  return mountBaseStateText(m);
}

function mountStateRank(m) {
  return mountStateRanks[mountStateText(m)] ?? 3;
}

function mountBlockers(m) {
  return Array.isArray(m.blockers) ? m.blockers : [];
}

function mountProcessLabel(p) {
  const pid = Number(p.pid || 0);
  const exe = p.exe || ((p.cmdline || [])[0]) || "unknown";
  return `pid ${pid || "?"} ${exe}`;
}

function mountUsageCell(items, maxRows = 3) {
  if (!items.length) return '<span class="muted">—</span>';
  const shown = items.slice(0, maxRows).map((item) => `<span class="mount-usage-item">${esc(item)}</span>`).join("");
  const extra = items.length > maxRows ? `<span class="muted mount-usage-extra">+${items.length - maxRows} more</span>` : "";
  return `<span title="${esc(items.join("\n"))}">${shown}${extra}</span>`;
}

function mountProcessesCell(m) {
  if (m.blocker_error) return `<span class="bad" title="${esc(m.blocker_error)}">error</span>`;
  return mountUsageCell(mountBlockers(m).map(mountProcessLabel));
}

function mountUsersCell(m) {
  if (m.blocker_error) return '<span class="muted">—</span>';
  return mountUsageCell(mountUserNames(m));
}

function mountUserNames(m) {
  const seen = new Set();
  const users = [];
  for (const p of mountBlockers(m)) {
    const user = p.user || `uid ${p.uid ?? "?"}`;
    if (seen.has(user)) continue;
    seen.add(user);
    users.push(user);
  }
  return users;
}

function mountCategoryCell(category) {
  return `<span class="category-badge" title="${esc(category)}">${esc(category)}</span>`;
}

function mountOperationReason(op) {
  if (!op) return "";
  return op.message || `${op.state || mountOperationState(op.action)} in progress`;
}

function mountPathCell(m) {
  const path = `<code>${esc(m.path || "")}</code>`;
  const op = mountOperation(m);
  if (!op) return path;
  const label = op.state || mountOperationState(op.action);
  const title = mountOperationReason(op);
  return `${path}<span class="mount-operation" title="${esc(title)}">${esc(label)}</span>`;
}

const mountSortKeys = {
  name: (m) => displayName(m).toLowerCase(),
  category: (m) => categoryOf(m, defaultCategoryStorage).toLowerCase(),
  path: (m) => (m.path || "").toLowerCase(),
  mounted: (m) => mountStateRank(m),
  refcount: (m) => numericSortValue(m && m.refcount),
  processes: (m) => mountBlockers(m).length,
  users: (m) => mountUserNames(m).length,
  state: (m) => mountStateRank(m),
};

function setMountSort(key) { toggleSort(mountSort, key, renderMounts); }
function setMountQuery(v) { mountQuery = (v || "").trim().toLowerCase(); renderMounts(); saveUIState(); }
function setMountCategory(v) { mountCategory = v || filterAll; renderMounts(); saveUIState(); }
function setMountGrouped(grouped) { mountGrouped = !!grouped; renderMounts(); saveUIState(); }
function toggleAllMountGroups() {
  toggleAllGroups(sortedCategories((allMounts || []).filter(mountMatches), defaultCategoryStorage), mountCollapsedGroups);
  renderMounts();
  saveUIState();
}
function setMountStatus(v) {
  mountStatus = normalizeMountStatusFilter(v);
  syncFilterButtons("#mount-filters", "mf", mountStatus);
  renderMounts();
  saveUIState();
}

function mountMatches(m) {
  const category = categoryOf(m, defaultCategoryStorage);
  if (mountCategory !== filterAll && category !== mountCategory) return false;
  if (mountStatus !== filterAll && mountStateText(m) !== mountStatus && mountBaseStateText(m) !== mountStatus) return false;
  if (!mountQuery) return true;
  const hay = [
    displayName(m), m.name || "", m.display_name || "", category, m.path || "",
    mountStateText(m), ...mountBlockers(m).map(mountProcessLabel), ...mountUserNames(m),
  ].join(" ").toLowerCase();
  return hay.includes(mountQuery);
}

function renderMountFilterCounts() {
  renderFilterButtonCounts("#mount-filters", stateCounts(allMounts, mountStateText, mountStatusFilterStates));
}

function syncMountCategorySelect() {
  return syncCategorySelect("#mount-category", allMounts || [], defaultCategoryStorage, mountCategory, "all groups");
}

function updateMountSortIndicators() {
  updateSortIndicatorsFor("mi", mountSort, ".mount-table th.sortable[data-mount-sort]", "mountSort");
}

function mountRowHTML(m) {
  const label = esc(m.display_name || m.name);
  const category = categoryOf(m, defaultCategoryStorage);
  const mounted = !!m.mounted;
  const state = mountStateText(m);
  const detail = m.message ? ` title="${esc(m.message)}"` : "";
  const refcount = m.refcounted === false ? '<span class="muted">off</span>' : String(Number(m.refcount || 0));
  const name = esc(m.name || "");
  const actions = mountActionButtons(m, mounted);
  const operation = mountOperation(m);
  const rowClass = operation ? ' class="mount-row-operating"' : "";
  return `<tr id="mount-row-${detailDomKey(m.name || m.path || "mount")}" tabindex="-1"${detail}${rowClass}>
    <td>${label}</td>
    <td>${mountCategoryCell(category)}</td>
    <td>${mountPathCell(m)}</td>
    <td>${mounted ? '<span class="ok">yes</span>' : '<span class="muted">no</span>'}</td>
    <td>${refcount}</td>
    <td class="mount-processes">${mountProcessesCell(m)}</td>
    <td class="mount-users">${mountUsersCell(m)}</td>
    <td><span class="target-state ${mountStateClass(state, mounted)}">${esc(state)}</span></td>
    <td class="actions" data-mount-row="${name}">${actions}</td>
  </tr>`;
}

function renderGroupedMountRows(list) {
  const groups = new Map();
  list.forEach((mount) => {
    const group = categoryOf(mount, defaultCategoryStorage);
    if (!groups.has(group)) groups.set(group, []);
    groups.get(group).push(mount);
  });
  const direction = mountSort.key === "category" ? mountSort.dir : 1;
  return Array.from(groups.entries()).sort((a, b) =>
    a[0].localeCompare(b[0], undefined, { numeric: true, sensitivity: "base" }) * direction
  ).map(([group, mounts]) => {
    const collapsed = mountCollapsedGroups.has(group);
    const first = mounts[0];
    const controls = `mount-row-${detailDomKey(first.name || first.path || "mount")}`;
    const header = `<tr class="group-row"><td colspan="9"><button type="button" class="row-toggle group-toggle" data-group-panel="mount" data-group-name="${esc(group)}" aria-expanded="${collapsed ? domBoolFalse : domBoolTrue}" aria-controls="${controls}" aria-label="${esc(groupToggleAriaLabel(group, mounts.length, collapsed))}"><span class="exp" aria-hidden="true">${collapsed ? "▸" : "▾"}</span>${esc(group)} <span class="muted">${mounts.length}</span></button></td></tr>`;
    return header + (collapsed ? "" : mounts.map(mountRowHTML).join(""));
  }).join("");
}

function renderMounts(mounts) {
  if (mounts) allMounts = mounts;
  scheduleGlobalTargetSync();
  const section = $("#mounts-section");
  const tbody = $("#mount-rows");
  const cnt = $("#mounts-count");
  const filterCount = $("#mount-filter-count");
  if (!section || !tbody) return;
  const total = (allMounts || []).length;
  if (!panelShell({ section, count: cnt, filterCount, total, onEmpty: updateSectionNav })) return;
  mountCategory = syncMountCategorySelect();
  renderMountFilterCounts();
  const list = (allMounts || []).filter(mountMatches);
  if (mountSort.key && mountSortKeys[mountSort.key]) {
    sortedBy(list, mountSort, mountSortKeys, "name");
  }
  updateMountSortIndicators();
  const groups = sortedCategories(list, defaultCategoryStorage);
  mountCollapsedGroups.forEach((group) => { if (!groups.includes(group)) mountCollapsedGroups.delete(group); });
  if (groups.length < 2) mountGrouped = false;
  updateGroupButtons("mount", mountGrouped, groups, mountCollapsedGroups, "mount units", "group");
  if (filterCount) filterCount.textContent = (mountQuery || mountStatus !== filterAll || mountCategory !== filterAll) ? `showing ${list.length} of ${total}` : "";
  const rows = mountGrouped ? renderGroupedMountRows(list) : list.map(mountRowHTML).join("");
  // setHTMLIfChanged, not a bare write: an unchanged poll must not destroy and
  // recreate every row, which would drop the focus of whoever is tabbing
  // through the action buttons and clear any text selection.
  setHTMLIfChanged(tbody, rows || `<tr><td colspan="9" class="muted">No mount units match the filter.</td></tr>`);
  updateSectionNav();
}

function mountActionButtons(m, mounted) {
  if (!me.can_act) return '<span class="muted">read-only</span>';
  const name = esc(m.name || "");
  const label = esc(m.display_name || m.name || m.path || "mount");
  const operation = mountOperation(m);
  const operationReason = mountOperationReason(operation);
  const operationDisabled = operation ? " disabled" : "";
  if (!mounted) {
    return `<button class="icon-btn" data-mount="${name}" data-mount-action="${actionMount}" aria-label="Mount ${label}" title="${operation ? esc(operationReason) : `Mount ${label}`}"${operationDisabled}><span aria-hidden="true">▶</span></button>`;
  }
  const disabledReason = mountUmountDisabledReason(m);
  const hintId = `mount-${detailDomKey(m.name || m.path || "mount")}-umount-hint`;
  const hint = disabledReason ? `<span id="${hintId}" class="visually-hidden">${esc(disabledReason)}</span>` : "";
  const disabledAttrs = disabledReason ? ` disabled aria-describedby="${hintId}"` : operationDisabled;
  const primaryTitle = disabledReason ? esc(disabledReason) : (operation ? esc(operationReason) : `Unmount ${label}`);
  const alertTitle = disabledReason ? esc(disabledReason) : (operation ? esc(operationReason) : `Alert users blocking ${label}`);
  return hint +
    `<button class="icon-btn" data-mount="${name}" data-mount-action="${actionUmount}" aria-label="Unmount ${label}" title="${primaryTitle}"${disabledAttrs}><span aria-hidden="true">⏏</span></button>` +
    `<button class="icon-btn" data-mount="${name}" data-mount-action="${actionAlert}" aria-label="Alert users blocking ${label}" title="${alertTitle}"${disabledAttrs}><span aria-hidden="true">!</span></button>`;
}

function mountUmountDisabledReason(m) {
  if (!m || m.can_umount !== false) return "";
  return m.umount_disabled_reason || "root filesystem cannot be unmounted";
}

// panelShell applies the contract every list panel follows at its edges: an
// empty panel hides its section and clears the counters (and the body, when
// the caller owns one through lit); a populated one shows the section and
// prints "(total)". Returns true when the caller should render content.
// onEmpty carries the one legitimate per-panel extra (updateSectionNav for
// panels the section nav lists).
function panelShell({ section, tbody, count, filterCount, total, onEmpty }) {
  if (!total) {
    setPanelVisible(section, false);
    if (count) count.textContent = "";
    if (filterCount) filterCount.textContent = "";
    if (tbody) litRender(nothing, tbody);
    if (onEmpty) onEmpty();
    return false;
  }
  setPanelVisible(section, true);
  if (count) count.textContent = `(${total})`;
  return true;
}

function renderNotifiers(notifiers) {
  const section = $("#notifiers-section");
  const tbody = $("#notifier-rows");
  const cnt = $("#notifiers-count");
  if (!section || !tbody) return;
  if (!panelShell({ section, count: cnt, total: (notifiers || []).length, onEmpty: updateSectionNav })) return;
  const rows = notifiers.map((n) => {
    const enabled = n.enabled !== false;
    const state = enabled ? "enabled" : "disabled";
    const cls = enabled ? "state-monitored" : "state-disabled";
    const dest = n.summary ? esc(n.summary) : '<span class="muted">—</span>';
    const used = Number(n.used_by || 0);
    const watches = used ? String(used) : '<span class="muted">—</span>';
    const test = enabled && me.can_act
      ? `<button class="icon-btn" data-notifier-test="${esc(n.name)}" aria-label="Send test notification to ${esc(n.name)}" title="Send test notification to ${esc(n.name)}"><span aria-hidden="true">▶</span></button>`
      : '<span class="muted">—</span>';
    return `<tr><td>${esc(n.name)}</td><td>${esc(n.type)}</td><td class="muted">${dest}</td><td>${watches}</td><td class="${cls}">${state}</td><td class="actions">${test}</td></tr>`;
  });
  setHTMLIfChanged(tbody, rows.join("") || `<tr><td colspan="6" class="muted">No notifiers.</td></tr>`);
  updateSectionNav();
}

// setTextOrDash fills one element's text, dashing absent values; shared by the
// Daemon info panel's static and runtime halves.
function setTextOrDash(id, val) {
  const el = $(id);
  if (el) el.textContent = val || "—";
}

function renderDaemon(info) {
  if (!info) return;
  rememberDashboardHostname(info.hostname);
  const set = setTextOrDash;
  const hostType = hostTypeDisplay(info.host_type);
  set("#daemon-backend", info.backend);
  set("#daemon-host-type", hostType.label);
  const hostTypeEl = $("#daemon-host-type");
  if (hostTypeEl) hostTypeEl.title = hostType.title;
  set("#daemon-config", info.config_path);
  set("#daemon-runtime", info.runtime_dir);
  set("#daemon-state", info.state_dir);
  set("#engine-interval", info.interval);
  set("#engine-max-checks", info.max_parallel_checks);
  set("#engine-default-timeout", info.default_timeout);
  set("#engine-op-timeout", info.operation_timeout);
  set("#engine-startup-delay", info.startup_delay);
}

function hostTypeDisplay(hostType) {
  if (!hostType) return { label: "", title: "" };
  const kind = (hostType.kind || "").replace(/_/g, " ");
  const label = hostType.label || hostType.platform || kind;
  const detail = [];
  if (kind) detail.push(kind);
  if (hostType.platform) detail.push(hostType.platform);
  if (hostType.detail) detail.push(hostType.detail);
  return { label, title: detail.join(" · ") };
}

function lockName(l) {
  return l.name || "(default)";
}

function lockStateHTML(l) {
  const cls = l.state === lockStateActive ? "bad" : (l.state === lockStateStale ? "inactive" : "muted");
  return tpl`<span class="${cls}">${l.state || ""}</span>`;
}

function lockTTL(l) {
  if (!l.expires_at) return tpl`<span class="muted">—</span>`;
  if (l.ttl_remaining_seconds > 0) return tpl`<span title="${fmtTime(l.expires_at)}">${fmtDuration(l.ttl_remaining_seconds)}</span>`;
  return tpl`<span class="muted" title="${fmtTime(l.expires_at)}">expired</span>`;
}

function lockOwner(l) {
  if (!l.owner_pid) return tpl`<span class="muted">none</span>`;
  const cls = l.owner_status === lockOwnerStatusLive ? "ok" : (l.owner_status === lockOwnerStatusStale ? "inactive" : "muted");
  const reason = l.stale_reason ? ` · ${l.stale_reason}` : "";
  return tpl`<span class="${cls}">${l.owner_pid}</span> <span class="muted">${(l.owner_status || "") + reason}</span>`;
}

function lockCreated(l) {
  if (l.created_age_seconds > 0) return tpl`<span title="${fmtTime(l.created_at)}">${fmtDuration(l.created_age_seconds)} ago</span>`;
  if (l.created_at) return tpl`<span title="${fmtTime(l.created_at)}">${fmtAge(l.created_at)}</span>`;
  return tpl`<span class="muted">—</span>`;
}

function lockBlocks(l) {
  const actions = l.blocked_actions || [];
  return actions.length ? actions.join(" ") : tpl`<span class="muted">none</span>`;
}

function lockReleaseHintId(l) {
  const svc = (l.service || "svc").replace(/[^a-zA-Z0-9._-]+/g, "-");
  const name = (l.name || "default").replace(/[^a-zA-Z0-9._-]+/g, "-");
  return `lock-${svc}-${name}-release-hint`;
}

function lockReleaseLabel(l) {
  const svc = l.service || "";
  const name = lockName(l);
  return svc ? `Release lock ${svc}:${name}` : `Release lock ${name}`;
}

function lockReleaseDisabled(l) {
  if (!me.can_act || !l) return true;
  return !l.releaseable;
}

function lockReleaseDisabledReason(l) {
  if (!me.can_act) return "";
  if (l.releaseable) return "";
  if (l.state === lockStateActive) return "lock is still active";
  return "lock cannot be released";
}

function lockReleaseButton(l) {
  if (!me.can_act) return nothing;
  const disabled = lockReleaseDisabled(l);
  const reason = lockReleaseDisabledReason(l);
  const hint = disabled && reason
    ? tpl`<span id="${lockReleaseHintId(l)}" class="visually-hidden">${reason}</span>`
    : nothing;
  const describedBy = disabled && reason ? lockReleaseHintId(l) : nothing;
  return tpl`${hint}<button class="danger-btn" ?disabled=${disabled} data-lock-release="1" data-lock-service="${l.service || ""}" data-lock-name="${l.name || ""}" aria-label="${lockReleaseLabel(l)}" aria-describedby="${describedBy}">release</button>`;
}

function lockServiceLink(l) {
  const svc = l.service || "";
  if (!svc) return tpl`<span class="muted">—</span>`;
  return tpl`<button type="button" class="name row-toggle" data-service-open="${svc}" aria-label="Open service ${svc}">${svc}</button>`;
}

async function releaseLock(service, name) {
  const label = name ? `${service}.${name}` : service;
  if (!(await promptConfirm({
    title: `Release lock ${label}?`,
    message: `Release inactive lock "${label}"?`,
    okLabel: "release",
    danger: true,
  }))) return;
  setStatus("");
  const qs = name ? `?${apiQueryName}=${encodeURIComponent(name)}` : "";
  try {
    const res = await fetch(lockReleaseAPI(service, qs), targetPostOptions());
    await jsonOrThrow(res);
    setStatus(`released lock ${label}`, feedbackStatusOK);
    await load();
  } catch (e) {
    setStatus(`release ${label}: ${e.message}`, feedbackStatusErr);
  }
}

function renderLocks(locks) {
  latestLocks = locks || [];
  const section = $("#locks-section");
  const tbody = $("#locks-rows");
  const cnt = $("#locks-count");
  if (!section || !tbody) return;
  if (!locks || locks.length === 0) {
    setPanelVisible(section, false);
    if (cnt) cnt.textContent = "";
    renderAttention();
    updateSectionNav();
    return;
  }
  setPanelVisible(section, true);
  if (cnt) cnt.textContent = `(${locks.length})`;
  const rows = locks.map((l) => {
    return tpl`<tr>
      <td>${lockServiceLink(l)}</td>
      <td>${lockName(l)}</td>
      <td>${lockStateHTML(l)}</td>
      <td>${lockTTL(l)}</td>
      <td>${lockOwner(l)}</td>
      <td>${lockCreated(l)}</td>
      <td>${lockBlocks(l)}</td>
      <td>${l.reason || l.stale_reason || ""}</td>
      <td>${lockReleaseButton(l)}</td>
    </tr>`;
  });
  litRender(rows, tbody);
  renderAttention();
  updateSectionNav();
}

function renderActivity(sum) {
  if (!sum) return;
  latestActivity = sum;
  renderAttention();
}

function attnAriaLabel(it) {
  const parts = [it.title];
  if (it.detail) parts.push(it.detail);
  parts.push(`Open ${panelTargetLabel(it.target)}`);
  return parts.join(". ");
}

function panelTargetLabel(target) {
  switch (target) {
    case "failed-services": return "service targets panel, failed filter";
    case "starting-services": return "service targets panel, starting filter";
    case "collecting-services": return "service targets panel, collecting filter";
    case "warning-services": return "service targets panel, warning filter";
    case "monitored-services": return "service targets panel, monitored filter";
    case "failed-watches": return "watches panel, failed filter";
    case "starting-watches": return "watches panel, starting filter";
    case "warning-watches": return "watches panel, warning filter";
    case "stale-watches": return "watches panel, stale filter";
    case "failed-apps": return "applications panel, failed filter";
    case "starting-apps": return "applications panel, starting filter";
    case "locks-section": return "runtime locks panel";
    case "containers-section": return "containers panel";
    case "vms-section": return "virtual machines panel";
    case "daemon-section": return "daemon panel";
    case "watches-section": return "watches panel";
    case "services-section":
    default: return "services panel";
  }
}

function tileAriaLabel(label, valueText, sub, target) {
  const parts = [`${label}: ${valueText}`];
  if (sub) parts.push(sub);
  parts.push(`Open ${panelTargetLabel(target)}`);
  return parts.join(". ");
}

function tileGaugeId(key) {
  return `tile-${key}-gauge`;
}

// renderOverview fills the at-a-glance tile band under the topbar: one tile per
// vital sign, colored by health, each clickable to jump to its panel. load()
// firstMatch returns the value paired with the first truthy condition, or the
// fallback when none match. The overview picks a tile's class, subtitle and
// click target from ordered priority lists; written as nested ternaries those
// chains are the same logic but unreadable, and they dominate this function's
// measured complexity.
function firstMatch(pairs, fallback) {
  const hit = pairs.find(([when]) => when);
  return hit ? hit[1] : fallback;
}

// settlingSub renders a tile's settling subtitle: its own starting count when
// it has one, otherwise the other panels still settling, in order.
function settlingSub(ownCount, others) {
  if (ownCount) return `${ownCount} starting`;
  return others.filter(([when]) => when).map(([, label]) => label).join(" · ");
}

// overviewTile renders one clickable band tile. Module scope so renderOverview
// stays within its size budget; the band's default target is passed in.
const overviewTile = (opts, defaultTarget) => tpl`
  <button class="tile ${opts.cls || ""}" data-panel-target="${opts.target || defaultTarget}" aria-label="${opts.ariaLabel || opts.label}" aria-describedby="${opts.describedBy || nothing}">
    <span class="t-label">${opts.label}</span>
    <div class="t-value">${opts.value}</div>
    <div class="t-sub">${opts.sub || ""}</div>
    ${opts.extra || nothing}
  </button>`;

// overviewInventory derives the shared counts once for all non-host tiles.
// renderOverview receives the same burst snapshot as renderStatus, so none of
// these helpers makes an extra request.
function overviewInventory(ctx) {
  const source = ctx || {};
  const ready = source.ready || {};
  const live = source.live || {};
  const mon = source.mon || {};
  const locks = source.locks || [];
  const hostMetrics = source.hostMetrics || [];
  const svcs = allServices || [];
  const enabled = svcs.filter((s) => s.enabled);
  const failedSvcs = svcs.filter((s) => serviceDisplayState(s) === targetStateFailed);
  const startingSvcs = svcs.filter((s) => serviceDisplayState(s) === targetStateStarting);
  const collectingSvcs = svcs.filter((s) => serviceDisplayState(s) === targetStateCollecting);
  // Warning services need operator attention: either a process-attribution gap
  // or a failed init unit whose verified workload remains healthy.
  const warningSvcs = svcs.filter((s) => serviceDisplayState(s) === targetStateWarning);
  const activeSvcs = enabled.filter((s) => overviewActiveServiceStates.includes(serviceDisplayState(s)));
  const monitoredSvcs = enabled.filter((s) => serviceDisplayState(s) === targetStateMonitored);
  const watches = allWatches || [];
  const enabledWatches = watches.filter((w) => w && w.enabled);
  const failedWatches = watches.filter((w) => watchStateText(w) === targetStateFailed);
  const warningWatches = watches.filter(isWatchWarning);
  const staleWatches = watches.filter(isWatchSampleStale);
  const startingWatches = watches.filter((w) => watchStateText(w) === targetStateStarting);
  const startingApps = (allApps || []).filter((a) => appStateText(a) === targetStateStarting);
  const daemonStarting = ready && ready.status === daemonStatusStarting && ready.ready === false;
  const activeLocks = (locks || []).filter((l) => l.state === lockStateActive);
  const failedApps = (allApps || []).filter((a) => appStateText(a) === targetStateFailed);
  return {
    ready, live, mon, hostMetrics, enabled, failedSvcs, startingSvcs, collectingSvcs,
    warningSvcs, activeSvcs, monitoredSvcs, watches, enabledWatches, failedWatches,
    warningWatches, staleWatches, startingWatches, startingApps, daemonStarting, activeLocks, failedApps,
  };
}

function overviewServiceTileOptions(data, defaultServiceTarget) {
  const {
    mon, enabled, failedSvcs, startingSvcs, collectingSvcs, warningSvcs, activeSvcs,
    monitoredSvcs, watches, enabledWatches, failedWatches, warningWatches, staleWatches,
    startingWatches, startingApps, daemonStarting, activeLocks, failedApps,
  } = data;
  const alerts = failedSvcs.length + failedWatches.length + failedApps.length + activeLocks.length;
  const settling = daemonStarting || startingSvcs.length > 0 || startingWatches.length > 0 || startingApps.length > 0;
  const servicesSettlingText = settlingSub(startingSvcs.length, [
    [daemonStarting, "daemon starting"],
    [startingWatches.length, `${startingWatches.length} watch starting`],
    [startingApps.length, `${startingApps.length} app starting`],
  ]);
  const watchesSettlingText = settlingSub(startingWatches.length, [
    [daemonStarting, "daemon starting"],
    [startingSvcs.length, `${startingSvcs.length} svc starting`],
    [startingApps.length, `${startingApps.length} app starting`],
  ]);
  const servicesTarget = firstMatch([
    [failedSvcs.length, "failed-services"],
    [startingSvcs.length || daemonStarting, "starting-services"],
    [collectingSvcs.length, "collecting-services"],
    [warningSvcs.length, "warning-services"],
    [startingWatches.length, "starting-watches"],
    [startingApps.length, "starting-apps"],
  ], defaultServiceTarget);
  const watchesTarget = firstMatch([
    [failedWatches.length, "failed-watches"],
    [warningWatches.length, "warning-watches"],
    [staleWatches.length, "stale-watches"],
    [startingWatches.length, "starting-watches"],
    [startingApps.length && !startingSvcs.length && !daemonStarting, "starting-apps"],
    [settling, "starting-services"],
  ], "watches-section");

  const tiles = [];
  const servicesSub = firstMatch([
    [failedSvcs.length, `${failedSvcs.length} failed`],
    [servicesSettlingText, servicesSettlingText],
    [collectingSvcs.length, `${collectingSvcs.length} collecting`],
    [warningSvcs.length, `${warningSvcs.length} need attention`],
    [enabled.length === 0, "none enabled"],
  ], "all active");
  tiles.push({
    label: "Services active",
    value: tpl`${activeSvcs.length}<small> / ${enabled.length}</small>`,
    cls: firstMatch([
      [failedSvcs.length, "t-crit"],
      [collectingSvcs.length || warningSvcs.length, "t-warn"],
      [settling, ""],
      [enabled.length, "t-ok"],
    ], ""),
    sub: servicesSub,
    target: servicesTarget,
    ariaLabel: tileAriaLabel("Services active", `${activeSvcs.length} of ${enabled.length}`, servicesSub, servicesTarget),
  });
  if (watches.length) {
    const watchesSub = firstMatch([
      [failedWatches.length, `${failedWatches.length} firing`],
      [warningWatches.length, `${warningWatches.length} warning`],
      [staleWatches.length, `${staleWatches.length} stale`],
      [watchesSettlingText, watchesSettlingText],
    ], "quiet");
    // A single predicate over the enabled set: subtracting the failed/warning/stale
    // list lengths (which span all watches and can overlap) could go negative.
    const watchesUp = enabledWatches.filter((w) => watchStateText(w) !== targetStateFailed && !isWatchWarning(w) && !isWatchSampleStale(w)).length;
    tiles.push({
      label: "Watches",
      value: tpl`${watchesUp}<small> / ${enabledWatches.length}</small>`,
      cls: failedWatches.length ? "t-crit" : ((warningWatches.length || staleWatches.length) ? "t-warn" : (settling ? "" : "t-ok")),
      sub: watchesSub,
      target: watchesTarget,
      ariaLabel: tileAriaLabel("Watches", `${watchesUp} of ${enabledWatches.length}`, watchesSub, watchesTarget),
    });
  }
  const alertsTarget = firstMatch([
    [failedSvcs.length, "failed-services"],
    [failedWatches.length, "failed-watches"],
    [failedApps.length, "failed-apps"],
    [activeLocks.length, "locks-section"],
  ], defaultServiceTarget);
  const alertsSub = alerts
    ? [failedSvcs.length && `${failedSvcs.length} svc`, failedWatches.length && `${failedWatches.length} watch`, failedApps.length && `${failedApps.length} app`, activeLocks.length && `${activeLocks.length} lock`].filter(Boolean).join(" · ")
    : "nothing on fire";
  // Until the first complete snapshot (watches/apps still loading) the alert
  // total is incomplete, so show a neutral loading dash rather than a wrong
  // count that jumps a moment later.
  tiles.push({
    label: "Alerts",
    value: firstSnapshotDone ? String(alerts) : overviewLoadingValue,
    cls: !firstSnapshotDone ? "" : (alerts ? "t-crit" : "t-ok"),
    sub: firstSnapshotDone ? alertsSub : overviewLoadingText,
    target: alertsTarget,
    ariaLabel: firstSnapshotDone ? tileAriaLabel("Alerts", String(alerts), alertsSub, alertsTarget) : "Alerts loading",
  });
  const monitoredTarget = firstMatch([
    [collectingSvcs.length && !failedSvcs.length, "collecting-services"],
    [settling && !failedSvcs.length, servicesTarget],
    [warningSvcs.length && !failedSvcs.length, "warning-services"],
    [monitoredSvcs.length, "monitored-services"],
  ], defaultServiceTarget);
  const monitoredSub = firstMatch([
    [collectingSvcs.length, `${collectingSvcs.length} collecting`],
    [settling && !failedSvcs.length, servicesSettlingText || "settling"],
    [warningSvcs.length, `${warningSvcs.length} need attention`],
  ], "");
  if (enabled.length || (mon && mon.total != null)) {
    tiles.push({
      label: "Monitored",
      value: tpl`${monitoredSvcs.length}<small> / ${enabled.length}</small>`,
      cls: firstMatch([
        [collectingSvcs.length || warningSvcs.length, "t-warn"],
        [settling && !failedSvcs.length, ""],
        [enabled.length && monitoredSvcs.length === enabled.length, "t-ok"],
      ], ""),
      sub: monitoredSub,
      target: monitoredTarget,
      ariaLabel: tileAriaLabel("Monitored", `${monitoredSvcs.length} of ${enabled.length}`, monitoredSub, monitoredTarget),
    });
  }
  return tiles;
}

// overviewUsedFreeSub renders the volume-style "X used · Y free" line for a
// usage metric carrying its capacity (total bytes).
function overviewUsedFreeSub(metric) {
  if (!metric.total) return "";
  const used = metric.absolute || 0;
  return `${fmtBytes(used)} used · ${fmtBytes(Math.max(metric.total - used, 0))} free`;
}

function overviewSwapLevel(pct) {
  if (pct >= overviewSwapCriticalPct) return "t-crit";
  if (pct >= overviewSwapWarnPct) return "t-warn";
  return "";
}

const overviewHostUsageDescriptors = [
  { metric: hostMetricTotalCPU, gauge: metricNameCPU, label: "Host CPU" },
  { metric: hostMetricTotalMemory, gauge: "mem", label: "Host memory", capacity: true },
  { metric: hostMetricTotalSwap, gauge: "swap", label: "Host swap", capacity: true, requireTotal: true, level: overviewSwapLevel },
];

function overviewHostUsageTile(metric, descriptor, loading) {
  const pct = pctClamp(metric.percent || 0);
  const sub = descriptor.capacity ? overviewUsedFreeSub(metric) : "";
  const gaugeId = tileGaugeId(descriptor.gauge);
  const cls = !loading && descriptor.level ? descriptor.level(pct) : "";
  return {
    label: descriptor.label,
    value: loading ? overviewLoadingValue : tpl`${fmtNum(pct, 2)}<small>${metricUnitPercent}</small>`,
    sub: loading ? overviewLoadingText : sub,
    cls,
    extra: loading ? nothing : usageBar(pct, fmtPct(pct), gaugeId),
    target: overviewDaemonPanel,
    ariaLabel: loading
      ? `${descriptor.label} loading`
      : tileAriaLabel(descriptor.label, fmtPct(pct), sub, overviewDaemonPanel),
    describedBy: loading ? nothing : gaugeId,
  };
}

function overviewHostTileOptions(data) {
  const metrics = new Map((data.hostMetrics || []).map((metric) => [metric.name, metric]));
  const tiles = [];
  // Until the first complete snapshot the host readings may be missing or a
  // pre-sample zero, so show a neutral loading dash — same as the Alerts tile —
  // rather than a value that corrects a moment later.
  const loading = !firstSnapshotDone;
  for (const descriptor of overviewHostUsageDescriptors) {
    const metric = metrics.get(descriptor.metric);
    if (!metric || (descriptor.requireTotal && !metric.total)) continue;
    tiles.push(overviewHostUsageTile(metric, descriptor, loading));
  }
  const load = metrics.get(hostMetricLoad1);
  if (load && load.absolute != null) {
    // load.total carries the logical CPU count and load.percent the saturation
    // (load1/CPUs), so the tile gets the same bar as cpu/mem/swap. >100% means
    // the run queue exceeds the cores.
    const hasCap = load.total > 0;
    const pct = hasCap ? pctClamp(load.percent || 0) : 0;
    const uptime = fmtDuration(data.live.uptime_seconds);
    const loadSub = hasCap ? `${fmtNum(load.total, 0)} CPUs · ${fmtPct(load.percent)}` : (uptime ? `up ${uptime}` : "");
    const gaugeId = hasCap ? tileGaugeId("load") : nothing;
    tiles.push({
      label: "Load 1m",
      value: loading ? overviewLoadingValue : fmtNum(load.absolute, 2),
      sub: loading ? overviewLoadingText : loadSub,
      cls: loading ? "" : (hasCap ? (pct >= percentMax ? "t-crit" : (pct >= loadWarnPct ? "t-warn" : "")) : ""),
      extra: loading || !hasCap ? nothing : usageBar(pct, fmtPct(pct), gaugeId),
      target: overviewDaemonPanel,
      ariaLabel: loading ? "Load 1m loading" : tileAriaLabel("Load 1m", fmtNum(load.absolute, 2), loadSub, overviewDaemonPanel),
      describedBy: loading ? nothing : gaugeId,
    });
  }
  return tiles;
}

function renderOverview(ctx) {
  const band = $("#overview");
  if (!band) return;
  const data = overviewInventory(ctx);
  const defaultTarget = defaultServicePanelTarget();
  const options = [...overviewServiceTileOptions(data, defaultTarget), ...overviewHostTileOptions(data)];
  const tiles = options.map((opts) => overviewTile(opts, defaultTarget));
  litRender(tiles, band);
}

// responseGeneration returns the daemon configuration generation attached to
// a read response. Older/test backends omit it, which keeps their existing
// behaviour while reloadable sermod instances get the stronger consistency
// check below.
function responseGeneration(res) {
  const generation = Number(res.headers.get(apiHeaderGeneration));
  return Number.isSafeInteger(generation) && generation > 0 ? generation : 0;
}

// fetchPinned fetches one dashboard sub-resource pinned to a configuration
// generation: a response from another generation triggers the full reload
// (unless the caller suppresses it) and reports stale, so no panel ever mixes
// two configurations. Callers keep their own ok/body policy — some tolerate an
// HTTP error, some throw — but the generation contract has exactly one owner.
async function fetchPinned(url, generation, { reload = true } = {}) {
  const res = await fetch(url);
  if (generationMismatch(res, generation)) {
    if (reload) load();
    return { stale: true, res };
  }
  return { stale: false, res };
}

function generationMismatch(res, expectedGeneration) {
  const actualGeneration = responseGeneration(res);
  return !!(expectedGeneration && actualGeneration && actualGeneration !== expectedGeneration);
}

function sharedBackendGeneration(results) {
  const generations = [...new Set(results.map((result) => result.generation).filter(Boolean))];
  return { generation: generations[0] || 0, mismatch: generations.length > 1 };
}

// getJSONResult keeps failure information next to the fallback value so the
// dashboard can retain the last panel render without claiming a full refresh.
// When expectedGeneration is set, data produced after a daemon reload is kept
// out of the current render and the next queued refresh obtains one view.
async function getJSONResult(url, dflt, expectedGeneration = 0) {
  try {
    const r = await fetch(url);
    if (redirectToLogin(r)) return { ok: false, data: dflt };
    const generation = responseGeneration(r);
    if (generationMismatch(r, expectedGeneration)) {
      return { ok: false, data: dflt, generation, generationMismatch: true };
    }
    return r.ok ? { ok: true, data: await r.json(), generation } : { ok: false, data: dflt, generation };
  } catch (_) {
    return { ok: false, data: dflt };
  }
}

// fetchReadyReport loads GET /readyz?verbose and parses the JSON body even when
// the probe returns 503 (starting / shutting_down), so the header status line
// keeps showing the daemon lifecycle state.
async function fetchReadyReportResult() {
  try {
    const r = await fetch(readyVerbosePath);
    const data = await r.json();
    const validStatus = r.ok || r.status === httpStatusServiceUnavailable;
    const generation = responseGeneration(r);
    return (validStatus && data && typeof data === "object")
      ? { ok: true, data, generation } : { ok: false, data: {}, generation };
  } catch (_) {
    return { ok: false, data: {} };
  }
}

// setHTMLIfChanged skips DOM writes (and SR re-announcements) when a live
// region's markup is unchanged — routine auto-refresh cycles often repeat.
function setHTMLIfChanged(el, html) {
  if (!el) return;
  if (el.innerHTML !== html) el.innerHTML = html;
}

function panelVisible(el) {
  return el && !el.classList.contains("panel-hidden");
}

function setPanelVisible(el, show) {
  if (!el) return;
  el.classList.toggle("panel-hidden", !show);
}

function updateTopbarHeight() {
  const topbar = $("#topbar");
  if (!topbar) return;
  const height = Math.ceil(topbar.getBoundingClientRect().height);
  document.documentElement.style.setProperty("--topbar-h", `${height}px`);
}

function sectionNavCountText(text) {
  const trimmed = (text || "").trim();
  if (!trimmed) return "";
  const paren = trimmed.match(/^\(([^)]+)\)$/);
  if (paren) return paren[1];
  const leading = trimmed.match(/^(\d+)/);
  if (leading) return leading[1];
  return trimmed;
}

function updateSectionNav() {
  const nav = $("#section-nav");
  if (!nav) return;
  let visible = 0;
  nav.querySelectorAll("[data-panel-target]").forEach((btn) => {
    const target = btn.getAttribute("data-panel-target") || "";
    const panel = target ? document.getElementById(target) : null;
    const hidden = !panel || panel.classList.contains("panel-hidden");
    btn.classList.toggle("nav-hidden", hidden);
    if (!hidden) visible++;

    const countID = btn.getAttribute("data-count-source") || "";
    const countSource = countID ? document.getElementById(countID) : null;
    const count = countSource ? sectionNavCountText(countSource.textContent) : "";
    const countEl = btn.querySelector("[data-nav-count]");
    if (countEl) countEl.textContent = count;
  });
  nav.classList.toggle("nav-hidden", visible === 0);
  updateTopbarHeight();
}

function renderStatus(ctx) {
  const bar = $("#statusbar");
  if (!bar) return;
  try {
    const { ready, live, mon, locks, daemon, hostMetrics } = ctx || {};
    latestReady = ready || {};
    latestLocks = locks || [];
    latestHostMetrics = hostMetrics || [];
    if (daemon && daemon.hostname) rememberDashboardHostname(daemon.hostname);

    // System-status line (line 2): host identity + detected backend + OS + live readings.
    const sys = $("#system-status");
    if (sys) {
      // Each reading carries a key so the compact (mobile) layout can drop the
      // host identity only. The live cpu/mem/swap/load readings live in the
      // overview tiles (Host CPU/Memory/Swap/Load) — repeating them here was
      // redundant, so they are intentionally not added.
      const sp = [];
      if (daemon.hostname) sp.push(["host", `host: <b>${esc(daemon.hostname)}</b>`]);
      const hostType = hostTypeDisplay(daemon.host_type);
      if (hostType.label) {
        const title = hostType.title ? ` title="${esc(hostType.title)}"` : "";
        sp.push(["type", `type: <b${title}>${esc(hostType.label)}</b>`]);
      }
      if (ready.backend) sp.push(["backend", `backend: <b>${esc(ready.backend)}</b>`]);
      if (daemon.os) sp.push(["os", `OS: <b>${esc(daemon.os)}</b>`]);
      setHTMLIfChanged(sys, sp.map(([k, s]) => `<span class="stat stat-${k}">${s}</span>`).join(""));
    }

    // Services active and Monitored each have their own overview tile, so they
    // are not repeated here. The header keeps only readings without a tile:
    // watches, interactive sessions and locks.
    const parts = [];
    parts.push(["watches", `watches: <b>${ready.watches || 0}</b>`]);
    if (daemon.sessions) {
      const consoleSessions = Number(daemon.sessions.console) || 0;
      const sshSessions = Number(daemon.sessions.ssh) || 0;
      parts.push(["sessions", `sessions (console/SSH): <b>${consoleSessions}/${sshSessions}</b>`]);
    } else if (daemon.active_users != null) {
      parts.push(["users", `users: <b>${daemon.active_users || 0}</b>`]);
    }
    const activeLocks = (locks || []).filter(l => l.state === lockStateActive).length;
    if (activeLocks > 0 || (locks || []).length > 0) {
      let lockStr = `locks: <b>${activeLocks}</b>`;
      if (activeLocks < (locks || []).length) lockStr += `/${(locks || []).length}`;
      if (activeLocks > 0) lockStr += ` <span class="muted">(active)</span>`;
      parts.push(["locks", lockStr]);
    }
    // Host uptime and daemon lifecycle status, as separate keyed readings. On
    // phones the status text is dropped — the brand dot next to "SERMO" already
    // shows overall health by color — leaving just host and uptime.
    const hostUp = fmtDuration(daemon.host_uptime_seconds);
    const statusText = ready.status || (ready.ready ? healthStatusOK : "");
    const statusCls = ready.panic ? "status-panic" : (statusText === daemonStatusStarting ? "status-starting" : (ready.ready ? healthStatusOK : "inactive"));
    // Label the healthy daemon state "running", not "ok": "ok" reads as "all
    // services fine", but this line only reflects the daemon's own lifecycle
    // (service health is shown by the brand dot and the Alerts tile).
    const statusDisplay = statusText === healthStatusOK ? "running" : statusText;
    const statusLabel = statusText === daemonStatusStarting && ready.message
      ? `${esc(statusText)} <span class="muted">(${esc(ready.message)})</span>`
      : esc(statusDisplay || "—");
    const statItems = parts.map(([k, p]) => `<span class="stat stat-${k}">${p}</span>`);
    statItems.push(`<span class="stat stat-uptime">uptime: <b>${esc(hostUp || "—")}</b></span>`);
    statItems.push(`<span class="stat stat-status">status: <span class="${statusCls}">${statusLabel}</span></span>`);
    setHTMLIfChanged(bar, statItems.join(""));
    updatePanicView(ready.panic);
    renderOverview({ ready, live, mon, locks, hostMetrics });
    updateSectionNav();

    // Also populate the runtime part of the Daemon info panel
    const set = setTextOrDash;
    if (live.started_at) set("#daemon-started", live.started_at);
    set("#daemon-uptime", fmtDuration(live.uptime_seconds));
    if (live.go) set("#daemon-go", live.go);
    if (ready.status) {
      const cls = ready.panic ? "status-panic" : (ready.status === daemonStatusStarting ? "status-starting" : (ready.ready ? healthStatusOK : "inactive"));
      const el = $("#daemon-ready");
      if (el) {
        el.textContent = ready.status;
        el.className = cls;
      }
    }
    renderAttention();
  } catch {
    bar.textContent = "status unavailable";
  }
}

async function act(name, action) {
  let noCascade = false;
  if (isServicePreflightAction(action) && !(await confirmAction(name, action))) return;
  if (isServiceCascadeAction(action)) {
    noCascade = confirmNoCascade;
  }
  confirmNoCascade = false;
  const toggleKey = acquireMonitorToggle(expansionPrefixService, name, action);
  if (toggleKey === null) return;
  const tracked = isTrackedOperation(action);
  try {
    // Everything after the guard-add runs inside the try so a throw (even from
    // a render) cannot strand the key and leave the button disabled for good.
    if (toggleKey) renderServices();
    setStatus("");
    if (tracked) beginOperation(name, action);
    const q = noCascade ? `?${apiQueryNoCascade}=${queryBoolOne}` : "";
    const res = await fetch(serviceAPI(name, apiActionSuffix(action, q)), targetPostOptions());
    const body = await jsonOrThrow(res);
    if (tracked) finishOperation(name, true, body.message || "operation completed");
  } catch (e) {
    if (tracked) finishOperation(name, false, e.message);
    setStatus(`${action} ${name}: ${e.message}`, feedbackStatusErr);
  } finally {
    releaseMonitorToggleAfterRefresh(toggleKey, renderServices);
  }
}

async function closeSSHSession(name, pid, startTicks, terminal, user, managedByLogind) {
  const sessionPID = Number(pid);
  const sessionStartTicks = Number(startTicks);
  if (!name || !terminal || !Number.isSafeInteger(sessionPID) || sessionPID <= 0 || !Number.isSafeInteger(sessionStartTicks) || sessionStartTicks <= 0) {
    setStatus("close SSH session: invalid session identity; refresh and try again", feedbackStatusErr);
    return;
  }
  const label = `${user || "unknown user"} on ${terminal}`;
  if (!(await promptConfirm({
    title: `Close SSH session for ${label}?`,
    message: managedByLogind
      ? "This asks systemd-logind to revalidate and terminate that exact remote SSH login. It does not restart the SSH service and cannot be undone."
      : "This gracefully ends that one SSH terminal. It does not restart the SSH service and cannot be undone.",
    okLabel: "close session",
    danger: true,
  }))) return;
  await postSessionClose(`close SSH session ${label}`, sshSessionCloseAPI(name, sessionPID, sessionStartTicks, terminal, managedByLogind));
}

async function postSessionClose(statusLabel, endpoint) {
  setStatus("");
  try {
    const res = await fetch(endpoint, targetPostOptions());
    const body = await jsonOrThrow(res);
    setStatus(`${statusLabel}: ${body.message || feedbackStatusOK}`, feedbackStatusOK);
    load();
  } catch (e) {
    setStatus(`${statusLabel}: ${e.message}`, feedbackStatusErr);
  }
}

// reapStrays clears the processes a service cannot account for. It is confirmed
// like the session closes because it signals processes; the daemon decides which
// ones, from the service's own reap.kill_only_if, so with none declared the reply
// says nothing was authorized and nothing died.
async function reapStrays(name) {
  const svc = (allServices || []).find((s) => s && s.name === name);
  const n = svc ? serviceStrayCount(svc) : 0;
  if (!n) return;
  const ok = await promptConfirm({
    title: "Reap unaccounted-for processes",
    message: `Signal the ${n} process(es) in ${displayName(svc)}'s control group that no selector claims? `
      + "Only those matching the service's reap.kill_only_if are signalled; the rest are reported.",
    okLabel: "reap",
    danger: true,
  });
  if (!ok) return;
  if (pendingReaps.has(name)) return;
  pendingReaps.add(name);
  try {
    renderServices();
    setStatus("");
    const res = await fetch(serviceAPI(name, apiActionSuffix(actionReap)), targetPostOptions());
    const body = await jsonOrThrow(res);
    setStatus(`reap ${name}: ${body.message || feedbackStatusOK}`, feedbackStatusOK);
  } catch (e) {
    setStatus(`reap ${name}: ${e.message}`, feedbackStatusErr);
  } finally {
    // Hold the guard until the refresh lands, so the button cannot be pressed
    // again against a count the dashboard has not re-read yet.
    const refresh = load();
    Promise.resolve(refresh).finally(() => {
      pendingReaps.delete(name);
      renderServices();
    });
  }
}

async function closeTerminalSession(service, check, multiplexer, session, user, identity) {
  if (!service || !check || !session || !user || !identity || !terminalSessionKinds.includes(multiplexer)) {
    setStatus("close terminal session: invalid session identity; refresh and try again", feedbackStatusErr);
    return;
  }
  const label = `${multiplexer} ${session} for ${user}`;
  if (!(await promptConfirm({
    title: `Close ${label}?`,
    message: "This asks the multiplexer to close only that exact session. It cannot be undone.",
    okLabel: "close session",
    danger: true,
  }))) return;
  await postSessionClose(`close ${label}`, terminalSessionCloseAPI(service, check, multiplexer, session, user, identity));
}

async function closeEmptySessionSource(service, check) {
  const source = (latestSessionInventory.sources || []).find((candidate) =>
    candidate.service === service && (candidate.check || "") === check);
  if (!source || source.state !== sessionSourceAvailable || !source.can_close_empty || sourceHasSession(source, latestSessionInventory)) {
    setStatus("close empty session: source changed; refresh and try again", feedbackStatusErr);
    return;
  }
  const label = `tmux ${check || service}${source.user ? ` for ${source.user}` : ""}`;
  if (!(await promptConfirm({
    title: `Close empty ${label}?`,
    message: "This stops only the empty tmux server and safely removes any stale socket it leaves behind. The action cannot be undone.",
    okLabel: "close empty server",
    danger: true,
  }))) return;
  await postSessionClose(`close empty ${label}`, emptyTerminalSessionCloseAPI(service, check));
}

async function actWatch(name, action) {
  let headers = {};
  if (action === actionExpand && !(await confirmWatchExpand(name))) return;
  if (action === actionPause) {
    const w = (allWatches || []).find((item) => item && item.name === name) || {};
    if (!(await confirmWatchRAIDPause(name, w.raid_array || ""))) return;
    headers = { [apiHeaderConfirm]: w.raid_array || "" };
  }
  if (action === actionResume && !(await confirmWatchRAIDResume(name))) return;
  if (action === actionReplicationStart && !(await confirmWatchReplicationStart(name))) return;
  const toggleKey = acquireMonitorToggle(expansionPrefixWatch, name, action);
  if (toggleKey === null) return;
  try {
    // Guard-add render inside the try, as in act(): a throw must not strand
    // the pending key.
    if (toggleKey) renderWatches();
    setStatus("");
    if (action === actionProbe) beginWatchProbe(name);
    const res = await fetch(watchAPI(name, apiActionSuffix(action)), targetPostOptions(headers));
    const { body, failed, status } = await actionResult(res);
    if (action === actionProbe) {
      applyWatchProbeResult(name, body, failed);
      setStatus(`${action} watch ${name}: ${body.message || (failed ? "failed" : feedbackStatusOK)}`, failed ? feedbackStatusErr : feedbackStatusOK);
      return; // the finally below triggers the refresh
    }
    if (failed) {
      throw actionError(body, status);
    }
    setStatus(`${action} watch ${name}: ${body.message || feedbackStatusOK}`, feedbackStatusOK);
  } catch (e) {
    if (action === actionProbe) finishWatchProbe(name);
    setStatus(`${action} watch ${name}: ${e.message}`, feedbackStatusErr);
  } finally {
    releaseMonitorToggleAfterRefresh(toggleKey, renderWatches);
  }
}

function applyWatchProbeResult(name, body, failed) {
  const idx = (allWatches || []).findIndex((item) => item && item.name === name);
  if (idx < 0 || !body || typeof body !== "object") return;
  if (!Array.isArray(body.readings) && !body.message) return;
  const next = { ...allWatches[idx] };
  if (Array.isArray(body.readings)) next.readings = body.readings;
  if (body.message) next.summary = body.message;
  delete next.probe;
  next.sample_state = watchSampleStateFresh;
  next.last_checked_at = new Date().toISOString();
  next.state = failed ? targetStateFailed : targetStateOK;
  allWatches = [...allWatches];
  allWatches[idx] = next;
  renderWatches(allWatches);
}

function beginWatchProbe(name) {
  const idx = (allWatches || []).findIndex((item) => item && item.name === name);
  if (idx < 0) return;
  const next = { ...allWatches[idx], probe: { state: operationStateRunning, started_at: new Date().toISOString() } };
  allWatches = [...allWatches];
  allWatches[idx] = next;
  renderWatches(allWatches);
}

function finishWatchProbe(name) {
  const idx = (allWatches || []).findIndex((item) => item && item.name === name);
  if (idx < 0 || !allWatches[idx].probe) return;
  const next = { ...allWatches[idx] };
  delete next.probe;
  allWatches = [...allWatches];
  allWatches[idx] = next;
  renderWatches(allWatches);
}

async function confirmWatchRAIDPause(name, array) {
  if (!(await promptConfirm({ title: `Pause RAID reconstruction for ${name}?`, message: `Pause the active reconstruction on ${array || name}. This delays redundancy recovery.`, okLabel: "Continue", danger: true }))) return false;
  return promptConfirm({ title: "Confirm RAID pause", message: `Confirm pausing reconstruction for ${array || name}.`, okLabel: "Pause reconstruction", danger: true });
}

function confirmWatchReplicationStart(name) {
  return promptConfirm({
    title: `Start replication for ${name}?`,
    message: "Runs START REPLICA on the server, exactly as the manual repair would, and verifies both threads afterwards. It cannot skip or discard replication events.",
    okLabel: "Start replication",
    danger: true,
  });
}

function confirmWatchRAIDResume(name) {
  return promptConfirm({ title: `Resume RAID reconstruction for ${name}?`, message: "Resume the array's current reconstruction.", okLabel: "Resume reconstruction", danger: true });
}

async function testNotifier(name) {
  if (!name) return;
  if (!(await promptConfirm({
    title: `Test notifier ${name}?`,
    message: `Send a clearly marked test notification through "${name}"?`,
    okLabel: "Send test",
    danger: false,
  }))) return;
  setStatus("");
  try {
    const res = await fetch(notifierTestAPI(name), targetPostOptions());
    const body = await jsonOrThrow(res);
    setStatus(body.message || `test notification sent to ${name}`, feedbackStatusOK);
  } catch (e) {
    setStatus(`test notifier ${name}: ${e.message}`, feedbackStatusErr);
  }
  load();
}

async function fetchMountBlockers(name) {
  const res = await fetch(mountBlockersAPI(name));
  return jsonOrThrow(res);
}

function mountBlockerSummary(blockers) {
  const rows = (blockers || []).slice(0, 5).map((p) => {
    const user = p.user || `uid ${p.uid}`;
    const exe = p.exe || ((p.cmdline || [])[0]) || "unknown exe";
    const kill = p.killable ? ", killable by policy" : "";
    return `pid ${p.pid} ${user} ${exe}${kill}`;
  });
  const extra = (blockers || []).length > rows.length ? `\n… plus ${(blockers || []).length - rows.length} more` : "";
  return rows.join("\n") + extra;
}

function mountBlockerUser(p) {
  return p.user || `uid ${p.uid ?? "?"}`;
}

function mountBlockerGroup(p) {
  return p.group || `gid ${p.gid ?? "?"}`;
}

function mountBlockerCommand(p) {
  const cmd = Array.isArray(p.cmdline) ? p.cmdline.filter(Boolean).join(" ") : "";
  return cmd || p.exe || "unknown";
}

function mountBlockerRowsHTML(blockers, killSelected) {
  if (!blockers.length) {
    return '<tr><td colspan="5" class="muted">No current blockers.</td></tr>';
  }
  return blockers.map((p) => {
    const willSignal = killSelected && !!p.killable;
    const status = willSignal ? "will signal" : "will not signal";
    const cls = willSignal ? "will-signal" : "will-not-signal";
    return `<tr>
      <td class="${cls}">${esc(status)}</td>
      <td>${esc(mountBlockerUser(p))}</td>
      <td>${esc(mountBlockerGroup(p))}</td>
      <td>${esc(String(p.pid || "?"))}</td>
      <td class="cmd">${esc(mountBlockerCommand(p))}</td>
    </tr>`;
  }).join("");
}

function mountKillNote(info) {
  if (!info.has_kill_policy) {
    return "No stop_policy.kill_only_if is configured; blockers are shown but none can be signalled.";
  }
  if (!info.can_kill) {
    return "stop_policy.kill_only_if is configured, but no current blocker matches it.";
  }
  return "Only blockers matching stop_policy.kill_only_if can receive TERM/KILL; all other rows remain untouched.";
}

function updateMountUnmountBlockers(info) {
  const tbody = $("#mount-umount-blockers");
  if (!tbody) return;
  const killSelected = !!$("#mount-umount-kill")?.checked;
  setHTMLIfChanged(tbody, mountBlockerRowsHTML(info.blockers || [], killSelected));
}

let mountUnmountConfirmResolve = null;
let mountUnmountConfirmInfo = null;

function promptMountUnmount(name, info) {
  const dlg = $("#mount-umount-confirm");
  if (!dlg || typeof dlg.showModal !== "function") {
    return Promise.resolve(window.confirm(`Unmount "${name}"?`) ? { force: false, lazy: false, kill: false } : null);
  }
  mountUnmountConfirmInfo = info || {};
  const title = $("#mount-umount-title");
  const path = $("#mount-umount-path");
  const msg = $("#mount-umount-message");
  const note = $("#mount-umount-kill-note");
  const force = $("#mount-umount-force");
  const lazy = $("#mount-umount-lazy");
  const kill = $("#mount-umount-kill");
  if (title) title.textContent = `Unmount ${name}?`;
  if (path) path.textContent = info.path || "";
  const count = (info.blockers || []).length;
  if (msg) msg.textContent = count ? `${count} current blocker${count === 1 ? "" : "s"} detected.` : "No current blockers detected.";
  if (force) force.checked = false;
  if (lazy) lazy.checked = false;
  if (kill) {
    kill.checked = false;
    kill.disabled = !info.has_kill_policy || !info.can_kill;
    kill.title = kill.disabled ? mountKillNote(info) : "";
  }
  if (note) note.textContent = mountKillNote(info);
  updateMountUnmountBlockers(info);
  return new Promise((resolve) => {
    mountUnmountConfirmResolve = resolve;
    dlg.oncancel = () => closeMountUnmountConfirm(false);
    dlg.showModal();
  });
}

function closeMountUnmountConfirm(ok) {
  const dlg = $("#mount-umount-confirm");
  if (dlg && dlg.open) dlg.close();
  const resolve = mountUnmountConfirmResolve;
  mountUnmountConfirmResolve = null;
  const result = ok ? {
    force: !!$("#mount-umount-force")?.checked,
    lazy: !!$("#mount-umount-lazy")?.checked,
    kill: !!$("#mount-umount-kill")?.checked,
  } : null;
  mountUnmountConfirmInfo = null;
  if (resolve) resolve(result);
}

async function confirmMountUnmount(name) {
  const info = await fetchMountBlockers(name);
  if (info.can_umount === false) {
    setStatus(`umount ${name}: ${info.umount_disabled_reason || info.message || "unmount is disabled"}`, feedbackStatusWarn);
    return null;
  }
  return promptMountUnmount(name, info);
}

async function confirmMountAlert(name) {
  const info = await fetchMountBlockers(name);
  if (info.can_umount === false) {
    setStatus(`alert ${name}: ${info.umount_disabled_reason || info.message || "unmount is disabled"}`, feedbackStatusWarn);
    return false;
  }
  const blockers = info.blockers || [];
  if (!blockers.length) {
    setStatus(`alert ${name}: no blocking processes found`, feedbackStatusWarn);
    return false;
  }
  if (!info.can_alert) {
    setStatus(`alert ${name}: blockers have no resolved login user`, feedbackStatusWarn);
    return false;
  }
  return promptConfirm({
    title: `Alert users for ${name}?`,
    message: `Send a console message to users currently blocking "${name}"?\n\n${mountBlockerSummary(blockers)}`,
    okLabel: actionAlert,
    danger: false,
  });
}

async function actMount(name, action) {
  if (!name) return;
  let postAction = action;
  let query = "";
  const tracked = action === actionMount || action === actionUmount;
  try {
    if (action === actionUmount) {
      const opts = await confirmMountUnmount(name);
      if (!opts) return;
      const params = new URLSearchParams();
      if (opts.force) params.set(apiQueryForce, queryBoolOne);
      if (opts.lazy) params.set(apiQueryLazy, queryBoolOne);
      if (opts.kill) params.set(apiQueryKill, queryBoolOne);
      const encoded = params.toString();
      query = encoded ? `?${encoded}` : "";
    }
    if (action === actionAlert && !(await confirmMountAlert(name))) return;
  } catch (e) {
    setStatus(`${action} ${name}: ${e.message}`, feedbackStatusErr);
    return;
  }

  setStatus("");
  if (tracked) startMountOperation(name, action);
  try {
    const res = await fetch(mountAPI(name, apiActionSuffix(postAction, query)), targetPostOptions());
    const { body, failed, status } = await actionResult(res);
    if (failed) {
      const blockers = body.blockers && body.blockers.length ? `; blockers: ${mountBlockerSummary(body.blockers)}` : "";
      throw new Error(actionError(body, status).message + blockers);
    }
    setStatus(`${action} ${name}: ${body.message || feedbackStatusOK}`, feedbackStatusOK);
  } catch (e) {
    setStatus(`${action} ${name}: ${e.message}`, feedbackStatusErr);
  } finally {
    if (tracked) finishMountOperation(name);
  }
  load();
}

async function confirmWatchExpand(name) {
  const w = (allWatches || []).find((item) => item && item.name === name) || {};
  const by = w.expand && Number(w.expand.by_bytes) > 0 ? fmtBytes(w.expand.by_bytes) : "the configured amount";
  const path = w.storage && w.storage.path ? ` on ${w.storage.path}` : "";
  return promptConfirm({
    title: `Expand ${name}?`,
    message: `Expand "${name}"${path} by ${by}?`,
    okLabel: actionExpand,
    danger: true,
  });
}

let promptConfirmResolve = null;

// promptConfirm is the shared yes/no dialog for destructive or irreversible
// actions. Native <dialog> handles focus and Esc; callers await the boolean.
function promptConfirm(opts) {
  const dlg = $("#simple-confirm");
  const title = $("#simple-confirm-title");
  const msg = $("#simple-confirm-message");
  const okBtn = $("#simple-confirm-ok");
  const o = opts || {};
  if (!dlg || typeof dlg.showModal !== "function") {
    const text = [o.title, o.message].filter(Boolean).join("\n\n");
    return Promise.resolve(window.confirm(text || "Continue?"));
  }
  if (title) title.textContent = o.title || "Confirm";
  if (msg) msg.textContent = o.message || "";
  if (okBtn) {
    const okLabel = o.okLabel || "confirm";
    okBtn.textContent = okLabel;
    okBtn.className = o.danger ? "danger-btn" : "";
    okBtn.setAttribute("aria-label", okLabel === "confirm" ? "Confirm action" : `Confirm: ${okLabel}`);
  }
  return new Promise((resolve) => {
    promptConfirmResolve = resolve;
    dlg.oncancel = () => closePromptConfirm(false);
    dlg.showModal();
  });
}

function closePromptConfirm(ok) {
  const dlg = $("#simple-confirm");
  if (dlg && dlg.open) dlg.close();
  const resolve = promptConfirmResolve;
  promptConfirmResolve = null;
  if (resolve) resolve(!!ok);
}

let confirmResolve = null;
let confirmCtx = null;
let confirmNoCascade = false;

function confirmPreflightDisabledReason(action, state = {}) {
  if (state.loading) return "loading service context";
  if (state.running) return "preflight is running";
  if (!isServicePreflightAction(action)) return "preflight not available for this action";
  return "";
}

function syncConfirmPreflightButton(action, state = {}) {
  const btn = $("#confirm-preflight-btn");
  const hint = $("#confirm-preflight-hint");
  if (!btn) return;
  const reason = confirmPreflightDisabledReason(action, state);
  const disabled = !!reason;
  btn.disabled = disabled;
  if (hint) {
    hint.textContent = reason;
    if (reason) {
      hint.classList.remove("visually-hidden");
      btn.setAttribute("aria-describedby", "confirm-preflight-hint");
    } else {
      hint.textContent = "";
      hint.classList.add("visually-hidden");
      btn.removeAttribute("aria-describedby");
    }
  }
}

function servicePreflightDisabled(d) {
  return !d || !d.enabled;
}

function servicePreflightDisabledReason(d) {
  if (d && !d.enabled) return "service is disabled in configuration";
  return "";
}

function servicePreflightHintId(name) {
  return `svc-${name}-preflight-hint`;
}

function servicePreflightButton(d) {
  if (!me.can_act) return tpl`<span class="muted">admin only</span>`;
  const disabled = servicePreflightDisabled(d);
  const reason = servicePreflightDisabledReason(d);
  const hintId = servicePreflightHintId(d.name);
  const hint = disabled && reason
    ? tpl`<span id="${hintId}" class="visually-hidden">${reason}</span>`
    : nothing;
  const describedBy = disabled && reason ? hintId : nothing;
  const svcName = displayName(d) || d.name || "";
  return tpl`${hint}<button ?disabled=${disabled} data-preflight-service="${d.name}" aria-label="Run preflight checks for ${svcName}" aria-describedby="${describedBy}">run</button>`;
}

async function confirmAction(name, action) {
  const dlg = $("#action-confirm");
  if (!dlg || typeof dlg.showModal !== "function") {
    return promptConfirm({
      title: `${action} ${name}?`,
      message: `${action} "${name}"?`,
      okLabel: action,
      danger: isDangerServiceAction(action),
    });
  }
  confirmCtx = { name, action, detail: null, lastEvent: null, preflight: null };
  confirmNoCascade = false;
  $("#confirm-title").textContent = `${action.toUpperCase()} ${name}`;
  $("#confirm-subtitle").textContent = "Review the current service context before sending the operation.";
  litRender(tpl`<span class="muted">loading…</span>`, $("#confirm-body"));
  const actionBtn = $("#confirm-action-btn");
  if (actionBtn) {
    actionBtn.textContent = `${action} ${name}`;
    actionBtn.setAttribute("aria-label", `Confirm: ${action} ${name}`);
  }
  syncConfirmPreflightButton(action, { loading: true });
  const cascadeWrap = $("#confirm-no-cascade-wrap");
  const cascadeBox = $("#confirm-no-cascade");
  if (cascadeWrap) cascadeWrap.classList.add("is-hidden");
  if (cascadeBox) cascadeBox.checked = false;

  try {
    const generation = dashboardGeneration;
    const [detailRes, eventRes] = await Promise.all([
      fetch(serviceAPI(name)),
      fetch(serviceEventsAPI(name, eventContextLimit)),
    ]);
    if (generationMismatch(detailRes, generation) || generationMismatch(eventRes, generation)) {
      load();
      throw new Error("configuration changed; refresh and try again");
    }
    if (!detailRes.ok) throw new Error("HTTP " + detailRes.status);
    confirmCtx.detail = await detailRes.json();
    if (eventRes.ok) {
      const events = await eventRes.json();
      confirmCtx.lastEvent = (events || [])[0] || null;
    }
    syncConfirmPreflightButton(action);
    const alsoApply = (confirmCtx.detail?.also_apply || []);
    const showCascade = alsoApply.length > 0 && isServiceCascadeAction(action);
    if (cascadeWrap) cascadeWrap.classList.toggle("is-hidden", !showCascade);
    renderActionConfirm();
  } catch (e) {
    litRender(tpl`<span class="bad">Failed to load context: ${e.message}</span>`, $("#confirm-body"));
  }

  return new Promise((resolve) => {
    confirmResolve = resolve;
    dlg.oncancel = () => closeActionConfirm(false);
    dlg.showModal();
  });
}

function closeActionConfirm(ok) {
  if (ok) confirmNoCascade = !!($("#confirm-no-cascade")?.checked);
  const dlg = $("#action-confirm");
  if (dlg && dlg.open) dlg.close();
  const resolve = confirmResolve;
  confirmResolve = null;
  confirmCtx = null;
  if (resolve) resolve(!!ok);
}

// A native <dialog> handles Esc/backdrop close and role=dialog/focus itself; this
// makes sure an Esc-driven close still resolves the pending action as "cancel".
(function initConfirmDialog() {
  const dlg = $("#action-confirm");
  if (dlg) dlg.addEventListener(domEventClose, () => { if (confirmResolve) closeActionConfirm(false); });
})();

function renderActionConfirm() {
  const ctx = confirmCtx || {};
  const d = ctx.detail || {};
  const activeLocks = (d.locks || []).filter((l) => l.state === lockStateActive);
  // A verdictless check has no verdict to fail: its ok flag carries the sensed
  // state, so an idle state sensor would otherwise read as a blocker here.
  const failingChecks = (d.checks || []).filter((c) => c.ran && !c.ok && !c.optional && !verdictlessCheck(c));
  const procWarnings = d.process_warnings || [];
  const noResidentProcess = !!d.no_resident_process;
  const ev = ctx.lastEvent;
  const pre = ctx.preflight;
  const preState = isServicePreflightAction(ctx.action)
    ? pre ? (pre.ok ? tpl`<span class="ok">OK</span>` : tpl`<span class="bad">FAIL</span>`) : tpl`<span class="inactive">not run in this dialog</span>`
    : tpl`<span class="muted">not available for this action</span>`;
  const lockLine = activeLocks.length
    ? tpl`<span class="bad">${activeLocks.length} active</span> <span class="muted">(${activeLocks.map((l) => l.name || "default").join(", ")})</span>`
    : tpl`<span class="ok">none active</span>`;
  const checksLine = failingChecks.length
    ? tpl`<span class="bad">${failingChecks.length} required check${failingChecks.length === 1 ? "" : "s"} failing</span>`
    : tpl`<span class="ok">no required check failures observed</span>`;
  const procLine = noResidentProcess
    ? tpl`<span class="muted">No resident process expected</span>`
    : tpl`${(d.processes || []).length} discovered${procWarnings.length ? tpl` <span class="bad">· ${procWarnings.length} warning${procWarnings.length === 1 ? "" : "s"}</span>` : nothing}`;
  const lastEvent = ev
    ? tpl`${fmtTimeTitled(ev.time)} · <span class="kind kind-${ev.kind}">${ev.kind || ""}</span> ${[ev.action, ev.status].filter(Boolean).join(" ")} <span class="muted">${ev.message || ""}</span>`
    : tpl`<span class="muted">none recorded</span>`;
  const preRows = pre ? tpl`<div class="confirm-preflight-block">${preflightRows(pre.checks || [])}</div>` : nothing;
  const warning = ctx.action === actionRepair
    ? "Repair is a manual recovery for a failed or inactive service. It removes only a proven-stale runtime pidfile, clears a failed init state and then uses the normal guarded start path."
    : ctx.action === actionRestart
    ? "A safe restart stops the unit, verifies residual processes, then starts only if the stop phase is clean."
    : ctx.action === actionStart
      ? "Start will run through locks, guards and configured checks before the service is started."
      : "Stop will run through locks, guards and residual-process handling. It will not start the service again.";
  const cascadeTargets = (d.also_apply || []).filter(Boolean);
  const cascadeLine = cascadeTargets.length
    ? tpl`<p class="muted confirm-cascade-line">also_apply: <code>${cascadeTargets.join(", ")}</code></p>`
    : nothing;

  litRender(tpl`
    <p class="confirm-lead">${warning}</p>
    ${cascadeLine}
    <div class="modal-grid">
      <div class="muted">Unit</div><div><code>${d.unit || ""}</code></div>
      <div class="muted">State</div><div>${serviceStateCell(d)}</div>
      <div class="muted">Named locks</div><div>${lockLine}</div>
      <div class="muted">Guards</div><div><span class="muted">evaluated by the operation engine before ${ctx.action || "action"}</span></div>
      <div class="muted">Preflight</div><div>${preState}</div>
      <div class="muted">Checks</div><div>${checksLine}</div>
      <div class="muted">Processes</div><div>${procLine}</div>
      ${procWarnings.length ? tpl`<div class="muted">Discovery</div><div class="bad">${procWarnings[0]}${procWarnings.length > 1 ? tpl` <span class="muted">(+${procWarnings.length - 1} more)</span>` : nothing}</div>` : nothing}
      <div class="muted">Last event</div><div>${lastEvent}</div>
    </div>
    ${preRows}`, $("#confirm-body"));
}

async function runConfirmPreflight() {
  const ctx = confirmCtx;
  if (!ctx) return;
  syncConfirmPreflightButton(ctx.action, { running: true });
  $("#confirm-preflight-btn").textContent = "running…";
  try {
    const res = await fetch(servicePreflightAPI(ctx.name), targetPostOptions());
    if (!res.ok) throw new Error("HTTP " + res.status);
    ctx.preflight = await res.json();
  } catch (e) {
    ctx.preflight = { ok: false, checks: [{ name: "preflight", ok: false, message: e.message }] };
  }
  // The dialog may close (Esc/Cancel) while the request is in flight, which
  // nulls confirmCtx; a stale result must not touch the closed dialog.
  if (confirmCtx !== ctx) return;
  renderActionConfirm();
  $("#confirm-preflight-btn").textContent = "run preflight";
  syncConfirmPreflightButton(ctx.action);
}

function preflightRows(checks) {
  if (!checks || !checks.length) return tpl`<span class="muted">No preflight checks configured.</span>`;
  return tpl`<table>
    <caption class="visually-hidden">Preflight checks</caption>
    <thead><tr><th scope="col">Check</th><th scope="col">State</th><th scope="col">Message</th></tr></thead><tbody>${
    checks.map((c) => {
      const state = c.ok
        ? (c.optional ? tpl`<span class="ok">ok</span> <span class="muted">(optional)</span>` : tpl`<span class="ok">ok</span>`)
        : (c.optional ? tpl`<span class="inactive">warn</span>` : tpl`<span class="bad">fail</span>`);
      return tpl`<tr><td>${c.name}</td><td>${state}</td><td class="muted">${c.message || ""}</td></tr>`;
    })
  }</tbody></table>`;
}

async function runPreflight(name) {
  const target = document.getElementById(detailDomId(name, "preflight"));
  if (!target) return;
  litRender(tpl`<span class="muted">running…</span>`, target);
  try {
    const res = await fetch(servicePreflightAPI(name), targetPostOptions());
    if (!res.ok) throw new Error("HTTP " + res.status);
    const body = await res.json();
    const head = body.ok
      ? tpl`<span class="ok">OK</span>`
      : tpl`<span class="bad">FAIL</span>`;
    litRender(tpl`${head} ${preflightRows(body.checks || [])}`, target);
  } catch (e) {
    litRender(tpl`<span class="bad">Failed: ${e.message}</span>`, target);
  }
}

async function loadEventRows(targetID, url, generation = dashboardGeneration) {
  const target = document.getElementById(targetID);
  if (!target) return true;
  renderEventsLoading(target);
  try {
    const { stale, res } = await fetchPinned(url, generation);
    if (stale) return false;
    if (!res.ok) throw new Error("HTTP " + res.status);
    litRender(eventRows(await res.json(), false), target);
    return true;
  } catch (e) {
    litRender(tpl`<tr><td colspan="3" class="muted">Failed to load events: ${e.message}</td></tr>`, target);
    return false;
  }
}

async function loadServiceEvents(name, generation = dashboardGeneration) {
  return loadEventRows(
    detailDomId(name, "events"),
    serviceEventsAPI(name, eventDetailLimit),
    generation,
  );
}

const windowMs = {
  "1h": millisecondsPerHour,
  "24h": millisecondsPerDay,
  "168h": rollingWeekDays * millisecondsPerDay,
  "720h": rollingMonthDays * millisecondsPerDay,
  "8760h": rollingYearDays * millisecondsPerDay,
};

const metricTypes = ["tcp", "http", "ports", "service"];
const metricWins = [["1h", "1h"], ["24h", "24h"], ["7d", "168h"], ["30d", "720h"], ["1y", "8760h"]];

// applyWindowChoice records a time-window pick and puts it into effect: the
// stored window for that target, the persisted UI state, the pressed state of the
// buttons that offered it, and a reload of whatever the window governs. Every
// selector goes through it, so none can persist a choice its buttons do not show,
// or repaint without remembering.
function applyWindowChoice(stateKey, buttonKind, target, win, reload) {
  serviceMetricState(stateKey).window = win;
  saveUIState();
  syncWindowButtons(buttonKind, win, target);
  reload();
}

function setMetricWin(win, service) {
  if (!service) return;
  applyWindowChoice(service, "setMetricWin", service, win, () => {
    const detail = expDetailCache.get(serviceExpansionKey(service));
    if (detail) refreshServiceGraphs(detail);
    else loadExpansionFor(serviceExpansionKey(service));
  });
}
function setDaemonMetricWin(win) {
  daemonMetricWindow = win;
  saveUIState();
  syncWindowButtons("setDaemonMetricWin", daemonMetricWindow);
  loadDaemonMetrics();
}

function winButtons(list, selected, fn, groupLabel, service = "") {
  const btns = list.map(([label, val]) =>
    tpl`<button data-window-kind="${fn}" data-window-value="${val}" data-window-service="${service || nothing}" aria-pressed=${val === selected ? domBoolTrue : domBoolFalse} class="${val === selected ? "win-btn-active" : nothing}">${label}</button> `);
  return tpl`<span role="group" aria-label="${groupLabel || "Time window"}">${btns}</span>`;
}

function syncWindowButtons(kind, selected, service = "") {
  document.querySelectorAll("[data-window-kind][data-window-value]").forEach((btn) => {
    if (btn.dataset.windowKind !== kind) return;
    if ((btn.dataset.windowService || "") !== service) return;
    const active = btn.dataset.windowValue === selected;
    btn.classList.toggle("win-btn-active", active);
    btn.setAttribute("aria-pressed", active ? domBoolTrue : domBoolFalse);
  });
}

async function loadMetrics(name, measured, generation = dashboardGeneration) {
  const list = measured || [];
  if (!list.length) return true;
  const results = await Promise.all(list.map((c) => loadLatencyCheck(name, c.name, generation)));
  return results.every(Boolean);
}

// metricSeriesSummary is the one summary line a measured series gets: average,
// minimum and maximum, each through fmtMetricValue so the figures read exactly as
// the chart's own axis and tooltips do. Latency, named check metrics and service
// runtime metrics all use it — the panel's title already says which metric it is,
// so the line carries only the numbers.
function metricSeriesSummary(series) {
  const summary = (series && series.summary) || {};
  const unit = (series && series.unit) || "";
  if (!summary.count) return '<span class="muted">No data yet for this window.</span>';
  return `avg <b>${esc(fmtMetricValue(summary.avg, unit))}</b> · min ${esc(fmtMetricValue(summary.min, unit))} · max ${esc(fmtMetricValue(summary.max, unit))}`;
}

function loadLatencyCheck(name, check, generation = dashboardGeneration) {
  return loadMetricPanel(name, serviceLatencyDomID(name, check, "summary"), serviceLatencyDomID(name, check, "chart"),
    generation,
    (win) => serviceMetricsAPI(name, check, win),
    (body, win) => metricPanelContent(body, metricUnitMilliseconds, win, `Service latency chart (${check})`),
    `latency (${check})`);
}

function loadCheckMetric(name, metric, generation = dashboardGeneration) {
  return loadMetricPanel(name,
    serviceCheckMetricDomID(name, metric.check, metric.name, "summary"),
    serviceCheckMetricDomID(name, metric.check, metric.name, "chart"),
    generation,
    (win) => serviceMetricsAPI(name, metric.check, win, metric.name),
    (body, win) => metricPanelContent(body, metric.unit, win, `${serviceCheckMetricLabel(metric)} chart`),
    serviceCheckMetricLabel(metric));
}

// loadServiceRuntimeMetrics is the one loader that does not fit loadMetricPanel:
// a single request fills all three runtime panels. It paints and fails through the
// same two helpers, so the panels still report identically to every other one.
function loadServiceRuntimeMetrics(name, generation = dashboardGeneration) {
  const ids = (key) => [serviceRuntimeMetricDomID(name, key, "summary"), serviceRuntimeMetricDomID(name, key, "chart")];
  return loadServiceWindowGraph(name, generation,
    (win) => serviceRuntimeAPI(name, win),
    (body, win) => runtimeMetricDefs.forEach(({ key, label, unit }) => {
      paintMetricPanel(...ids(key), metricPanelContent(body[key], unit, win, `${label} runtime metric chart`));
    }),
    (e) => runtimeMetricDefs.forEach(({ key, label }) => failMetricPanel(...ids(key), `${label} runtime metrics`, e)));
}

async function loadDaemonMetrics() {
  try {
    const result = await getJSONResult(daemonMetricsAPI(daemonMetricWindow), null, dashboardGeneration);
    if (result.generationMismatch) {
      load();
      return;
    }
    if (result.data) renderDaemonMetrics(result.data);
  } catch (_) { /* getJSON already degrades */ }
}

function renderDaemonMetrics(body) {
  const c = (body && body.current) || {};
  const setText = (id, val) => {
    const el = $(id);
    if (el) el.textContent = (val === 0 || val) ? String(val) : "—";
  };
  setText("#daemon-pid", c.pid);
  setText("#daemon-fds", (c.fds === 0 || c.fds) ? fmtNum(c.fds, 0) : null);
  setText("#daemon-threads", (c.threads === 0 || c.threads) ? fmtNum(c.threads, 0) : null);
  setText("#daemon-cpu-live", c.cpu_ready ? `${fmtNum(c.cpu || 0, 2)}${metricUnitPercent}` : "measuring");
  const mem = c.rss ? fmtBytes(c.rss) : "";
  const memPct = (c.memory_percent === 0 || c.memory_percent) ? ` (${fmtNum(c.memory_percent, 2)}${metricUnitPercent})` : "";
  setText("#daemon-memory-live", mem ? mem + memPct : "");
  setText("#daemon-io-live", c.io_ready ? fmtBytesPerSecond(c.io || 0) : "measuring");

  const win = $("#daemon-metric-windows");
  if (win) litRender(winButtons(metricWins, daemonMetricWindow, "setDaemonMetricWin", "Daemon metrics time window"), win);
  // The daemon's process metrics are the same three a service's runtime panels
  // show, so they are the same panels: one titled box each, with its own summary
  // line, instead of a combined strip above three untitled charts.
  const grid = $("#daemon-metric-grid");
  if (grid) litRender(runtimeMetricPanels(daemonMetricDomID), grid);
  runtimeMetricDefs.forEach(({ key, unit, chartLabel }) => {
    paintMetricPanel(daemonMetricDomID(key, "summary"), daemonMetricDomID(key, "chart"),
      metricPanelContent(body[key], unit, daemonMetricWindow, chartLabel));
  });
}

const chartDataTableMaxRows = 30;

function metricChartDataTable(pts, unit, startMs, span, cols) {
  if (!pts.length) return "";
  const shown = pts.slice(-chartDataTableMaxRows);
  const rows = shown.map((o) => {
    const t = new Date(startMs + (o.i + 0.5) * (span / cols));
    const b = o.b;
    return `<tr><td>${esc(fmtTime(t.toISOString()))}</td><td>${esc(fmtMetricValue(b.sum / b.n, unit))}</td><td>${esc(fmtMetricValue(b.min, unit))}</td><td>${esc(fmtMetricValue(b.max, unit))}</td></tr>`;
  }).join("");
  return `<table class="chart-data visually-hidden"><caption>Chart data</caption><thead><tr><th scope="col">Time</th><th scope="col">Avg</th><th scope="col">Min</th><th scope="col">Max</th></tr></thead><tbody>${rows}</tbody></table>`;
}

function slaChartDataTable(observed) {
  if (!observed.length) return "";
  const shown = observed.slice(-chartDataTableMaxRows);
  const rows = shown.map((o) => {
    const up = Number(o.p.up || 0);
    const total = Number(o.p.total || 0);
    return `<tr><td>${esc(fmtTime(new Date(o.t).toISOString()))}</td><td>${esc(fmtPct(o.pct))}</td><td>${up}/${total}</td></tr>`;
  }).join("");
  return `<table class="chart-data visually-hidden"><caption>SLA chart data</caption><thead><tr><th scope="col">Time</th><th scope="col">SLA</th><th scope="col">Up/Total</th></tr></thead><tbody>${rows}</tbody></table>`;
}

function drawMetricChart(points, unit, win, label) {
  unit = unit || metricUnitMilliseconds;
  const W = chartViewWidth;
  const H = chartViewHeight;
  const pad = metricChartPad;
  const cols = chartColumnCount;
  const span = windowMs[win || defaultMetricWindow] || millisecondsPerDay;
  const { buckets, startMs } = bucketize(points, span, cols,
    () => ({ n: 0, sum: 0, min: Infinity, max: -Infinity }),
    (b, p) => {
      b.n += p.n; b.sum += p.avg * p.n;
      b.min = Math.min(b.min, p.min); b.max = Math.max(b.max, p.max);
    });
  let maxV = 0;
  buckets.forEach((b) => { if (b.n) maxV = Math.max(maxV, b.max); });
  const pts = buckets.map((b, i) => ({ i, b })).filter((o) => o.b.n > 0);
  if (!pts.length) return '<span class="muted">No data yet for this window.</span>';
  const scaleMax = maxV > 0 ? maxV : 1;
  const x = (i) => pad + (i + 0.5) * ((W - 2 * pad) / cols);
  const y = (v) => H - pad - (v / scaleMax) * (H - 2 * pad);
  const upper = pts.map((o) => `${x(o.i).toFixed(1)},${y(o.b.max).toFixed(1)}`);
  const lower = pts.slice().reverse().map((o) => `${x(o.i).toFixed(1)},${y(o.b.min).toFixed(1)}`);
  const band = pts.length > 1 ? `<polygon points="${upper.concat(lower).join(" ")}" fill="#1f6feb33"></polygon>` : "";
  const line = pts.length > 1
    ? `<polyline points="${pts.map((o) => `${x(o.i).toFixed(1)},${y(o.b.sum / o.b.n).toFixed(1)}`).join(" ")}" fill="none" stroke="#1f6feb" stroke-width="1.5"></polyline>`
    : pts.map((o) => `<circle cx="${x(o.i).toFixed(1)}" cy="${y(o.b.sum / o.b.n).toFixed(1)}" r="3" fill="#1f6feb"></circle>`).join("");
  const axis = `
    <line x1="${pad}" y1="${pad}" x2="${pad}" y2="${H - pad}" stroke="#8886"></line>
    <line x1="${pad}" y1="${H - pad}" x2="${W - pad}" y2="${H - pad}" stroke="#8886"></line>
    <text x="2" y="${pad + 4}" font-size="10" fill="#888">${esc(fmtMetricValue(maxV, unit))}</text>
    <text x="2" y="${H - pad}" font-size="10" fill="#888">0</text>
    <text x="${pad}" y="${H - 6}" font-size="10" fill="#888">${esc(fmtTime(new Date(startMs).toISOString()))}</text>
    <text x="${W - pad}" y="${H - 6}" font-size="10" fill="#888" text-anchor="end">now</text>`;
  // Transparent per-bucket strips carry a native <title> tooltip (value + time).
  const bw = (W - 2 * pad) / cols;
  const hover = pts.map((o) => {
    const t = new Date(startMs + (o.i + 0.5) * (span / cols));
    const tip = `${fmtTime(t.toISOString())}\navg ${fmtMetricValue(o.b.sum / o.b.n, unit)} · min ${fmtMetricValue(o.b.min, unit)} · max ${fmtMetricValue(o.b.max, unit)}`;
    return `<rect x="${(x(o.i) - bw / 2).toFixed(1)}" y="${pad}" width="${bw.toFixed(1)}" height="${(H - 2 * pad).toFixed(1)}" fill="transparent"><title>${esc(tip)}</title></rect>`;
  }).join("");
  const chartLabel = label || "Metric chart";
  // Convey the actual numbers, not just "<metric> chart", to screen readers.
  const latest = pts.length ? pts[pts.length - 1] : null;
  const aria = latest
    ? `${chartLabel}: latest ${fmtMetricValue(latest.b.sum / latest.b.n, unit)}, peak ${fmtMetricValue(maxV, unit)}`
    : chartLabel;
  const dataTable = metricChartDataTable(pts, unit, startMs, span, cols);
  return `${dataTable}<svg viewBox="0 0 ${W} ${H}" width="100%" role="img" aria-label="${esc(aria)}" style="max-width:${W}px"><title>${esc(aria)}</title>${axis}${band}${line}${hover}</svg>`;
}

function ruleState(r) {
  if (r.firing) return tpl`<span class="bad">firing</span>`;
  if (r.condition_true) return tpl`<span class="inactive">matching</span>`;
  return tpl`<span class="muted">idle</span>`;
}

function renderRules(rules) {
  if (!rules || !rules.length) return tpl`<p class="muted">No remediation or alert rules configured.</p>`;
  const rows = rules.map((r) => {
    const cond = r.condition_true
      ? tpl`<span class="inactive">${r.condition || ""}</span>`
      : tpl`<span class="muted">${r.condition || ""}</span>`;
    const action = r.action ? r.action : tpl`<span class="muted">—</span>`;
    return tpl`<tr>
      <td>${r.name}</td><td class="muted">${r.type || ""}</td><td>${action}</td>
      <td>${cond}</td><td class="muted">${r.window || ""}</td>
      <td>${r.progress || ""}</td><td>${ruleState(r)}</td></tr>`;
  });
  return tpl`<table>
    <caption class="visually-hidden">Remediation rules</caption>
    <thead><tr>
    <th scope="col">Name</th><th scope="col">Type</th><th scope="col">Action</th><th scope="col">Condition</th><th scope="col">Window</th><th scope="col">Progress</th><th scope="col">State</th>
  </tr></thead><tbody>${rows}</tbody></table>`;
}

function renderRemediation(r) {
  if (!r) return tpl`<span class="muted">not observed yet</span>`;
  const parts = [];
  if (!r.allowed) {
    if (r.reason === policyStateCooldown) {
      const rem = r.cooldown_until ? fmtRemain(r.cooldown_until) : "";
      parts.push(tpl`<span class="inactive">cooldown</span>${rem ? " · " + rem : nothing}`);
      if (r.effective_cooldown) parts.push(tpl`<span class="muted">effective ${r.effective_cooldown}</span>`);
    } else if (r.reason === policyStateRateLimit) {
      const lim = r.max_actions ? `${r.recent_actions || 0}/${r.max_actions}` : String(r.recent_actions || 0);
      parts.push(tpl`<span class="inactive">rate limit</span> · ${lim}`);
      if (r.max_actions_window) parts.push(tpl`<span class="muted">in ${r.max_actions_window}</span>`);
      if (r.next_eligible_at) parts.push(tpl`<span class="muted">eligible ${fmtUntilShort(r.next_eligible_at)}</span>`);
    } else if (r.reason) {
      parts.push(tpl`<span class="inactive">${r.reason}</span>`);
    }
  } else {
    parts.push(tpl`<span class="ok">ready</span>`);
  }
  if (r.current_backoff) parts.push(tpl`<span class="muted">backoff ${r.current_backoff}</span>`);
  if (r.last_action_at) parts.push(tpl`<span class="muted">last action ${fmtAge(r.last_action_at)}</span>`);
  // Intersperse " · " separators between the parts (TemplateResults can't be join()ed).
  return parts.map((p, i) => i ? [" · ", p] : p);
}

function esc(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function capFirst(s) {
  const chars = Array.from(String(s || "").trim());
  if (!chars.length) return "";
  chars[0] = chars[0].toLocaleUpperCase();
  return chars.join("");
}

function displayName(item) {
  return capFirst((item && (item.display_name || item.name)) || "");
}

function categoryOf(item, fallback) {
  const raw = item && typeof item.category === "string" ? item.category.trim() : "";
  return raw || fallback;
}

function categoryBadge(category) {
  return tpl`<span class="category-badge" title="${category}">${category}</span>`;
}

function categoryCounts(items, fallback) {
  const counts = new Map();
  (items || []).forEach((item) => {
    const category = categoryOf(item, fallback);
    counts.set(category, (counts.get(category) || 0) + 1);
  });
  return counts;
}

function sortedCategories(items, fallback) {
  return Array.from(categoryCounts(items, fallback).keys())
    .sort((a, b) => a.localeCompare(b, undefined, { numeric: true, sensitivity: "base" }));
}

function syncCategorySelect(id, items, fallback, selected, allLabel = "all categories") {
  const select = $(id);
  if (!select) return selected || filterAll;
  const categories = sortedCategories(items, fallback);
  const visible = categories.length > 1;
  select.hidden = !visible;
  select.disabled = !visible;
  const next = visible && selected !== filterAll && categories.includes(selected) ? selected : filterAll;
  const counts = categoryCounts(items, fallback);
  select.innerHTML = `<option value="${filterAll}">${esc(allLabel)}</option>` + categories.map((category) =>
    `<option value="${esc(category)}">${esc(category)} (${counts.get(category) || 0})</option>`
  ).join("");
  select.value = next;
  return next;
}

function groupedPanelId(rowPrefix, items) {
  const first = items && items[0];
  if (!first || !first.name) return nothing;
  return `${rowPrefix}-row-${first.name}`;
}

function sortedGroupValues(list, groupOf) {
  return [...new Set(list.map(groupOf))].sort((a, b) =>
    a.localeCompare(b, undefined, { numeric: true, sensitivity: "base" }));
}

function toggleAllGroups(groups, collapsedGroups) {
  const allCollapsed = groups.length > 0 && groups.every((group) => collapsedGroups.has(group));
  if (allCollapsed) groups.forEach((group) => collapsedGroups.delete(group));
  else groups.forEach((group) => collapsedGroups.add(group));
}

function renderGroupedRows(list, collapsedGroups, panel, rowPrefix, groupOf, colspan, renderRow, groupDir) {
  const groups = new Map();
  list.forEach((item) => {
    const group = groupOf(item);
    if (!groups.has(group)) groups.set(group, []);
    groups.get(group).push(item);
  });
  const dir = groupDir === -1 ? -1 : 1;
  return Array.from(groups.entries()).sort((a, b) =>
    a[0].localeCompare(b[0], undefined, { numeric: true, sensitivity: "base" }) * dir
  ).map(([group, items]) => {
    const collapsed = collapsedGroups.has(group);
    const panelId = groupedPanelId(rowPrefix, items);
    const header = tpl`<tr class="group-row">
      <td colspan="${colspan}"><button type="button" class="row-toggle group-toggle" data-group-panel="${panel}" data-group-name="${group}" aria-expanded="${collapsed ? domBoolFalse : domBoolTrue}" aria-controls="${panelId}" aria-label="${groupToggleAriaLabel(group, items.length, collapsed)}"><span class="exp" aria-hidden="true">${collapsed ? "▸" : "▾"}</span>${group} <span class="muted">${items.length}</span></button></td>
    </tr>`;
    return [header, collapsed ? nothing : items.map(renderRow)];
  });
}

function updateGroupButtons(prefix, grouped, groups, collapsedGroups, label, groupBy = "category") {
  const group = $("#" + prefix + "-group-toggle");
  const available = groups.length > 1;
  if (group) {
    group.hidden = !available;
    group.disabled = !available;
    group.setAttribute("aria-pressed", grouped ? domBoolTrue : domBoolFalse);
    group.title = grouped ? `Ungroup ${label}` : `Group ${label} by ${groupBy}`;
    group.setAttribute("aria-label", group.title);
  }
  const all = $("#" + prefix + "-groups-toggle");
  if (!all) return;
  const allCollapsed = available && groups.every((groupName) => collapsedGroups.has(groupName));
  all.hidden = !available;
  all.disabled = !grouped || !available;
  all.innerHTML = allCollapsed ? "▾" : "▴";
  all.title = allCollapsed ? `Expand all ${label} groups` : `Collapse all ${label} groups`;
  all.setAttribute("aria-label", all.title);
  all.setAttribute("aria-pressed", grouped && available && !allCollapsed ? domBoolTrue : domBoolFalse);
}

function closestFrom(event, selector) {
  let target = event.target;
  if (target && target.nodeType !== 1) target = target.parentElement;
  return target && target.closest ? target.closest(selector) : null;
}

function bindSortHeader(th, action) {
  th.tabIndex = 0;
  th.setAttribute("aria-sort", "none");
  th.addEventListener(domEventClick, action);
  th.addEventListener(domEventKeydown, (e) => {
    if (e.key !== keyEnter && e.key !== keySpace) return;
    e.preventDefault();
    action();
  });
}

// bindConfirmDialog wires a result-button dialog: a click on a
// resultSelector button closes it with dataset[resultKey] === "true", and a
// bare dialog close while pending() resolves as false. A missing dlg is a no-op.
function bindConfirmDialog(dlg, resultSelector, resultKey, close, pending) {
  if (!dlg) return;
  dlg.addEventListener(domEventClick, (e) => {
    const b = closestFrom(e, resultSelector);
    if (b) close(b.dataset[resultKey] === domBoolTrue);
  });
  dlg.addEventListener(domEventClose, () => {
    if (pending()) close(false);
  });
}

// bindStopClick wires a click that only stops propagation before invoking fn —
// the admin-button idiom where default button behavior stays intact. A missing
// el is a no-op.
function bindStopClick(el, fn) {
  if (!el) return;
  el.addEventListener(domEventClick, (e) => {
    e.stopPropagation();
    fn();
  });
}

// bindActionClick wires a click on el that suppresses default/propagation before
// invoking fn — the group-toggle button idiom. A missing el is a no-op.
function bindActionClick(el, fn) {
  if (!el) return;
  el.addEventListener(domEventClick, (e) => {
    e.preventDefault();
    e.stopPropagation();
    fn();
  });
}

// bindSearchBox wires a panel search input: apply(value) on input, and clear on
// Escape. A missing el is a no-op.
function bindSearchBox(el, apply) {
  if (!el) return;
  el.addEventListener(domEventInput, () => apply(el.value));
  el.addEventListener(domEventKeydown, (e) => {
    if (e.key === keyEscape) {
      el.value = "";
      apply("");
    }
  });
}

// bindFilterButtons wires a filter bar: a click on a button[data-<dataKey>] calls
// apply with its dataset value (or filterAll). A missing el is a no-op.
function bindFilterButtons(el, dataKey, apply) {
  if (!el) return;
  el.addEventListener(domEventClick, (e) => {
    const btn = closestFrom(e, `button[data-${dataKey}]`);
    if (btn) apply(btn.dataset[dataKey] || filterAll);
  });
}

function initStaticHandlers() {
  const targetSearch = $("#target-search");
  if (targetSearch) {
    targetSearch.addEventListener(domEventChange, submitGlobalTargetSearch);
    targetSearch.addEventListener(domEventKeydown, (e) => {
      if (e.key === keyEnter) {
        e.preventDefault();
        submitGlobalTargetSearch();
      } else if (e.key === keyEscape) {
        targetSearch.value = "";
      }
    });
  }

  const refreshSelect = $("#refresh-select");
  if (refreshSelect) refreshSelect.addEventListener(domEventChange, () => setRefresh(refreshSelect.value));

  const refreshButton = $("#refresh-now");
  if (refreshButton) refreshButton.addEventListener(domEventClick, refreshNow);

  const refreshPendingButton = $("#refresh-pending");
  if (refreshPendingButton) refreshPendingButton.addEventListener(domEventClick, refreshNow);

  const notifyToggle = $("#notify-toggle");
  if (notifyToggle) {
    syncNotifyToggle();
    notifyToggle.addEventListener(domEventClick, async () => {
      if (!(notificationAPIName in window)) return;
      if (browserNotifyEnabled()) {
        setBrowserNotifyEnabled(false);
        return;
      }
      let permission = Notification.permission;
      if (permission === notifyPermissionDefault) permission = await Notification.requestPermission();
      if (permission !== notifyPermissionGranted) {
        setBrowserNotifyEnabled(false);
        setStatus("browser notifications are blocked for this site", feedbackStatusWarn, false);
        return;
      }
      setBrowserNotifyEnabled(true);
    });
  }

  const shortcutToggle = $("#shortcut-toggle");
  if (shortcutToggle) {
    shortcutToggle.checked = keyboardShortcutsEnabled();
    shortcutToggle.addEventListener(domEventChange, () => setKeyboardShortcutsEnabled(shortcutToggle.checked));
  }

  bindSearchBox($("#svc-search"), setSvcQuery);
  bindFilterButtons($("#svc-filters"), "f", setSvcStatus);
  bindSearchBox($("#session-search"), setSessionQuery);
  bindFilterButtons($("#session-filters"), "sf", setSessionFilter);
  document.querySelectorAll("#sessions-section .sessions-table th.sortable[data-session-sort]").forEach((th) => {
    bindSortHeader(th, () => setSessionSort(th.dataset.sessionSort || ""));
  });

  const svcCategorySelect = $("#svc-category");
  if (svcCategorySelect) svcCategorySelect.addEventListener(domEventChange, () => setSvcCategory(svcCategorySelect.value));

  bindActionClick($("#svc-group-toggle"), () => setSvcGrouped(!svcGrouped));
  bindActionClick($("#svc-groups-toggle"), toggleAllSvcGroups);

  function bindSplitServicePanelControls(panelKey) {
    const panel = getSplitServicePanel(panelKey);
    if (!panel) return;
    bindSearchBox($(panel.search), (v) => setSplitServiceQuery(panelKey, v));
    bindFilterButtons($(panel.filters), panel.filterDataset, (v) => setSplitServiceStatus(panelKey, v));
    bindActionClick($("#" + panelKey + "-group-toggle"), () => setSplitServiceGrouped(panelKey, !panel.grouped));
    bindActionClick($("#" + panelKey + "-groups-toggle"), () => toggleAllSplitServiceGroups(panelKey));
    document.querySelectorAll(`${panel.section} .services-table th.sortable[data-${panel.sortAttr}]`).forEach((th) => {
      bindSortHeader(th, () => setSplitServiceSort(panelKey, th.dataset[panel.sortDataset] || ""));
    });
  }
  Object.keys(splitServicePanels).forEach(bindSplitServicePanelControls);

  document.querySelectorAll(".services-table th.sortable[data-sort]").forEach((th) => {
    bindSortHeader(th, () => setSvcSort(th.dataset.sort || ""));
  });

  document.querySelectorAll(".events th.sortable[data-ev-sort]").forEach((th) => {
    bindSortHeader(th, () => setEvSort(th.dataset.evSort || ""));
  });

  applyUIStateToControls();

  function bindWatchPanelControls(panelKey) {
    const panel = getWatchPanel(panelKey);
    bindSearchBox($(panel.search), (v) => setWatchQuery(panelKey, v));

    const typeSelect = $(panel.typeSelect);
    if (typeSelect) typeSelect.addEventListener(domEventChange, () => setWatchType(panelKey, typeSelect.value));

    bindActionClick($("#" + panelKey + "-group-toggle"), () => setWatchGrouped(panelKey, !panel.grouped));
    bindActionClick($("#" + panelKey + "-groups-toggle"), () => toggleAllWatchGroups(panelKey));
    bindFilterButtons($(panel.filters), "wf", (v) => setWatchStatus(panelKey, v));
  }
  [watchPanelKeyHost].forEach(bindWatchPanelControls);

  document.querySelectorAll(".watch-table th.sortable[data-watch-sort]").forEach((th) => {
    bindSortHeader(th, () => setWatchSort(watchPanelKeyForElement(th), th.dataset.watchSort || ""));
  });

  bindSearchBox($("#mount-search"), setMountQuery);

  const mountCategorySelect = $("#mount-category");
  if (mountCategorySelect) mountCategorySelect.addEventListener(domEventChange, () => setMountCategory(mountCategorySelect.value));
  bindActionClick($("#mount-group-toggle"), () => setMountGrouped(!mountGrouped));
  bindActionClick($("#mount-groups-toggle"), toggleAllMountGroups);

  bindFilterButtons($("#mount-filters"), "mf", setMountStatus);

  document.querySelectorAll(".mount-table th.sortable[data-mount-sort]").forEach((th) => {
    bindSortHeader(th, () => setMountSort(th.dataset.mountSort || ""));
  });

  bindSearchBox($("#app-search"), setAppQuery);

  const appCategorySelect = $("#app-category");
  if (appCategorySelect) appCategorySelect.addEventListener(domEventChange, () => setAppCategory(appCategorySelect.value));
  bindFilterButtons($("#app-filters"), "af", setAppStatus);

  bindActionClick($("#app-group-toggle"), () => setAppGrouped(!appGrouped));
  bindActionClick($("#app-groups-toggle"), toggleAllAppGroups);

  document.querySelectorAll(".apps-table th.sortable[data-app-sort]").forEach((th) => {
    bindSortHeader(th, () => setAppSort(th.dataset.appSort || ""));
  });

  bindSearchBox($("#library-search"), setLibraryQuery);
  const libraryCategorySelect = $("#library-category");
  if (libraryCategorySelect) libraryCategorySelect.addEventListener(domEventChange, () => setLibraryCategory(libraryCategorySelect.value));
  bindFilterButtons($("#library-filters"), "lf", setLibraryStatus);
  bindActionClick($("#library-group-toggle"), () => setLibraryGrouped(!libraryGrouped));
  bindActionClick($("#library-groups-toggle"), toggleAllLibraryGroups);
  document.querySelectorAll(".libraries-table th.sortable[data-library-sort]").forEach((th) => {
    bindSortHeader(th, () => setLibrarySort(th.dataset.librarySort || ""));
  });

  ["event-service", "event-watch", "event-kind", "event-status", "event-range", "event-from", "event-to"].forEach((id) => {
    const el = $("#" + id);
    if (!el) return;
    el.addEventListener(domEventChange, flushLoadEvents);
    el.addEventListener(domEventKeydown, eventFilterKey);
  });
  const onlyErrors = $("#event-errors");
  if (onlyErrors) onlyErrors.addEventListener(domEventChange, flushLoadEvents);
  const groupEvents = $("#event-group");
  if (groupEvents) groupEvents.addEventListener(domEventChange, () => { saveUIState(); renderGlobalEvents(); });
  const eventResetFilters = $("#event-reset-filters");
  if (eventResetFilters) eventResetFilters.addEventListener(domEventClick, clearEventFilters);
  const eventMore = $("#event-more");
  if (eventMore) eventMore.addEventListener(domEventClick, loadOlderEvents);
  bindStopClick($("#event-clear"), clearEventLog);

  document.querySelectorAll("[data-confirm-result]").forEach((btn) => {
    btn.addEventListener(domEventClick, () => closeActionConfirm(btn.dataset.confirmResult === domBoolTrue));
  });
  const confirmPreflight = $("#confirm-preflight-btn");
  if (confirmPreflight) confirmPreflight.addEventListener(domEventClick, runConfirmPreflight);

  bindStopClick($("#reload-btn"), reloadConfig);
  bindStopClick($("#state-compact-btn"), compactState);
  bindStopClick($("#panic-btn"), () => requestPanic(!panicOn));
  bindConfirmDialog($("#panic-confirm"), "[data-panic-result]", "panicResult", closePanicConfirm, () => panicResolve);
  bindConfirmDialog($("#simple-confirm"), "[data-simple-result]", "simpleResult", closePromptConfirm, () => promptConfirmResolve);
  bindConfirmDialog($("#mount-umount-confirm"), "[data-mount-umount-result]", "mountUmountResult", closeMountUnmountConfirm, () => mountUnmountConfirmResolve);
  const mountUmountKill = $("#mount-umount-kill");
  if (mountUmountKill) {
    mountUmountKill.addEventListener(domEventChange, () => {
      if (mountUnmountConfirmInfo) updateMountUnmountBlockers(mountUnmountConfirmInfo);
    });
  }
}

function initDelegatedHandlers() {
  document.addEventListener(domEventChange, (e) => {
    const typeFilter = closestFrom(e, "[data-watch-type-filter-panel][data-watch-type-filter]");
    if (typeFilter) setWatchTypeFilter(typeFilter.dataset.watchTypeFilterPanel || watchPanelKeyHost, typeFilter.dataset.watchTypeFilter || "", typeFilter.value);
  });

  // Delegated click routes: the first selector matching an ancestor of the
  // click target wins, mirroring the previous if/return chain. The plain-row
  // fallthrough stays outside because rowClick also needs the event.
  const clickRoutes = [
    ["[data-event-toggle]", (el) => toggleEventMsg(el.dataset.eventToggle || "")],
    ["[data-panel-target]", (el) => openPanelTarget(el.dataset.panelTarget || "")],
    ["[data-ssh-session-close]", (el) => closeSSHSession(
      el.dataset.sshService || "", el.dataset.sshSessionPid || "", el.dataset.sshSessionStartTicks || "",
      el.dataset.sshSessionTerminal || "", el.dataset.sshSessionUser || "",
      el.dataset.sshSessionManaged === domBoolTrue)],
    ["[data-terminal-session-close]", (el) => closeTerminalSession(
      el.dataset.terminalService || "", el.dataset.terminalCheck || "", el.dataset.terminalMultiplexer || "",
      el.dataset.terminalSession || "", el.dataset.terminalUser || "", el.dataset.terminalIdentity || "")],
    ["[data-empty-session-close]", (el) => closeEmptySessionSource(
      el.dataset.emptySessionService || "", el.dataset.emptySessionCheck || "")],
    ["[data-service-action][data-service]", (el) => act(el.dataset.service || "", el.dataset.serviceAction || "")],
    ["[data-service-button][data-service]", (el) => pressServiceButton(el.dataset.service || "", el.dataset.serviceButton || "")],
    ["[data-watch-action][data-watch]", (el) => actWatch(el.dataset.watch || "", el.dataset.watchAction || "")],
    ["[data-mount-action][data-mount]", (el) => actMount(el.dataset.mount || "", el.dataset.mountAction || "")],
    ["[data-notifier-test]", (el) => testNotifier(el.dataset.notifierTest || "")],
    ["[data-service-reap]", (el) => reapStrays(el.dataset.serviceReap || "")],
    ["[data-service-pin]", (el) => toggleServicePin(el.dataset.servicePin || "")],
    ["[data-service-expand]", (el) => toggleServiceExpansion(el.dataset.serviceExpand || "")],
    ["[data-service-open]", (el) => openServiceExpansion(el.dataset.serviceOpen || "", true)],
    ["[data-lock-release]", (el) => releaseLock(el.dataset.lockService || "", el.dataset.lockName || "")],
    ["[data-preflight-service]", (el) => runPreflight(el.dataset.preflightService || "")],
    ["[data-window-kind][data-window-value]", (el) => {
      const val = el.dataset.windowValue || "";
      switch (el.dataset.windowKind) {
        case "setMetricWin":
          setMetricWin(val, el.dataset.windowService || "");
          break;
        case "setSLAWin":
          setSLAWin(val, el.dataset.windowService || "");
          break;
        case "setWatchMetricWin":
          setWatchMetricWin(val, el.dataset.windowService || "");
          break;
        case "setDaemonMetricWin":
          setDaemonMetricWin(val);
          break;
      }
    }],
    ["[data-watch-type-sort-panel][data-watch-type-sort-type][data-watch-type-sort]",
      (el) => setWatchTypeSort(el.dataset.watchTypeSortPanel || watchPanelKeyHost, el.dataset.watchTypeSortType || "", el.dataset.watchTypeSort || "name")],
    ["[data-group-panel][data-group-name]", (el) => toggleGroup(el.dataset.groupPanel || "", el.dataset.groupName || "")],
    ["[data-exp-toggle]", (el) => toggleExpand(el.dataset.expToggle || "")],
  ];

  document.addEventListener(domEventClick, (e) => {
    for (const [selector, handle] of clickRoutes) {
      const el = closestFrom(e, selector);
      if (el) {
        handle(el);
        return;
      }
    }

    const row = closestFrom(e, "[data-exp-key]");
    if (row) rowClick(e, row.dataset.expKey || "");
  });

  document.addEventListener(domEventKeydown, (e) => {
    if (e.key !== keyEnter && e.key !== keySpace) return;
    const typeSort = closestFrom(e, "[data-watch-type-sort-panel][data-watch-type-sort-type][data-watch-type-sort]");
    if (!typeSort) return;
    e.preventDefault();
    setWatchTypeSort(typeSort.dataset.watchTypeSortPanel || watchPanelKeyHost, typeSort.dataset.watchTypeSortType || "", typeSort.dataset.watchTypeSort || "name");
  });
}

initStaticHandlers();
initDelegatedHandlers();
window.addEventListener(domEventResize, updateTopbarHeight);
updateTopbarHeight();
loadMe().then(() => { load(); });

// Manual refresh + a once-per-second "updated Xs ago" readout. The readout is
// independent of the auto-refresh interval, so it keeps counting up even when
// auto-refresh is set to a long interval or stopped.
let lastRefresh = 0;
function refreshNow() { load().finally(scheduleRefresh); }
function showPartialRefresh(failures) {
  const age = lastRefresh ? ` (last full update ${fmtSince(Date.now() - lastRefresh)} ago)` : "";
  setStatus(`Partial refresh — stale: ${failures.join(", ")}${age}`, feedbackStatusWarn, false);
}
function tickRefreshAge() {
  if (!connOK) { showDisconnected(); return; } // keep the banner's age fresh
  updateWatchProbeElapsed();
  const el = $("#last-refresh");
  if (!el) return;
  const text = lastRefresh ? `fully updated ${fmtSince(Date.now() - lastRefresh)} ago` : "";
  if (el.textContent === text) return;
  el.textContent = text;
}

function updateWatchProbeElapsed() {
  document.querySelectorAll("[data-probe-started-at]").forEach((el) => {
    const text = "· " + watchProbeElapsed(el.dataset.probeStartedAt || "");
    if (el.textContent !== text) el.textContent = text;
  });
}
setInterval(tickRefreshAge, refreshAgeTickMs);

let refreshTimer = null;
let refreshDelay = 0;
// While the daemon is unreachable each failed poll doubles the retry delay up
// to a cap, so a downed daemon is not hammered at the configured cadence; any
// successful load (scheduled, manual or visibility-driven) resets it.
const refreshBackoffMaxMs = 2 * millisecondsPerMinute;
let refreshFailures = 0;
function refreshInterval() {
  if (!refreshFailures) return refreshDelay;
  return Math.min(refreshDelay * 2 ** refreshFailures, Math.max(refreshDelay, refreshBackoffMaxMs));
}

// The change stream (SSE) makes the dashboard push-driven on top of the poll:
// the daemon signals every emitted event and the client refetches through the
// normal API within a debounce window. It never slows the scheduled poll down,
// because the daemon only notifies on events (action, alert, firing, …) —
// nothing pushes when a metric sample changes, so host CPU/memory/load, service
// runtime and watch readings still depend on the operator's chosen cadence.
const streamLoadDebounceMs = 500;
let streamLoadTimer = null;
(function initChangeStream() {
  if (!(eventSourceAPIName in window)) return;
  const source = new EventSource(apiStreamPath);
  source.addEventListener(domEventChange, () => {
    if (streamLoadTimer) clearTimeout(streamLoadTimer);
    streamLoadTimer = setTimeout(() => {
      streamLoadTimer = null;
      // A hidden tab catches up via the visibilitychange reload instead.
      if (document.hidden) return;
      // Auto-refresh "stop" means the view is frozen on purpose — the operator
      // is reading a row or copying a value. Never repaint underneath them;
      // offer the update instead.
      if (refreshDelay <= 0) {
        setRefreshPending(true);
        return;
      }
      load();
    }, streamLoadDebounceMs);
  });
})();

// setRefreshPending shows or hides the topbar "new data" button, the escape
// hatch that keeps a frozen dashboard from silently going out of date.
function setRefreshPending(pending) {
  const btn = $("#refresh-pending");
  if (btn) btn.hidden = !pending;
}
function scheduleRefresh() {
  if (refreshTimer) clearTimeout(refreshTimer);
  refreshTimer = null;
  if (refreshDelay <= 0 || document.hidden) return;
  refreshTimer = setTimeout(async () => {
    refreshTimer = null;
    await load();
    scheduleRefresh();
  }, refreshInterval());
}
function applyRefresh(ms) {
  refreshDelay = ms;
  scheduleRefresh();
}
document.addEventListener(domEventVisibilityChange, () => {
  if (document.hidden) {
    if (refreshTimer) clearTimeout(refreshTimer);
    refreshTimer = null;
    return;
  }
  // Same rule as the change stream: with auto-refresh stopped, coming back to
  // the tab must not discard the frozen view.
  if (refreshDelay <= 0) {
    setRefreshPending(true);
    return;
  }
  load().finally(scheduleRefresh);
});
function setRefresh(v) {
  const ms = parseInt(v, 10) || 0;
  applyRefresh(ms);
  // Leaving "stop" resumes at once; waiting out the first interval would look
  // like the selector did nothing.
  if (ms > 0) refreshNow();
  try { localStorage.setItem("sermo-refresh", String(ms)); } catch (_) {}
}
(function initRefresh() {
  let ms = 30000;
  try { const s = localStorage.getItem("sermo-refresh"); if (s !== null) ms = parseInt(s, 10) || 0; } catch (_) {}
  const sel = $("#refresh-select");
  if (sel) sel.value = String(ms);
  applyRefresh(ms);
})();

function keyboardShortcutsEnabled() {
  try { return localStorage.getItem(KEYBOARD_SHORTCUTS_KEY) !== storageBoolFalse; } catch (_) { return true; }
}

function setKeyboardShortcutsEnabled(enabled) {
  try { localStorage.setItem(KEYBOARD_SHORTCUTS_KEY, enabled ? storageBoolTrue : storageBoolFalse); } catch (_) {}
}

// activeSearchBox returns the search input for the topmost open data panel.
// Watch panels come straight from the watchPanels registry so a new panel's
// search box joins the "/" shortcut without extending this list.
function activeSearchBox() {
  const panels = [
    ["#sessions-section", "#session-search"],
    ["#services-section", "#svc-search"],
    ["#containers-section", "#container-search"],
    ["#vms-section", "#vm-search"],
    ...Object.values(watchPanels).map((p) => [p.section, p.search]),
    ["#apps-section", "#app-search"],
    ["#libraries-section", "#library-search"],
  ];
  for (const [sectionSel, searchSel] of panels) {
    const section = $(sectionSel);
    if (!section || !panelVisible(section) || !section.open) continue;
    const box = $(searchSel);
    if (box) return { section, box };
  }
  const fallback = $("#svc-search") || $("#container-search") || $("#vm-search");
  return fallback ? { section: $("#" + defaultServicePanelTarget()), box: fallback } : null;
}

// Ctrl/Cmd+K focuses global target search; "/" focuses the visible panel search.
document.addEventListener(domEventKeydown, (e) => {
  if (!keyboardShortcutsEnabled()) return;
  if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === "k") {
    const globalSearch = $("#target-search");
    if (!globalSearch) return;
    e.preventDefault();
    globalSearch.focus();
    globalSearch.select();
    return;
  }
  if (e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey) return;
  const tag = document.activeElement && document.activeElement.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;
  const target = activeSearchBox();
  if (!target) return;
  e.preventDefault();
  if (target.section && !target.section.open) target.section.open = true;
  target.box.focus();
});


// Remember which sections the operator left open/closed across reloads. Each
// <details id="..."> restores its saved state on load and saves on every toggle;
// sections with no saved state keep their HTML default.
(function initCollapse() {
  document.querySelectorAll("details[id]").forEach((el) => {
    const key = "sermo-open-" + el.id;
    try {
      const saved = localStorage.getItem(key);
      if (saved !== null) el.open = saved === storageBoolTrue;
    } catch (_) {}
    el.addEventListener(domEventToggle, () => {
      try { localStorage.setItem(key, el.open ? storageBoolTrue : storageBoolFalse); } catch (_) {}
    });
  });
})();
