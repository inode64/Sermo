import { html as tpl, render as litRender, nothing, svg } from "./vendor/lit-html.js";
import watchPanelDescriptors from "./watch-panels.json";
import {
  apiActionSuffix, apiActivityPath, apiApplicationsPath, apiDaemonPath, apiHeaderGeneration,
  apiEventsRecentPath, apiHostPath, apiLibrariesPath, apiLocksPath,
  apiHeaderConfirm, apiMountsPath, apiNotifiersPath,
  apiQueryBefore, apiQueryBeforeID,
  apiQueryForce, apiQueryKill, apiQueryKind, apiQueryLazy, apiQueryLimit, apiQueryName, apiQueryNoCascade,
  apiQueryOnlyErrors, apiQueryService, apiQuerySince, apiQueryStatus,
  apiQueryWatch, apiReloadPath, notifierTestAPI,
  apiServicesPath, apiSessionsPath, apiStreamPath, apiWatchesPath, apiWhoamiPath, applicationEventsAPI,
  csrfPostOptions, dashboardAPI, daemonMetricsAPI, eventsAPI, eventsClearAPI,
  dbQueryKillAPI, emptyTerminalSessionCloseAPI,
  liveVerbosePath, lockReleaseAPI, mountAPI, mountBlockersAPI, panicAPI,
  readyVerbosePath, refreshAPI, serviceAPI, serviceButtonAPI, serviceEventsAPI, serviceMetricsAPI,
  servicePreflightAPI, serviceRuntimeAPI, serviceSLAAPI, sshSessionCloseAPI, terminalSessionCloseAPI, stateCompactAPI, watchAPI,
  watchMetricsAPI, watchProcessKillAPI, watchSLAAPI,
} from "./api.js";
import {
  fmtAge, fmtBytes, fmtBytesPerSecond, fmtCountdown, fmtDuration, fmtElapsedTime, fmtMetricValue, fmtNum, fmtPct, fmtRemain,
  fmtSince, fmtTime, fmtUntilShort, millisecondsPerDay,
  millisecondsPerHour, millisecondsPerMinute, millisecondsPerSecond,
  metricUnitBytes, metricUnitBytesPerSecond, metricUnitMilliseconds,
  metricUnitPercent, pctClamp, percentMax,
  percentScale, rollingMonthDays, rollingWeekDays, rollingYearDays,
  secondsPerHour, secondsPerMinute, isAdvisorySeverity, severityCritical, severityLevels,
} from "./format.js";

const $ = (s) => document.querySelector(s);
// mutedDash stands for a reading there is nothing to show for, readOnlyNote
// for the actions a viewer who cannot act does not get.
const mutedDash = tpl`<span class="muted">—</span>`;
const readOnlyNote = tpl`<span class="muted">read-only</span>`;

const metricNameCPU = "cpu";
const metricNameMemory = "memory";
const metricNameIO = "io";
const hostMetricTotalCPU = "total_cpu";
const hostMetricTotalMemory = "total_memory";
const hostMetricTotalSwap = "total_swap";
const hostMetricLoad1 = "load1";
const eventLogLimit = "500";
const lastActivityMaxAgeMs = 15 * millisecondsPerDay;
const httpStatusServiceUnavailable = 503;
const httpStatusUnauthorized = 401;
const httpStatusPreconditionFailed = 412;
const httpStatusConflict = 409;
// Relative, like the API paths, so the dashboard keeps working under a reverse
// proxy path.
const loginPath = "login";
const logoutPath = "logout";
const expansionPrefixApp = "app:";
const expansionPrefixLibrary = "lib:";
const expansionPrefixService = "svc:";
const expansionPrefixWatch = "wat:";
// watchTypeUnownedProcesses is the one watch type whose expansion lists
// processes with a kill button; its rows arrive in w.processes.
const watchTypeUnownedProcesses = "unowned_processes";
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
const serviceSurfaceRegular = "service";
const serviceSurfaceContainer = "container";
const serviceSurfaceVM = "vm";
const dockerServiceCategory = "docker";
const vmServiceCategory = "virtual-machine";
// Classes for a General data field in a service/container/VM expansion whose
// reading is also a .services-table column: the field is hidden while that column
// is on screen and revealed at the breakpoint that retires it, so the reading is
// shown exactly once and a narrow viewport loses nothing. The suffix names the
// breakpoint, matching the .col-dup rules in styles.css — colDupWide for the
// columns the 1420px block drops (Uptime, CPU total, Memory, FDs, IO R/W)
// and colDupPhone for Last activity, which survives down to 640px.
//
// A field the column does not restate *exactly* carries neither class:
// "FDs / Threads" also reports the thread count, which has no column of its own.
// TestIndexDetailDedupesVisibleColumns pins these against both the CSS and the
// fields that use them.
// SQL details use the same breakpoints: State, both CPU readings, Memory and
// IO R/W move at 1420px; Type, Database and ID move at 640px.
const colDupWide = "col-dup col-dup-1420";
const colDupPhone = "col-dup col-dup-640";
const eventDetailLimit = "50";
// eventRangeAll is the events range selector's explicit "any time" value.
const eventRangeAll = "all";
// eventRangeDefault is the window an events panel opens on. "any time" and a
// legacy empty range are the same unfiltered log; every other window is narrower.
const eventRangeDefault = "24h";
const eventContextLimit = "1";
const queryBoolOne = "1";
const storageBoolTrue = "1";
const storageBoolFalse = "0";
const domBoolTrue = "true";
const domBoolFalse = "false";
const domEventAnimationEnd = "animationend";
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
const targetStateRestartRequired = "restart_required";
const targetStateStale = "stale";
// Mirrors stateReason* in internal/app/metric_constants.go.
const stateReasonConfigurationInvalid = "configuration_invalid";
const stateReasonConfigurationWarning = "configuration_warning";
const stateReasonStaleBinary = "stale_binary";
const stateReasonStaleLibraries = "stale_libraries";
const stateReasonFailedUnitLiveProcess = "failed_unit_live_process";
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
const targetStatePausing = "pausing";
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
const healthStatusCriticalSeverity = severityCritical;
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
const actionSilence = "silence";
const actionPause = "pause";
const actionReplicationStart = "replication-start";
const actionMonitor = "monitor";
const actionReload = "reload";
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
const eventKindError = "error";
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
const eventKindRemount = "remount";
const eventKindRemountFailed = "remount-failed";
const eventKindRemountSkipped = "remount-skipped";
const eventKindOutputDrain = "drain_output";
const eventKindOutputDrainFailed = "drain_output-failed";
const eventKindOutputDrainSkipped = "drain_output-skipped";
const eventKindNotify = "notify";
const eventKindNotifyFailed = "notify-failed";
const eventKindNotifySuppressed = "notify-suppressed";
const eventKindPanicSuppressed = "panic-suppressed";
const eventKindRecovered = "recovered";
const eventKindReload = "reload";
const eventKindSuppressed = "suppressed";
const eventStatusOK = "ok";
const eventStatusBlocked = "blocked";
const eventStatusPreflightFailed = "preflight_failed";
const eventStatusPostflightFailed = "postflight_failed";
const eventStatusOrphanProcesses = "orphan_processes";
// Stop still opens the confirmation dialog; the engine does not run preflight
// on stop, so the dialog's preflight runner stays disabled for that action.
const serviceConfirmActions = [actionStart, actionStop, actionRestart, actionRepair, actionPause];
const servicePreflightActions = [actionStart, actionRestart, actionRepair];
// Only lifecycle actions that change the unit's running state propagate to
// also_apply targets. Repair remains a deliberately one-service recovery.
const serviceCascadeActions = [actionStart, actionStop, actionRestart];
// The engine operations (mirrors operation.IsServiceAction in the API): the
// actions that run through the operation pipeline, so they are the ones this
// browser tracks in liveOps and the ones a named lock blocks.
const serviceTrackedActions = [actionStart, actionStop, actionRestart, actionReload, actionPause, actionResume, actionRepair];
const activityCriticalStatuses = [targetStateFailed, mountStateError, eventStatusPreflightFailed, eventStatusPostflightFailed, eventStatusOrphanProcesses];
// operationOutcomeStyles is the one presentation of an operation outcome
// (operationOutcome): the class of its state text (op card, row note, detail
// status), its button mark's badge, its status-line tone and how a forecast of
// it reads. "" is an operation still running, amber like a blocked one.
const operationOutcomeStyles = {
  "": { cls: "inactive", badge: "", tone: "", forecast: "" },
  [eventStatusOK]: { cls: targetStateOK, badge: "", tone: feedbackStatusOK, forecast: "" },
  [eventStatusBlocked]: { cls: "inactive", badge: "!", tone: feedbackStatusWarn, forecast: "would be blocked" },
  [targetStateFailed]: { cls: "bad", badge: "✕", tone: feedbackStatusErr, forecast: "would fail" },
};
// An outcome mark pulses when it first appears: the class (and its CSS
// animation, same name) runs three .8s iterations, under the 5s WCAG 2.2.2
// bound for unrequested motion, and a row rendered after this window does not
// carry it.
const outcomePulseClass = "outcome-pulse";
const outcomePulseMs = 2400;
const activityCriticalKinds = [mountStateError, eventKindHookFailed, eventKindNotifyFailed, eventKindExpandFailed, eventKindKillFailed, eventKindMakeStepFailed, eventKindRemountFailed, eventKindOutputDrainFailed];
const activityWarningKinds = [actionAlert, eventKindFiring, eventKindSuppressed, eventKindPanicSuppressed, eventKindNotifySuppressed, eventKindExpandSkipped, eventKindMakeStepSkipped, eventKindRemountSkipped, eventKindOutputDrainSkipped];
const activityOKKinds = [eventKindAction, eventKindCascade, eventKindHook, eventKindNotify, eventKindRecovered, actionExpand, eventKindKill, eventKindMakeStep, eventKindRemount, eventKindOutputDrain];
const activityInfoKinds = [eventKindDryRun, eventKindReload];
const serviceStatusFilterStates = [
  targetStateDisabled,
  targetStateStopped,
  targetStatePaused,
  targetStateStarted,
  targetStateActive,
  targetStateStarting,
  targetStateCollecting,
  targetStateMonitored,
  targetStateRestartRequired,
  targetStateWarning,
  targetStateFailed,
];
const watchStatusFilterStates = [targetStateDisabled, targetStateOK, targetStateStarting, targetStateWarning, targetStateStale, targetStateTesting, targetStateRecovering, targetStateRebuilding, targetStateRepairing, targetStateMoving, targetStateMerging, targetStateMissing, targetStateFailed];
const appStatusFilterStates = [targetStateOK, targetStateStarting, targetStateWarning, targetStateFailed];
const mountStatusFilterStates = [mountStateActive, mountStateInactive];
const libraryStatusFilterStates = [targetStateOK, targetStateWarning, targetStateFailed];
const sessionKindSSH = "ssh";
const sessionKindTmux = "tmux";
const sessionKindScreen = "screen";
const terminalSessionKinds = [sessionKindTmux, sessionKindScreen];
// A db_queries watch lists running statements under its own source kind; its
// rows have their own table, since their columns (database, client, running
// time, statement) differ from a terminal's.
const sessionKindDatabase = "database";
const sessionTypeFilterStates = [sessionKindSSH, ...terminalSessionKinds, sessionKindDatabase];
const dbQueryKillModeQuery = "query";
const dbQueryKillModeConnection = "connection";
// A statement longer than this shows a preview that expands to the full text.
const sessionSourceAvailable = "available";
const sessionSourceUnavailable = "unavailable";
const sessionSourcePartial = "partial";
const sessionStateActive = "active";
const sessionStateResidual = "residual";
const sessionStateAttached = "attached";
const sessionStateDetached = "detached";
const sessionStateEmpty = "empty";
const sessionStateUnknown = "unknown";
const sessionStateBadges = {
  [targetStateCollecting]: [targetStateCollecting, targetStateCollecting],
  [sessionSourceUnavailable]: [targetStateFailed, sessionSourceUnavailable],
  [sessionSourcePartial]: [targetStateWarning, sessionSourcePartial],
  [sessionStateActive]: [targetStateActive, sessionStateActive],
  [sessionStateResidual]: [targetStateWarning, sessionStateResidual],
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
const overviewActiveServiceStates = [targetStateStarted, targetStateActive, targetStateCollecting, targetStateWarning, targetStateRestartRequired, targetStateMonitored];
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
  [targetStateRestartRequired]: "state-restart-required",
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
  [targetStatePausing]: "state-starting",
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
  [targetStateRestartRequired]: 6,
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
  [targetStatePausing]: 1,
  [targetStateReloading]: 1,
};
const operationActionStates = {
  [actionStart]: targetStateStarting,
  [actionRepair]: targetStateStarting,
  [actionStop]: targetStateStopping,
  [actionRestart]: targetStateRestarting,
  [actionResume]: targetStateResuming,
  [actionPause]: targetStatePausing,
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
function isServiceExpansionKey(key) { return key.startsWith(expansionPrefixService); }
function isWatchExpansionKey(key) { return key.startsWith(expansionPrefixWatch); }
function isServiceConfirmAction(action) { return serviceConfirmActions.includes(action); }
function isServicePreflightAction(action) { return servicePreflightActions.includes(action); }
function isServiceCascadeAction(action) { return serviceCascadeActions.includes(action); }
function isDangerServiceAction(action) { return action === actionStop || action === actionRestart || action === actionRepair || action === actionPause; }

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
  // Admin-only controls carry data-admin; the stylesheet shows them only to
  // who can act.
  document.body.classList.toggle("can-act", !!me.can_act);
  const readOnly = me.auth && me.role !== "admin";
  $("#me").innerHTML = readOnly ? '<a class="role-badge" href="login" title="Log in to operate services">read-only · log in</a>' : "";
  // Only a /login session can be ended (me.session): the server cannot make a
  // browser forget a cached Basic credential, so it offers no logout for one.
  const session = $("#menu-session");
  if (session) {
    session.hidden = !me.session;
    session.innerHTML = me.session
      ? `<button type="button" class="menu-item" data-logout>Log out <span class="muted">${esc(me.role || "")}</span></button>`
      : "";
  }
}

// Connection state: when a fetch fails the table is dimmed and a "disconnected,
// retrying" banner (with the age of the last good update) replaces the refresh
// status, instead of silently showing stale data.
let connOK = true;
let lastLoadOk = Date.now();
let loadSeq = 0;
let dashboardGeneration = 0;
function targetPostOptions(headers = {}, expectedGeneration = dashboardGeneration) {
  const generation = expectedGeneration > 0
    ? { [apiHeaderGeneration]: String(expectedGeneration) }
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
    try {
      await performLoad();
    } catch (err) {
      // Nothing in a refresh is meant to throw. One that does leaves the page
      // half drawn, so it says so instead of passing for a finished refresh.
      console.error(err);
      setStatus(`refresh failed: ${err.message}`, feedbackStatusErr, false);
    }
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
    getJSONResult(apiHostPath, []),
  ]);
  const [servicesResult, sessionsResult, mountsResult, notifiersResult, daemonResult, daemonMetricsResult,
    locksResult, activityResult, readyResult, liveResult, hostMetricsResult] = results;
  const { generation, mismatch: generationMismatch } = sharedBackendGeneration(results);
  return { servicesResult, sessionsResult, mountsResult, notifiersResult, daemonResult, daemonMetricsResult,
    locksResult, activityResult, readyResult, liveResult,
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
  // draw runs one renderer of this refresh on its own. The page shows one view
  // at a time and the tiles are the way between them, so a renderer that throws
  // on a host's data must cost its own panel — not the tiles and every panel
  // drawn after it — and is named with the stale sections when the refresh ends.
  const drawFailures = [];
  const draw = (what, render) => {
    try {
      render();
    } catch (err) {
      console.error(err);
      drawFailures.push(`${what} (display error: ${err.message})`);
    }
  };
  const { servicesResult, sessionsResult, mountsResult, notifiersResult, daemonResult,
    daemonMetricsResult, locksResult, activityResult, readyResult, liveResult,
    hostMetricsResult, generation, generationMismatch,
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
  const hostMetrics = hostMetricsResult.data;
  if (servicesResult.ok) {
    draw("services", () => render(services));
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
  if (mounts) draw("mounts", () => renderMounts(mounts));
  if (sessionsResult.ok) draw("sessions", () => renderSessions(sessions));
  if (notifiers) draw("notifiers", () => renderNotifiers(notifiers));
  if (daemon) draw("daemon", () => renderDaemon(daemon));
  if (daemonMetrics) draw("daemon metrics", () => renderDaemonMetrics(daemonMetrics));
  if (locks) draw("locks", () => renderLocks(locks));
  if (activity) draw("activity", () => renderActivity(activity));

  // Status/readyz updates every refresh even when api/services fails, so the
  // header lifecycle line does not stay stuck on a stale "starting".
  renderStatus({
    ready,
    live,
    locks: locks || latestLocks,
    daemon: daemon || {},
    // The failure fallback for host metrics is [] (truthy), so keeping the
    // last known readings needs the result's ok flag, not || on the data.
    hostMetrics: hostMetricsResult.ok ? hostMetrics : latestHostMetrics,
  });
  draw("address target", applyHash);
  if (connOK) {
    healthIconReady = true;
    refreshChrome();
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
  if (watchesResult.ok) draw("watches", () => renderWatches(watchesResult.data));
  if (appsResult.ok) draw("applications", () => renderApps(appsResult.data));
  if (librariesResult.ok) draw("libraries", () => renderLibraries(librariesResult.data));
  if (!connOK) return;

  // First complete snapshot: watches and apps have now loaded, so render the
  // alert-count tile and the attention banner for real (they were held neutral
  // until here). Later polls already carry this data into phase one, so this
  // one-time reveal runs only on the initial load.
  if (!firstSnapshotDone) {
    firstSnapshotDone = true;
    draw("overview", () => renderOverview({ ready, live, daemon: daemon || {}, locks: locks || latestLocks, hostMetrics: hostMetricsResult.ok ? hostMetrics : latestHostMetrics }));
    refreshChrome();
  }

  const [expandedWatchesOK, expandedApplicationsOK] = await Promise.all([
    watchesResult.ok ? refreshExpandedWatches(generation) : Promise.resolve(false),
    appsResult.ok ? refreshExpandedApplications(generation) : Promise.resolve(false),
  ]);
  if (!sameLoad()) return;

  const refreshResults = [
    ["sessions", sessionsResult], ["mounts", mountsResult], ["notifiers", notifiersResult], ["daemon", daemonResult],
    ["daemon metrics", daemonMetricsResult], ["locks", locksResult], ["activity", activityResult],
    ["readiness", readyResult], ["liveness", liveResult],
    ["host metrics", hostMetricsResult], ["watches", watchesResult],
    ["applications", appsResult], ["libraries", librariesResult], ["events", eventsResult],
    ["service details", { ok: expandedServicesOK }],
    ["watch details", { ok: expandedWatchesOK }],
    ["application details", { ok: expandedApplicationsOK }],
  ];
  const failures = [...refreshResults.filter(([, result]) => !result.ok).map(([name]) => name), ...drawFailures];
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

// redirectToLogin reacts to a 401 on an API call. The server answers API calls
// and the EventSource with a bare 401 — never the Basic challenge, which made the
// browser throw a password box at someone who was just reading the dashboard —
// so a 401 here would otherwise fail silently: the page goes to the login form
// instead. Guarded so several concurrent polls cannot each start a navigation.
let loginRedirectStarted = false;
function redirectToLogin(res) {
  if (!res || res.status !== httpStatusUnauthorized) return false;
  if (!loginRedirectStarted) {
    loginRedirectStarted = true;
    window.location.assign(loginPath);
  }
  return true;
}

// logout ends this browser's /login session, then shows the login form. The
// session cookie is HttpOnly, so only the server can drop it. It claims the
// redirect guard first, so a poll that hits the 401 meanwhile does not start a
// second navigation.
async function logout() {
  loginRedirectStarted = true;
  try {
    await fetch(logoutPath, csrfPostOptions());
  } catch { /* the form below is the outcome either way */ }
  window.location.assign(loginPath);
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
  if (res.status === httpStatusPreconditionFailed) load();
  return { body, failed: !res.ok || body.ok === false, status: res.status };
}

// actionError is the thrown form of a refused action. resultStatus keeps the
// body's operation result status, so a service action can tell a blocked
// operation from a failed one.
function actionError(body, status) {
  const err = new Error(body.message || ("HTTP " + status));
  err.resultStatus = body.status || "";
  return err;
}

async function jsonOrThrow(res) {
  const { body, failed, status } = await actionResult(res);
  if (failed) throw actionError(body, status);
  return body;
}

// runAction is the life of a dashboard action that is one POST: ask first when
// it needs asking, clear the last result, hold its button, post, say what the
// daemon answered and follow up. `ok` turns the reply into the message (or
// [message, tone]); `fail` opens the error line; `then` is what follows a
// success — a dashboard refresh unless the caller names something else, or
// null. Returns true when it went through, false when it failed and null when
// the operator declined.
async function runAction({ confirm, button, request, ok, fail, then = load }) {
  if (confirm && !(await confirm())) return null;
  setStatus("");
  if (button) button.disabled = true;
  try {
    const body = await jsonOrThrow(await request());
    const [message, tone = feedbackStatusOK] = [ok(body)].flat();
    setStatus(message, tone);
    if (then) await then(body);
    return true;
  } catch (e) {
    setStatus(`${fail}: ${e.message}`, feedbackStatusErr);
    return false;
  } finally {
    if (button) button.disabled = false;
  }
}

// beforeQuery is the optional cutoff the two pruning actions pass on.
function beforeQuery(before) {
  return before ? `?${apiQueryBefore}=${encodeURIComponent(before)}` : "";
}

function reloadConfig() {
  return runAction({
    button: $("#reload-btn"),
    request: () => fetch(apiReloadPath, csrfPostOptions()),
    ok: () => "config reload requested",
    fail: "reload failed",
    // The daemon reloads after it answers: give it a moment before reading back.
    then: () => { setTimeout(load, 800); },
  });
}

// refreshPanelPaths maps a refresh button's data-refresh-panel, which also
// names the panel in the status line, to the collection whose checks it runs.
const refreshPanelWatches = "watches";
const refreshPanelPaths = {
  applications: apiApplicationsPath,
  libraries: apiLibrariesPath,
  [refreshPanelWatches]: apiWatchesPath,
};

// refreshPanel asks the daemon to run an extra check of everything behind one
// panel now, waits for it, then reloads the dashboard; each watch keeps its
// schedule. A watch's extra cycle runs its alerts and actions and counts toward
// its cycle windows, so the Watches panel asks first: repeated clicks must not
// reach a `for: N cycles` window in seconds. Applications and libraries carry
// no actions. A 409 carries an outcome that did not complete (a check not
// refreshed by the deadline), which reads amber rather than as a failed request.
async function refreshPanel(btn) {
  const panel = btn.dataset.refreshPanel || "";
  const path = refreshPanelPaths[panel];
  if (!path || btn.disabled) return;
  const generation = dashboardGeneration;
  if (panel === refreshPanelWatches && !(await promptConfirm({
    title: "Check every watch now?",
    message: "Each enabled watch runs an extra check now; its schedule does not change.",
    detail: "It is a real check: alerts, hooks and actions run as on a scheduled one, "
      + "and it counts toward cycle-based for/within windows.",
    okLabel: "Check now",
  }))) return;
  btn.disabled = true;
  btn.setAttribute("aria-busy", domBoolTrue);
  setStatus(`refreshing ${panel}…`, feedbackStatusOK);
  try {
    const res = await fetch(refreshAPI(path), targetPostOptions({}, generation));
    const { body, failed, status } = await actionResult(res);
    if (failed && !(status === httpStatusConflict && body.message)) throw actionError(body, status);
    setStatus(body.message || `${panel} refreshed`, failed ? feedbackStatusWarn : feedbackStatusOK);
  } catch (e) {
    setStatus(`refresh ${panel} failed: ${e.message}`, feedbackStatusErr);
  } finally {
    btn.disabled = false;
    btn.removeAttribute("aria-busy");
    load();
  }
}

let eventNextBeforeID = 0;
let eventHasMore = false;

function eventPageEvents(page) {
  return Array.isArray(page.events) ? page.events : [];
}

// eventFilterFields are the Events panel's value controls: where each lives,
// the key it is saved under and, for the ones the daemon filters by, their
// query parameter. Reading, saving, restoring, resetting and wiring them all
// go through this list.
const eventFilterFields = [
  { id: "#event-service", key: "service", param: apiQueryService },
  { id: "#event-watch", key: "watch", param: apiQueryWatch },
  { id: "#event-kind", key: "kind", param: apiQueryKind },
  { id: "#event-status", key: "status", param: apiQueryStatus },
  { id: "#event-range", key: "range" },
  { id: "#event-from", key: "from" },
  { id: "#event-to", key: "to" },
];

function eventFilterValue(field) {
  return ($(field.id) || {}).value || "";
}

async function loadEvents(seq = 0, append = false, generation = dashboardGeneration) {
  try {
    const params = new URLSearchParams({ [apiQueryLimit]: eventLogLimit });
    for (const field of eventFilterFields) {
      const value = field.param ? eventFilterValue(field).trim() : "";
      if (value) params.set(field.param, value);
    }
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
  eventFilterFields.forEach(({ id }) => {
    const el = $(id);
    if (el) el.value = "";
  });
  const err = $("#event-errors");
  if (err) err.checked = false;
  saveUIState();
  loadEvents();
}

// ---- Panic mode ----
let panicOn = false;
let panicResolve = null;

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
  await runAction({
    confirm: () => panicConfirm(enable),
    button: $("#panic-btn"),
    request: () => fetch(panicAPI(enable), csrfPostOptions()),
    ok: (body) => {
      updatePanicView(enable);
      return [body.message || (enable ? "panic mode enabled" : "panic mode disabled"), enable ? feedbackStatusErr : feedbackStatusOK];
    },
    fail: "panic mode",
  });
}

async function clearEventLog() {
  if (!me.can_act) return;
  const before = ($("#event-before")?.value || "").trim();
  await runAction({
    confirm: () => promptConfirm({
      title: "Clear event log?",
      message: before
        ? `Clear persisted events older than ${before}? This cannot be undone.`
        : "Clear all persisted events from the activity log? This cannot be undone.",
      okLabel: "clear log",
      danger: true,
    }),
    button: $("#event-clear"),
    request: () => fetch(eventsClearAPI(beforeQuery(before)), csrfPostOptions()),
    ok: (body) => {
      const n = Number(body.pruned) || 0;
      return n ? `cleared ${n} event${n === 1 ? "" : "s"}` : "no events to clear";
    },
    fail: "events clear",
  });
}

async function compactState() {
  if (!me.can_act) return;
  const before = ($("#state-before")?.value || "").trim();
  await runAction({
    confirm: () => promptConfirm({
      title: "Compact state?",
      message: before
        ? `Compact state history older than ${before} and vacuum the database?`
        : "Compact state history using the normal retention window and vacuum the database?",
      okLabel: "compact",
      danger: true,
    }),
    button: $("#state-compact-btn"),
    request: () => fetch(stateCompactAPI(beforeQuery(before)), csrfPostOptions()),
    ok: (body) => {
      const n = Number(body.pruned) || 0;
      return n ? `compacted state: pruned ${n} row${n === 1 ? "" : "s"}` : (body.message || "state compact completed");
    },
    fail: "state compact",
  });
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
    else if (isWatchExpansionKey(expKey)) rerenderWatchExpansion(expansionName(expKey, expansionPrefixWatch));
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
// the list panels via the shared toggleSort/sortedBy helpers.
let evSort = { key: "", dir: 1 };
const evSortKeys = {
  time: (e) => e.time || "",
  subject: (e) => eventSubject(e).toLowerCase(),
  kind: (e) => eventLabel(e).toLowerCase(),
  message: (e) => (e.message || "").toLowerCase(),
};
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

// eventListIsNarrowed reports that the visible log is not the cold default.
// Service, watch, kind, status and the exact dates are applied by the daemon
// before the rows arrive, so an empty allEvents cannot tell a quiet host from
// a filter that matched nothing. The error toggle and a window other than the
// last day do the same.
function eventListIsNarrowed() {
  for (const field of eventFilterFields) {
    if (field.key === "range") continue;
    if (eventFilterValue(field).trim()) return true;
  }
  if ($("#event-errors") && $("#event-errors").checked) return true;
  const range = ($("#event-range") || {}).value || "";
  return range !== "" && range !== eventRangeDefault && range !== eventRangeAll;
}

function renderGlobalEventEmpty() {
  const text = eventListIsNarrowed() ? "No events in this view." : "No events yet.";
  // The global log always has the subject column, so the note spans all four.
  return tpl`<tr><td colspan="4" class="muted">${text}</td></tr>`;
}

function renderGlobalEvents() {
  const tbody = $("#events");
  if (!tbody) return;
  updateSortHeaders("events");
  const events = sortedEvents(eventsWithinDateBounds(allEvents || []));
  const cnt = $("#event-count");
  if (cnt) cnt.textContent = events.length ? String(events.length) : "";
  const more = $("#event-more");
  if (more) more.hidden = !eventHasMore;
  syncEventTargetFilters();
  const grouped = $("#event-group") && $("#event-group").checked;
  if (!grouped) {
    if (!events.length) {
      litRender(renderGlobalEventEmpty(), tbody);
      return;
    }
    litRender(eventRows(events, true, { prefix: "global" }), tbody);
    return;
  }
  const groups = groupedEvents(events);
  if (!groups.length) {
    litRender(renderGlobalEventEmpty(), tbody);
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
  // Rebuilding the options of an open select closes it under the operator, so
  // they are replaced only when the names changed.
  if ([...select.options].slice(1).map((option) => option.value).join("\n") === values.join("\n")) return;
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
// titles only.
function fmtLocalTime(value) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "" : date.toLocaleString();
}

// fmtTimeTitled shows how long ago an event happened. The hover keeps the
// daemon's canonical UTC and the viewer's local time, so the short age never
// hides the instant.
function fmtTimeTitled(value) {
  const utc = fmtTime(value);
  if (!utc) return "";
  const local = fmtLocalTime(value);
  const title = local ? `${utc} · ${local} local` : utc;
  return tpl`<span title="${title}" data-clock-since=${value} .textContent=${clockSince(value, Date.now()) || utc}></span>`;
}

function eventRows(events, withService, opts = {}) {
  const cols = withService ? 4 : 3;
  if (!events || !events.length) return tpl`<tr><td colspan="${cols}" class="muted">No events yet.</td></tr>`;
  const prefix = opts.prefix || "event";
  return events.map((e, i) => {
    const who = eventSubject(e) || "system";
    const detail = eventDetail(e);
    const key = eventKey(prefix, e, i);
    const rowId = opts.panelId && i === 0 ? opts.panelId : nothing;
    return tpl`<tr id="${rowId}">
      <td class="t">${fmtTimeTitled(e.time)}</td>
      ${withService ? tpl`<td>${who}</td>` : nothing}
      <td class="event-kind">${eventBadge(e)}</td>
      <td><span class="event-kind-mobile">${eventBadge(e)} · </span>${detail ? tpl`<div class="muted event-detail">${detail}</div>` : nothing}${eventMessageHTML(e, key)}</td>
    </tr>`;
  });
}

function eventBadge(e) {
  return tpl`<span class="kind activity-${activitySeverity(e.kind, e.status, e.severity)}">${eventLabel(e)}</span>`;
}

// A recovery closes one incident; its stored severity is historical. Only an
// explicit operation outcome can say that an action completed.
function eventLabel(e) {
  if (e.kind === eventKindRecovered) return "Recovered";
  const action = capFirst((e.action || e.kind || "event").replaceAll(/[-_]/g, " "));
  if (e.kind === eventKindDryRun) return e.action ? `${action} · Dry run` : "Dry run";
  if (e.status === eventStatusOK) return `${action} completed`;
  if (e.status === eventStatusBlocked || [eventKindSuppressed, eventKindPanicSuppressed].includes(e.kind)) {
    return e.action ? `${action} blocked` : "Blocked";
  }
  if (e.action && (activityCriticalStatuses.includes(e.status) || e.kind === eventKindError || String(e.kind || "").endsWith("-failed"))) return `${action} failed`;
  if (e.status === operationStateRunning) return `${action} running`;
  if ([actionAlert, eventKindFiring, eventKindError].includes(e.kind)) {
    return capFirst(severityLevels.includes(e.severity) ? e.severity : "error");
  }
  return e.status ? `${action} · ${e.status}` : action;
}

function eventDetail(e) {
  const recovered = e.kind === eventKindRecovered;
  const parts = [e.rule ? (recovered ? `Rule condition cleared: ${e.rule}` : e.rule) : ""];
  if (severityLevels.includes(e.severity) && (recovered || ![actionAlert, eventKindFiring, eventKindError].includes(e.kind))) {
    parts.push(`${recovered ? "Previous incident severity" : "Incident severity"}: ${e.severity}`);
  }
  if (activityCriticalStatuses.includes(e.status) && e.status !== targetStateFailed) parts.push(e.status);
  return parts.filter(Boolean).join(" · ");
}

function renderEventsLoading(target, cols = 3) {
  if (target && !target.childNodes.length) {
    litRender(tpl`<tr><td colspan="${cols}" class="muted">loading…</td></tr>`, target);
  }
}

// fmtMonitorSource names who last changed a target's monitoring state. The
// values mirror the daemon's state store: config at startup, an operator through
// sermoctl or the web UI, and the two paused-for-later-restore paths a manual
// stop and a storage umount take.
function fmtMonitorSource(src) {
  switch (src) {
    case "cli": return "via sermoctl";
    case "web": return "via web UI";
    case "config": return "via config";
    case "cli-manual-stop": return "via sermoctl stop";
    case "web-manual-stop": return "via web UI stop";
    case "cli-mount-umount": return "via sermoctl umount";
    case "web-mount-umount": return "via web UI umount";
    default: return src ? "via " + src : "";
  }
}

function unitCell(s) {
  // The init backend is system-wide (shown once in the daemon status), so the
  // per-row cell shows only the unit.
  const unit = s.unit ? tpl`<span class="mono" title="${s.unit}">${s.unit}</span>` : mutedDash;
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
  if (state === targetStatePaused) return mutedDash;
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
  return activityDateCell(s && s.last_event, true, lastActivityMaxAgeMs);
}

function lastEventTime(item) {
  return (item && item.last_event && item.last_event.time) || "";
}

// Recovery and operation outcomes take precedence over the incident grade,
// which describes the original problem rather than the action's result.
function activitySeverity(kind, status, severity) {
  const k = String(kind || "").toLowerCase();
  const st = String(status || "").toLowerCase();
  if (k === eventKindRecovered) return healthStatusOK;
  if (k === eventKindDryRun) return healthStatusMuted;
  if (st === eventStatusBlocked) return healthStatusWarningShort;
  // An error an advisory incident raised (its check became unavailable) is
  // not an outage: it reads at its grade.
  if (k === eventKindError && isAdvisorySeverity(severity)) {
    return severity === targetStateWarning ? healthStatusWarningShort : healthStatusInfo;
  }
  if (activityCriticalStatuses.includes(st)) return healthStatusCriticalShort;
  if (st === operationStateRunning) return healthStatusInfo;
  if (st === eventStatusOK) return healthStatusOK;
  if (st || [eventKindAction, eventKindCascade].includes(k)) return healthStatusMuted;
  if ([actionAlert, eventKindFiring, eventKindError].includes(k)) {
    if (severity === severityCritical) return healthStatusCriticalSeverity;
    if (severity === targetStateWarning) return healthStatusWarningShort;
    return isAdvisorySeverity(severity) ? healthStatusInfo : healthStatusCriticalShort;
  }
  if (activityCriticalKinds.includes(k)) return healthStatusCriticalShort;
  if (activityWarningKinds.includes(k)) return healthStatusWarningShort;
  if (activityOKKinds.includes(k)) return healthStatusOK;
  if (activityInfoKinds.includes(k)) return healthStatusInfo;
  return healthStatusMuted;
}

// operationOutcome classifies an operation result status, in step with
// activitySeverity: ok, blocked (the engine refused before acting) or failed —
// every other status, the critical ones and any it does not know. "" for none.
function operationOutcome(status) {
  const st = String(status || "").toLowerCase();
  return !st || st === eventStatusOK || st === eventStatusBlocked ? st : targetStateFailed;
}

// operationMarkOutcome is the outcome a button mark shows: blocked or failed,
// "" for ok or none.
function operationMarkOutcome(status) {
  const outcome = operationOutcome(status);
  return outcome === eventStatusOK ? "" : outcome;
}

function activityDateCell(e, relative = true, maxAgeMs) {
  const time = e && e.time;
  if (!time || Number.isNaN(Date.parse(time))) return mutedDash;
  const kind = e.kind || "";
  const detail = kind || e.action || e.status ? [eventLabel(e), eventDetail(e)].filter(Boolean).join(" · ") : "";
  const local = fmtLocalTime(time);
  const title = [fmtTime(time), local && local + " local", detail, e.message || ""].filter(Boolean).join(" · ");
  const cls = "activity-time activity-" + activitySeverity(kind, e.status, e.severity);
  const label = [fmtTime(time), detail || "activity"].filter(Boolean).join(" · ");
  return tpl`<div class="event-cell" title="${title}"><span class="${cls}" aria-label="${label}" data-clock-since=${relative ? time : nothing} data-clock-max-age=${maxAgeMs ?? nothing} .textContent=${relative ? clockSince(time, Date.now(), maxAgeMs) : fmtTime(time)}></span></div>`;
}

function nextRemediationCell(s) {
  if (!s.enabled) return tpl`<span class="muted">disabled</span>`;
  const state = s.remediation_state || "";
  if (s.next_eligible_at) {
    return tpl`<span title="${fmtTime(s.next_eligible_at)}">${fmtUntilShort(s.next_eligible_at)}</span>`;
  }
  if (state === policyStateEligible) return tpl`<span class="ok">now</span>`;
  if (state === policyStatePending) return tpl`<span class="muted">${state}</span>`; // paused -> "—" (shown in State)
  return mutedDash;
}

// Dashboard data: the latest fetched lists, so a filter or sort change
// re-renders from cache without a refetch.
let allServices = [];
let latestSessionInventory = {};
// The longest-running statement is the one an operator came to see.
let dbQuerySort = { key: "running", dir: -1 };
const expandedDBQueries = new Set();
// svcPinned lifts hand-picked services to the top of their panel (and group),
// persisted with the rest of the UI state.
let svcPinned = new Set();
let allMounts = [];
let expanded = new Set(); // open expansions, keyed "svc:<name>" / "wat:<name>" / "app:<name>"
let allApps = [];
let allLibraries = [];
const defaultMetricWindow = "24h";
const serviceMetricStates = new Map();
let daemonMetricWindow = defaultMetricWindow;
let allWatches = [];
let globalTargetsByValue = new Map();
let globalTargetSyncPending = false;
let globalTargetSignature = "";

// ---- list panels ------------------------------------------------------------
// A list panel is one filterable, sortable, groupable table. Its descriptor
// names the columns (header and sort key), the status chips and the row
// renderer; the search/status/group filters, the grouping and sort state, the
// toolbar, the header and their persistence are shared (renderListPanel and
// friends), so a new panel is a descriptor rather than a parallel set of
// setters, bindings and saved fields.
//
//   key      the panel's data-panel attribute and its saved-state key
//   id       prefix of the generated control ids (<id>-search, <id>-filters, …)
//   label    plural noun used in the controls' accessible names
//   columns  [{ key, label, title, sort }]; a column without sort is not sortable
//   states   status chip values; stateOf(item) is the state a chip counts
//   groupOf  the group (category) an item belongs to
//   text     lowercase search haystack of an item
//   items    the panel's source list; row(item) its lit rows; rowId its row id
//   render   renders the panel (and the panels that share its data)
function listPanel(def) {
  return {
    query: "", status: filterAll, category: filterAll, grouped: !!def.alwaysGrouped,
    collapsedGroups: new Set(), sort: { key: "", dir: 1 },
    groupBy: "category", allGroupsLabel: "all categories", dom: null,
    ...def,
  };
}

const categoryColumn = (fallback) => ({ key: "category", label: "Category", sort: (item) => categoryOf(item, fallback).toLowerCase() });
const nameSort = (item) => displayName(item).toLowerCase();

function servicePanel(key, id, surface, nameLabel, label) {
  return listPanel({
    key, id, label, surface,
    columns: [
      { key: "name", label: nameLabel, sort: nameSort },
      { key: "state", label: "State", sort: (s) => stateRank(serviceDisplayState(s)) },
      { key: "uptime", label: "Uptime", sort: (s) => numericSortValue(s.uptime_seconds) },
      { key: "cpu", label: "CPU total", sort: (s) => (s.cpu_ready ? numericSortValue(s.cpu) : 0) },
      { key: "memory", label: "Memory", sort: (s) => numericSortValue(s.rss) },
      { key: "fds", label: "FDs", sort: (s) => numericSortValue(s.fds) },
      { key: "io", label: "IO R/W", sort: (s) => numericSortValue(s.io_read) + numericSortValue(s.io_write) },
      { key: "last", label: "Last activity", sort: lastEventTime },
      { key: "strays", label: "Strays", title: "Processes in the service's control group that no selector claims", sort: serviceStrayCount },
      { key: "actions", label: "Actions" },
    ],
    states: serviceStatusFilterStates,
    stateOf: serviceDisplayState,
    groupOf: (s) => categoryOf(s, defaultCategoryService),
    text: serviceSearchText,
    items: () => (allServices || []).filter((s) => serviceSurfaceOf(s) === surface),
    row: serviceRowHTML,
    rowId: (s) => `svc-row-${s.name}`,
    order: orderServices,
    render: renderServices,
  });
}

function artifactPanel(def) {
  return listPanel({
    stateOf: appStateText,
    groupOf: (item) => categoryOf(item, def.surface),
    text: (item) => artifactSearchText(item, def.surface),
    row: (item) => renderArtifactRow(item, listPanels[def.key]),
    rowId: (item) => `${def.id}-row-${item.name}`,
    ...def,
  });
}

const artifactColumns = (nameLabel, surface) => [
  { key: "name", label: nameLabel, sort: nameSort },
  ...(surface === defaultCategoryApp ? [categoryColumn(surface)] : []),
  { key: "state", label: "Status", sort: appStateRank },
  { key: "version", label: "Version", sort: (item) => (item.version_short || item.version || "").toLowerCase() },
];

const watchPanelDescriptor = watchPanelDescriptors[0];

const listPanels = {
  services: servicePanel("services", "svc", serviceSurfaceRegular, "Service", "services"),
  containers: servicePanel("containers", "container", serviceSurfaceContainer, "Container", "containers"),
  vms: servicePanel("vms", "vm", serviceSurfaceVM, "VM", "virtual machines"),
  // Watches are always grouped (system, storage, network, security): each
  // group holds one table per check type, with that type's own columns.
  [watchPanelDescriptor.key]: listPanel({
    key: watchPanelDescriptor.key, id: "watch", label: "watches",
    alwaysGrouped: true, groupBy: "group", allGroupsLabel: "all watch groups",
    typeSorts: {}, typeFilters: {},
    states: [targetStateDisabled, targetStateOK, targetStateStarting, targetStateWarning, targetStateStale, targetStateFailed],
    counts: watchFilterCounts,
    statusMatches: watchMatchesStatus,
    groupOf: watchGroupOf,
    text: watchSearchText,
    items: () => allWatches || [],
    body: (list) => renderWatchGroups(list),
    rowId: (w) => `wat-row-${w.name}`,
    order: (list) => list.sort((a, b) => compareSortValues(nameSort(a), nameSort(b))),
    refresh: "watches", refreshLabel: "Run an extra check of every enabled watch now",
    render: renderWatches,
  }),
  // The sessions panel shares the toolbar and its state; its two tables
  // (terminal sessions and database statements) are its own.
  sessions: listPanel({
    key: "sessions", id: "session", label: "sessions", statusLabel: "type",
    countUnit: "row",
    states: sessionTypeFilterStates,
    render: renderSessions,
  }),
  mounts: listPanel({
    key: "mounts", id: "mount", label: "mount units", groupBy: "group", allGroupsLabel: "all groups",
    columns: [
      { key: "name", label: "Name", sort: nameSort },
      { key: "path", label: "Path", sort: (m) => (m.path || "").toLowerCase() },
      { key: "mounted", label: "Mounted", sort: mountStateRank },
      { key: "refcount", label: "Refcount", sort: (m) => numericSortValue(m.refcount) },
      { key: "processes", label: "Processes", sort: (m) => mountBlockers(m).length },
      { key: "users", label: "Users", sort: (m) => mountUserNames(m).length },
      { key: "state", label: "State", sort: mountStateRank },
      { key: "actions", label: "Actions", hideLabel: true },
    ],
    states: mountStatusFilterStates,
    stateOf: mountStateText,
    statusMatches: (m, status) => mountStateText(m) === status || mountBaseStateText(m) === status,
    groupOf: (m) => categoryOf(m, defaultCategoryStorage),
    text: mountSearchText,
    items: () => allMounts || [],
    row: mountRow,
    rowId: mountRowId,
    render: renderMounts,
  }),
  apps: artifactPanel({
    key: "apps", id: "app", label: "applications", surface: defaultCategoryApp,
    columns: [...artifactColumns("Application", defaultCategoryApp), { key: "last", label: "Last activity", sort: lastEventTime }],
    states: appStatusFilterStates,
    items: () => allApps || [],
    detailLabel: "application details",
    expansionKey: appExpansionKey,
    renderExpansion: renderAppExpansion,
    refresh: "applications", refreshLabel: "Re-check every application now",
    render: renderApps,
  }),
  libraries: artifactPanel({
    key: "libraries", id: "library", label: "libraries", surface: defaultCategoryLibrary,
    columns: artifactColumns("Library", defaultCategoryLibrary),
    states: libraryStatusFilterStates,
    items: () => allLibraries || [],
    detailLabel: "library details",
    expansionKey: libraryExpansionKey,
    renderExpansion: renderLibraryExpansion,
    refresh: "libraries", refreshLabel: "Re-check every library now",
    render: renderLibraries,
  }),
};
const watchPanel = listPanels[watchPanelDescriptor.key];
const sessionPanel = listPanels.sessions;
const servicePanels = [listPanels.services, listPanels.containers, listPanels.vms];

function servicePanelOf(s) {
  const surface = serviceSurfaceOf(s);
  return servicePanels.find((p) => p.surface === surface) || listPanels.services;
}

// targetKinds is what the dashboard can address by name: the global search
// lists them, a #<prefix><name> fragment opens one, and each knows the panel
// that shows it. A mount has no expansion, so no prefix.
const targetKinds = [
  { kind: globalTargetService, prefix: expansionPrefixService, items: () => allServices || [], panelOf: servicePanelOf, render: renderServices },
  { kind: globalTargetWatch, prefix: expansionPrefixWatch, items: () => allWatches || [], panelOf: () => watchPanel, render: renderWatches },
  { kind: globalTargetApplication, prefix: expansionPrefixApp, items: () => allApps || [], panelOf: () => listPanels.apps, render: renderApps },
  { kind: globalTargetLibrary, prefix: expansionPrefixLibrary, items: () => allLibraries || [], panelOf: () => listPanels.libraries, render: renderLibraries },
  { kind: globalTargetMount, prefix: "", items: () => allMounts || [], panelOf: () => listPanels.mounts, render: renderMounts },
];

function targetKindOfKey(key) {
  return targetKinds.find((k) => k.prefix && key.startsWith(k.prefix)) || null;
}

function panelFilterActive(p) {
  return !!p.query || p.status !== filterAll || p.category !== filterAll;
}

function panelMatches(p, item) {
  if (p.category !== filterAll && p.groupOf(item) !== p.category) return false;
  if (p.status !== filterAll && !(p.statusMatches ? p.statusMatches(item, p.status) : p.stateOf(item) === p.status)) return false;
  return !p.query || p.text(item).includes(p.query);
}

// updatePanel is the one way a control changes a panel: apply, re-render, save.
function updatePanel(p, patch) {
  Object.assign(p, patch);
  p.render();
  saveUIState();
}

function resetPanelFilters(p) {
  p.query = "";
  p.status = filterAll;
  p.category = filterAll;
}

function togglePanelGroup(p, group) {
  if (!group) return;
  if (p.collapsedGroups.has(group)) p.collapsedGroups.delete(group);
  else p.collapsedGroups.add(group);
  updatePanel(p, {});
}

// toggleAllPanelGroups collapses every group the current filter shows, or
// expands them all when they already are collapsed.
function toggleAllPanelGroups(p) {
  const groups = sortedGroupValues(p.items().filter((item) => panelMatches(p, item)), p.groupOf);
  const allCollapsed = groups.length > 0 && groups.every((group) => p.collapsedGroups.has(group));
  groups.forEach((group) => (allCollapsed ? p.collapsedGroups.delete(group) : p.collapsedGroups.add(group)));
  updatePanel(p, {});
}

function sortArrow(sort, key) {
  if (sort.key !== key) return "";
  return sort.dir > 0 ? " ▲" : " ▼";
}

function sortAriaValue(sort, key) {
  if (!key || key !== sort.key) return "none";
  return sort.dir > 0 ? "ascending" : "descending";
}

// sortScopes are the sortable tables: every list panel with columns, by its
// key, and the hand-written tables that keep a sort of their own. A header
// cell names its scope through the closest data-sort-scope.
const sortScopes = {
  "db-queries": { get sort() { return dbQuerySort; }, render: () => renderSessions() },
  events: { get sort() { return evSort; }, render: () => renderGlobalEvents() },
};

function sortScope(scope) {
  return listPanels[scope] || sortScopes[scope] || null;
}

// toggleSort flips the direction when the same column is re-selected, otherwise
// selects the new column ascending, then re-renders.
function toggleSort(scope, key) {
  const owner = sortScope(scope);
  if (!owner || !key) return;
  const sort = owner.sort;
  if (sort.key === key) sort.dir = -sort.dir;
  else { sort.key = key; sort.dir = 1; }
  owner.render();
  saveUIState();
}

// updateSortHeaders refreshes the arrow and aria-sort of a hand-written
// table's headers; a list panel's header is re-rendered with them instead.
function updateSortHeaders(scope) {
  const owner = sortScope(scope);
  if (!owner) return;
  document.querySelectorAll(`[data-sort-scope="${scope}"] > thead th.sortable[data-sort]`).forEach((th) => {
    const key = th.dataset.sort || "";
    th.tabIndex = 0;
    th.setAttribute("aria-sort", sortAriaValue(owner.sort, key));
    const ind = th.querySelector(".sort-ind");
    if (ind) ind.textContent = sortArrow(owner.sort, key);
  });
}

function renderPanelHead(p) {
  if (!p.columns || !p.dom.head) return;
  litRender(tpl`<tr>${p.columns.map((c) => (c.sort
    ? tpl`<th scope="col" class="sortable" tabindex="0" data-col="${c.key}" data-sort="${c.key}" title="${c.title || nothing}" aria-sort="${sortAriaValue(p.sort, c.key)}">${c.label}<span class="sort-ind">${sortArrow(p.sort, c.key)}</span></th>`
    : tpl`<th scope="col" class="${c.key}" data-col="${c.key}">${c.hideLabel ? tpl`<span class="visually-hidden">${c.label}</span>` : c.label}</th>`))}</tr>`, p.dom.head);
}

function countLabel(count, unit) {
  return `${count} ${unit}${count === 1 ? "" : "s"}`;
}

// filterChips renders a status filter: "all" plus one chip per state with its
// count. A state nothing is in stays out of the row, unless it is the selected
// one — an emptied selection must remain visible to be switched away from.
function filterChips(states, counts, active, countUnit) {
  return [filterAll, ...states].map((state) => {
    const pressed = state === active;
    const n = counts[state] || 0;
    const count = state === filterAll && countUnit ? countLabel(n, countUnit) : n;
    return tpl`<button type="button" data-filter="${state}" class="${pressed ? "f-active" : ""}" aria-pressed="${pressed ? domBoolTrue : domBoolFalse}" ?hidden=${!n && state !== filterAll && !pressed}>${state.replace(/_/g, " ")} <span class="muted">${count}</span></button>`;
  });
}

function panelGroupControls(p, view) {
  const available = view.visibleGroups.length > 1;
  const select = view.groups.size > 1
    ? tpl`<select id="${p.id}-category" data-panel-category title="Filter by ${p.groupBy}" aria-label="Filter ${p.label} by ${p.groupBy}">
        <option value="${filterAll}" .selected=${p.category === filterAll}>${p.allGroupsLabel}</option>
        ${[...view.groups.keys()].map((group) => tpl`<option value="${group}" .selected=${p.category === group}>${group} (${view.groups.get(group)})</option>`)}
      </select>`
    : nothing;
  if (!available) return select;
  const allCollapsed = view.visibleGroups.every((group) => p.collapsedGroups.has(group));
  const groupTitle = p.grouped ? `Ungroup ${p.label}` : `Group ${p.label} by ${p.groupBy}`;
  const allTitle = allCollapsed ? `Expand all ${p.label} groups` : `Collapse all ${p.label} groups`;
  return tpl`${select}
    ${p.alwaysGrouped ? nothing : tpl`<button type="button" id="${p.id}-group-toggle" class="icon-btn" data-panel-group title="${groupTitle}" aria-label="${groupTitle}" aria-pressed="${p.grouped ? domBoolTrue : domBoolFalse}"><span aria-hidden="true">▦</span></button>`}
    <button type="button" id="${p.id}-groups-toggle" class="icon-btn" data-panel-groups ?disabled=${!p.grouped} title="${allTitle}" aria-label="${allTitle}"><span aria-hidden="true">${allCollapsed ? "▾" : "▴"}</span></button>`;
}

// renderPanelToolbar draws a panel's controls from its state. The search box
// carries no value binding: lit must not rewrite what is being typed, so the
// box is only brought back in step when the state changed behind it (a restore,
// a reset from the global search) and it is not the focused element.
function renderPanelToolbar(p, view) {
  const controls = p.dom.controls;
  if (!controls) return;
  const refresh = p.refresh && me.can_act
    ? tpl`<button type="button" id="${p.id}-refresh" class="icon-btn" data-refresh-panel="${p.refresh}" title="${p.refreshLabel}" aria-label="${p.refreshLabel}"><span aria-hidden="true">↻</span></button>`
    : nothing;
  litRender(tpl`
    <label for="${p.id}-search" class="visually-hidden">Filter ${p.label}</label>
    <input id="${p.id}-search" type="search" data-panel-search placeholder="Filter ${p.label}…" aria-describedby="search-shortcut-hint">
    <span id="${p.id}-filters" class="filters" role="group" aria-label="Filter ${p.label} by ${p.statusLabel || "state"}">${filterChips(p.states, view.counts, p.status, p.countUnit)}</span>
    <span class="panel-tools">
      <span id="${p.id}-count" class="muted">${panelFilterActive(p) ? `showing ${view.shown} of ${p.countUnit ? countLabel(view.total, p.countUnit) : view.total}` : ""}</span>
      ${panelGroupControls(p, view)}${refresh}
    </span>`, controls);
  const search = controls.querySelector("[data-panel-search]");
  if (search && search !== document.activeElement && search.value.trim().toLowerCase() !== p.query) search.value = p.query;
}

function groupCounts(items, groupOf) {
  const counts = new Map();
  items.forEach((item) => counts.set(groupOf(item), (counts.get(groupOf(item)) || 0) + 1));
  return new Map([...counts.entries()].sort((a, b) => a[0].localeCompare(b[0], undefined, { numeric: true, sensitivity: "base" })));
}

// setPanelPresence shows a panel only while it has something to list, and
// prints that total beside its title. Returns whether there is content.
function setPanelPresence(p, total) {
  setPanelVisible(p.dom.section, total > 0);
  if (p.dom.count) p.dom.count.textContent = total ? String(total) : "";
  return total > 0;
}

// renderListPanel is the whole life of a list panel's table: filter the source,
// sort it, group it, draw the toolbar, the header and the rows.
function renderListPanel(p) {
  if (!p.dom) return;
  const source = p.items();
  if (!setPanelPresence(p, source.length)) {
    litRender(nothing, p.dom.rows);
    return;
  }
  const groups = groupCounts(source, p.groupOf);
  // A single group cannot filter anything; drop a selection that would
  // otherwise filter invisibly, as would one naming a group that is gone.
  if (groups.size < 2 || !groups.has(p.category)) p.category = filterAll;
  const list = source.filter((item) => panelMatches(p, item));
  const column = p.columns && p.columns.find((c) => c.sort && c.key === p.sort.key);
  if (column) sortedBy(list, p.sort, { [column.key]: column.sort, name: nameSort }, "name");
  if (p.order) p.order(list, !!column);
  const visibleGroups = sortedGroupValues(list, p.groupOf);
  p.collapsedGroups.forEach((group) => { if (!visibleGroups.includes(group)) p.collapsedGroups.delete(group); });
  if (visibleGroups.length < 2 && !p.alwaysGrouped) p.grouped = false;
  p.dom.section.classList.toggle("panel-grouped", p.grouped);
  const counts = p.counts ? p.counts(source) : stateCounts(source, p.stateOf, p.states);
  renderPanelToolbar(p, { counts, groups, visibleGroups, shown: list.length, total: source.length });
  renderPanelHead(p);
  let content;
  if (!list.length) content = tpl`<tr><td colspan="${panelColumnCount(p)}" class="muted">No ${p.label} match the filter.</td></tr>`;
  else if (p.body) content = p.body(list);
  else content = p.grouped ? renderGroupedRows(p, list) : list.flatMap(p.row);
  litRender(content, p.dom.rows);
}

function panelColumnCount(p) {
  return p.columns ? p.columns.length : 1;
}

// renderGroupedRows draws a panel's rows under one collapsible heading per
// group; sorting by the category column also orders the groups.
function renderGroupedRows(p, list) {
  const groups = new Map();
  list.forEach((item) => {
    const group = p.groupOf(item);
    if (!groups.has(group)) groups.set(group, []);
    groups.get(group).push(item);
  });
  const dir = p.sort.key === "category" && p.sort.dir === -1 ? -1 : 1;
  return [...groups.entries()].sort((a, b) =>
    a[0].localeCompare(b[0], undefined, { numeric: true, sensitivity: "base" }) * dir
  ).map(([group, items]) => [groupHeaderRow(p, group, items), p.collapsedGroups.has(group) ? nothing : items.map(p.row)]);
}

function groupHeaderRow(p, group, items) {
  const collapsed = p.collapsedGroups.has(group);
  return tpl`<tr class="group-row">
    <td colspan="${panelColumnCount(p)}"><button type="button" class="row-toggle group-toggle" data-group-name="${group}" aria-expanded="${collapsed ? domBoolFalse : domBoolTrue}" aria-controls="${p.rowId(items[0])}" aria-label="${groupToggleAriaLabel(group, items.length, collapsed)}"><span class="exp" aria-hidden="true">${collapsed ? "▸" : "▾"}</span>${group} <span class="muted">${items.length}</span></button></td>
  </tr>`;
}

function panelOfElement(el) {
  const section = el && el.closest("[data-panel]");
  return (section && listPanels[section.dataset.panel]) || null;
}

function initListPanels() {
  for (const p of Object.values(listPanels)) {
    const section = document.querySelector(`[data-panel="${p.key}"]`);
    if (!section) continue;
    const table = section.querySelector(":scope > table");
    p.dom = {
      section,
      controls: section.querySelector(":scope > .panel-controls"),
      head: table && table.querySelector(":scope > thead"),
      rows: table && table.querySelector(":scope > tbody"),
      count: section.querySelector(".summary-title .muted"),
    };
    if (table) table.dataset.sortScope = p.key;
  }
}

const UI_STATE_KEY = "sermo-ui-state";
const KEYBOARD_SHORTCUTS_KEY = "sermo-keyboard-shortcuts";
const BROWSER_NOTIFY_KEY = "sermo-browser-notify";

function restoredSort(saved, fallback) {
  return saved && typeof saved.key === "string" ? { key: saved.key, dir: saved.dir === -1 ? -1 : 1 } : fallback;
}

function restorePanelState(p, saved) {
  if (!saved || typeof saved !== "object") return;
  if (typeof saved.query === "string") p.query = saved.query.trim().toLowerCase();
  if (p.states.includes(saved.status)) p.status = saved.status;
  if (typeof saved.category === "string") p.category = saved.category;
  if (typeof saved.grouped === "boolean" && !p.alwaysGrouped) p.grouped = saved.grouped;
  if (Array.isArray(saved.collapsedGroups)) p.collapsedGroups = new Set(saved.collapsedGroups);
  p.sort = restoredSort(saved.sort, p.sort);
  if (p.typeSorts && saved.typeSorts && typeof saved.typeSorts === "object") p.typeSorts = saved.typeSorts;
  if (p.typeFilters && saved.typeFilters && typeof saved.typeFilters === "object") p.typeFilters = saved.typeFilters;
}

function restoreUIState() {
  try {
    const raw = localStorage.getItem(UI_STATE_KEY);
    if (!raw) return;
    const s = JSON.parse(raw);
    const panels = s.panels && typeof s.panels === "object" ? s.panels : {};
    Object.values(listPanels).forEach((p) => restorePanelState(p, panels[p.key]));
    dbQuerySort = restoredSort(s.dbQuerySort, dbQuerySort);
    if (Array.isArray(s.svcPinned)) svcPinned = new Set(s.svcPinned);
    if (Array.isArray(s.expanded)) expanded = new Set(s.expanded.filter((k) => typeof k === "string"));
    if (s.serviceMetricStates && typeof s.serviceMetricStates === "object") {
      for (const [name, state] of Object.entries(s.serviceMetricStates)) {
        if (!state || typeof state !== "object") continue;
        serviceMetricStates.set(name, {
          window: typeof state.window === "string" ? state.window : defaultMetricWindow,
        });
      }
    }
    if (typeof s.daemonMetricWindow === "string") daemonMetricWindow = s.daemonMetricWindow;
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
  for (const field of eventFilterFields) {
    if (field.key !== "range" || ef.range) setVal(field.id, ef[field.key]);
  }
  // A rare filter still applied must not hide behind a closed disclosure.
  // The everyday row is the window and the error toggle; service, watch, kind,
  // status and the exact dates live under "more".
  const more = $("#event-more-controls");
  if (more && (ef.from || ef.to || ef.service || ef.watch || ef.kind || ef.status)) more.open = true;
  const err = $("#event-errors");
  if (err && typeof ef.onlyErrors === "boolean") err.checked = ef.onlyErrors;
  const grp = $("#event-group");
  if (grp && typeof ef.group === "boolean") grp.checked = ef.group;
}

function saveUIState() {
  try {
    localStorage.setItem(UI_STATE_KEY, JSON.stringify({
      panels: Object.fromEntries(Object.values(listPanels).map((p) => [p.key, {
        query: p.query, status: p.status, category: p.category, grouped: p.grouped,
        collapsedGroups: [...p.collapsedGroups], sort: p.sort, typeSorts: p.typeSorts, typeFilters: p.typeFilters,
      }])),
      dbQuerySort,
      serviceMetricStates: Object.fromEntries(serviceMetricStates), daemonMetricWindow,
      expanded: [...expanded],
      svcPinned: [...svcPinned],
      eventFilters: {
        ...Object.fromEntries(eventFilterFields.map((field) => [field.key, eventFilterValue(field)])),
        onlyErrors: !!($("#event-errors") && $("#event-errors").checked),
        group: !($("#event-group") && !$("#event-group").checked),
      },
    }));
  } catch (_) {}
}

// ---- views ------------------------------------------------------------------
// The page stacks every panel. A view is the group of panels carrying one
// data-view: what an overview tile reads and leads to.
const viewServices = "services";
const viewWatches = "watches";
const viewMounts = "mounts";
const viewSessions = "sessions";
const viewSoftware = "software";
const viewEvents = "events";
const viewDaemon = "daemon";

function viewPanels(view) {
  return [...document.querySelectorAll("[data-view]")].filter((el) => el.dataset.view === view);
}

// openView is what a tile does: it leads to the first panel of its view with
// something to list.
function openView(view) {
  revealPanel(viewPanels(view).find(panelVisible));
}

// revealPanel brings a panel on screen: opens it and, unless told not to,
// scrolls to it.
function revealPanel(section, scroll = true) {
  if (!section) return;
  if (section.tagName === "DETAILS") section.open = true;
  if (scroll) scrollToSection(section);
}

function sessionCount() {
  const inventory = latestSessionInventory || {};
  return (inventory.ssh || []).length + (inventory.terminal || []).length + (inventory.database || []).length;
}

// refreshChrome asks for the attention row, the favicon and the title to be
// brought up to date. They all derive from the same lists, and every renderer
// that changes one of those lists asks: a dashboard poll renders eight panels
// in a row, so the requests of one burst are answered once, in a microtask
// (not a frame: a background tab still owes its favicon and title).
let chromePending = false;
function refreshChrome() {
  if (chromePending) return;
  chromePending = true;
  queueMicrotask(() => {
    chromePending = false;
    renderAttention();
    updateTopbarHeight();
  });
}

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
// outcomeMarksSeen keeps each service's latest forecast and its pulse window,
// so polling does not replay the animation of an unchanged current condition.
const outcomeMarksSeen = new Map();

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

function serviceStateParts(s) {
  const st = serviceDisplayState(s);
  const indicators = (s && Array.isArray(s.observability_missing) && s.observability_missing.length)
    ? s.observability_missing.join(", ")
    : "";
  const missing = (st === targetStateCollecting && indicators) ? `Collecting ${indicators}` : "";
  const staleBinary = st === targetStateRestartRequired && s && s.state_reason === stateReasonStaleBinary;
  const staleLibraries = st === targetStateRestartRequired && s && s.state_reason === stateReasonStaleLibraries;
  const configurationInvalid = st === targetStateWarning && s && s.state_reason === stateReasonConfigurationInvalid;
  const configurationWarning = st === targetStateWarning && s && s.state_reason === stateReasonConfigurationWarning;
  const failedUnitLiveProcess = st === targetStateWarning && s && s.state_reason === stateReasonFailedUnitLiveProcess;
  const blind = (st === targetStateWarning && indicators && !staleBinary && !failedUnitLiveProcess)
    ? `The unit is active and its checks pass, but the daemon attributes no process to it, so ${indicators} stay unavailable`
    : "";
  const degraded = failedUnitLiveProcess
    ? "The init unit is failed, but the verified service process and its functional checks are healthy"
    : "";
  const invalidConfiguration = configurationInvalid
    ? "The service is active, but its configuration preflight currently fails"
    : configurationWarning ? "The configuration loads, but its test printed a warning worth reading" : "";
  const restartRequired = staleBinary
    ? "The running binary was replaced on disk; restart the service to run the installed version"
    : staleLibraries ? "The main process still maps replaced libraries; restart the service to load the installed versions" : "";
  const active = st === targetStateActive
    ? "Process confirmed; checks and runtime metrics are not available yet"
    : "";
  const title = missing || blind || degraded || invalidConfiguration || restartRequired || active;
  // An actionable state carries visible text beyond its colour. A tooltip is
  // invisible until hovered and unreachable on a touch screen.
  const reasonText = (st === targetStateWarning || st === targetStateRestartRequired)
    ? serviceStateReason(s)
    : "";
  const reason = reasonText
    ? tpl`<span class="muted state-reason" title="${title}">${reasonText}</span>`
    : nothing;
  const label = st === targetStateRestartRequired ? "restart required" : st;
  const badge = title ? tpl`<span title="${title}">${stateBadgeLabel(st, label)}</span>` : stateBadgeLabel(st, label);
  return { badge, reason, hasReason: !!reasonText };
}

function serviceStateBadge(s) {
  const state = serviceStateParts(s);
  return state.hasReason ? tpl`${state.badge} ${state.reason}` : state.badge;
}

// serviceStateReason phrases a state cause the dashboard needs to expose.
// The wording lives here, so the backend never has to agree on a sentence.
function serviceStateReason(s) {
  if (s && s.state_reason === stateReasonConfigurationInvalid) {
    return "configuration invalid";
  }
  if (s && s.state_reason === stateReasonConfigurationWarning) {
    return "configuration warning";
  }
  if (s && s.state_reason === stateReasonStaleBinary) {
    return "binary replaced on disk";
  }
  if (s && s.state_reason === stateReasonStaleLibraries) {
    return "libraries replaced on disk";
  }
  if (s && s.state_reason === stateReasonFailedUnitLiveProcess) {
    return "init unit failed; workload healthy";
  }
  return "no process attributed";
}

function serviceStateCell(s) {
  return tpl`${serviceStateBadge(s)}`;
}

function serviceSurfaceOf(s) {
  const category = categoryOf(s, defaultCategoryService).toLowerCase();
  if (category === dockerServiceCategory) return serviceSurfaceContainer;
  if (category === vmServiceCategory) return serviceSurfaceVM;
  return serviceSurfaceRegular;
}

function openSectionForService(name) {
  const service = (allServices || []).find((s) => s && s.name === name);
  const section = (service ? servicePanelOf(service) : listPanels.services).dom.section;
  revealPanel(section, false);
  return section;
}

function isFailing(s) { return serviceState(s) === targetStateFailed; }
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

// scrollToSection is the one spelling of "bring this panel into view".
function scrollToSection(el) {
  el && el.scrollIntoView({ block: scrollBlockStart, behavior: scrollBehaviorSmooth });
}

// statusTargets are the jump targets that also filter: the panels that can
// hold the state, and the state. Every other target is a section id.
const statusTargets = {
  "failed-services": [servicePanels, targetStateFailed],
  "failed-apps": [[listPanels.apps], targetStateFailed],
  "failed-watches": [[watchPanel], targetStateFailed],
  "warning-watches": [[watchPanel], targetStateWarning],
  "stale-watches": [[watchPanel], targetStateStale],
};

// statusTargetPanel picks, among a target's panels, the first that holds an
// item in the state — a failed container must open Containers, not Services —
// falling back to the first with anything to show.
function statusTargetPanel(panels, status) {
  const holds = (p) => {
    const probe = { ...p, query: "", category: filterAll, status };
    return p.items().some((item) => panelMatches(probe, item));
  };
  return panels.find(holds) || panels.find((p) => p.items().length) || panels[0];
}

function openPanelTarget(target) {
  const statusTarget = statusTargets[target];
  if (statusTarget) {
    const [panels, status] = statusTarget;
    const p = statusTargetPanel(panels, status);
    updatePanel(p, { status });
    revealPanel(p.dom.section);
    return;
  }
  revealPanel(document.getElementById(target));
}

function panelTargetLabel(target) {
  const statusTarget = statusTargets[target];
  if (statusTarget) {
    const [panels, status] = statusTarget;
    return `${statusTargetPanel(panels, status).label} panel, ${status.replace(/_/g, " ")} filter`;
  }
  const title = document.querySelector(`#${CSS.escape(target)} .summary-title`);
  const name = title && title.firstChild ? title.firstChild.textContent.trim().toLowerCase() : "";
  return `${name || "services"} panel`;
}

function globalTargetValue(kind, name) {
  return `${kind}: ${name}`;
}

function globalTargetRecords() {
  const records = targetKinds.flatMap((kind) => kind.items().map((item) => ({
    kind, name: item.name, label: displayName(item), item,
    rowID: kind.panelOf(item).rowId(item),
    value: globalTargetValue(kind.kind, item.name),
  })));
  return records.sort((a, b) => a.value.localeCompare(b.value));
}

function syncGlobalTargetSearch() {
  globalTargetSyncPending = false;
  const datalist = $("#target-search-options");
  if (!datalist) return;
  const records = globalTargetRecords();
  // The records carry this poll's items, so the map is always renewed; the
  // options are the same from one poll to the next unless a target came or went.
  globalTargetsByValue = new Map(records.map((record) => [record.value, record]));
  const signature = records.map((record) => `${record.value}\n${record.label}`).join("\n");
  if (signature === globalTargetSignature) return;
  globalTargetSignature = signature;
  const options = records.map((record) => {
    const option = new Option("", record.value);
    option.label = record.label === record.name ? record.kind.kind : `${record.label} · ${record.kind.kind}`;
    return option;
  });
  datalist.replaceChildren(...options);
}

function scheduleGlobalTargetSync() {
  if (globalTargetSyncPending) return;
  globalTargetSyncPending = true;
  queueMicrotask(syncGlobalTargetSearch);
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

// openGlobalTarget shows one target whatever is filtered, collapsed or in
// another view: its panel drops its filters and opens the target's group, and
// the target's fragment (or, for a mount, its panel) does the rest.
function openGlobalTarget(target) {
  if (!target) return;
  const p = target.kind.panelOf(target.item);
  resetPanelFilters(p);
  p.collapsedGroups.delete(p.groupOf(target.item));
  updatePanel(p, {});
  if (target.kind.prefix) {
    // applyHash does the rest for a fragment it has not applied yet.
    hashApplied = false;
    history.replaceState(null, "", "#" + target.kind.prefix + target.name);
    applyHash();
  } else {
    setFragment(p.dom.section.id);
    revealPanel(p.dom.section, false);
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
//   Sermo - web01
//   (3 groups) Sermo - web01
//   Sermo - web01 · starting
// Until healthIconReady, leave the bootstrap <title> alone.
function setDocumentTitle({ attentionCount = 0, starting = false } = {}) {
  if (!healthIconReady) return;
  const host = shortHostName(dashboardHostname);
  const base = host ? `Sermo - ${host}` : "Sermo";
  if (starting) {
    document.title = host ? `${base} · starting` : "Sermo · starting";
    return;
  }
  document.title = attentionCount > 0 ? `(${countLabel(attentionCount, "group")}) ${base}` : base;
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

// attentionNames lists the first few targets behind a signal.
const attentionPreviewNames = 4;
function attentionNames(list, nameOf) {
  const more = list.length > attentionPreviewNames ? ` and ${list.length - attentionPreviewNames} more` : "";
  return list.slice(0, attentionPreviewNames).map(nameOf).join(", ") + more;
}

// attentionSignals is everything that currently needs the operator: each
// signal names what, how bad and the jump target that shows it.
function attentionSignals() {
  const items = [];
  const signal = (level, list, title, nameOf, target) => {
    if (list.length) items.push({ level, title: title(list.length), detail: attentionNames(list, nameOf), target });
  };
  const named = (item) => displayName(item) || item.name;
  const lockLabel = (l) => [l.service, l.name].filter(Boolean).join(":");
  const failing = (allServices || []).filter(isFailing);
  const audibleWatches = (allWatches || []).filter((w) => !w.alarm?.silenced);
  const failingWatches = audibleWatches.filter(isWatchAttention);
  const failingApps = (allApps || []).filter((a) => appStateText(a) === targetStateFailed);
  signal(healthStatusCritical, failing, (n) => (n === 1 ? "1 service needs attention" : `${n} services need attention`), (s) => s.name, "failed-services");
  signal(healthStatusCritical, failingWatches, (n) => (n === 1 ? "1 watch firing" : `${n} watches firing`), named, "failed-watches");
  signal(healthStatusWarning, audibleWatches.filter(isWatchWarning), (n) => (n === 1 ? "1 watch warning" : `${n} watches warning`), named, "warning-watches");
  signal(healthStatusWarning, audibleWatches.filter(isWatchSampleStale), (n) => (n === 1 ? "1 watch sample is stale" : `${n} watch samples are stale`), named, "stale-watches");
  signal(healthStatusCritical, failingApps, (n) => (n === 1 ? "1 application failed" : `${n} applications failed`), named, "failed-apps");
  signal(healthStatusCritical, (latestLocks || []).filter((l) => l.state === lockStateActive), (n) => (n === 1 ? "1 active lock" : `${n} active locks`), lockLabel, "locks-section");
  signal(healthStatusWarning, (latestLocks || []).filter((l) => l.state === lockStateStale), (n) => (n === 1 ? "1 stale lock" : `${n} stale locks`), lockLabel, "locks-section");
  if (latestReady && latestReady.ready === false && latestReady.status === daemonStatusShuttingDown) {
    items.push({ level: healthStatusWarning, title: "Daemon shutting down", detail: latestReady.message || latestReady.status || "", target: "daemon-section" });
  }
  // Recent errors are an advisory, not a critical signal. The daemon counts the
  // errors still standing — one a later recovery or successful operation of the
  // same target settled is history — and names their targets; even so, they
  // come from the event log, not from current state, so they never drive the
  // overall status red. A currently-failing target turns the favicon red
  // through its own path (failed services/watches/apps).
  if (latestActivity && (latestActivity.errors || 0) > 0) {
    items.push({
      level: healthStatusWarning,
      title: latestActivity.errors === 1 ? "1 recent error" : `${latestActivity.errors} recent errors`,
      detail: attentionNames(latestActivity.error_targets || [], (name) => name) || "see events",
      target: "events-section",
    });
  }
  return { items, failing, failingWatches, failingApps };
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
  const { items, failing, failingWatches, failingApps } = attentionSignals();
  maybeNotifyAttention(failing, failingWatches, failingApps);
  // While the daemon is settling (starting), the tab favicon is neutral grey and
  // other health signals are premature, so it overrides the ok/warning/critical
  // colour. Startup progress lives in the status line (`daemon starting`).
  const startingNow = latestReady && latestReady.ready === false && latestReady.status === daemonStatusStarting;
  if (startingNow) {
    setFavicon(targetStateStarting);
    setDocumentTitle({ starting: true });
  } else if (!items.length) {
    setFavicon(healthStatusOK);
    setDocumentTitle({ attentionCount: 0 });
  } else {
    setFavicon(items.some((it) => it.level === healthStatusCritical) ? healthStatusCritical : healthStatusWarning);
    setDocumentTitle({ attentionCount: items.length });
  }
  box.classList.toggle("live-hidden", !items.length);
  // One line per signal — what, and who — with the failing ones first; the
  // card's edge takes the colour of the worst.
  const critical = items.filter((it) => it.level === healthStatusCritical);
  const ordered = [...critical, ...items.filter((it) => it.level !== healthStatusCritical)];
  box.classList.toggle("attn-warning", items.length > 0 && !critical.length);
  const html = items.length ? `<span class="attn-head">Needs attention <span class="muted">${countLabel(items.length, "group")}</span></span>
    <span class="attn-list">${ordered.map((it) => `
      <button type="button" class="attn-item ${esc(it.level)}" data-panel-target="${esc(it.target)}" aria-label="${esc(attnAriaLabel(it))}">
        <span class="attn-dot" aria-hidden="true"></span>
        <span class="attn-title">${esc(it.title)}</span>
        <span class="attn-detail" title="${esc(it.detail || "")}">${esc(it.detail || "")}</span>
      </button>`).join("")}</span>` : "";
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
  return op.outcome === eventStatusOK ? operationStateCompleted : op.outcome;
}
function beginOperation(name, action) {
  liveOps.set(name, {
    name,
    action,
    started: Date.now(),
    finished: 0,
    outcome: "",
    message: "operation in progress",
  });
  ensureLiveOpsTimer();
  updateLiveOps();
}
// finishOperation records an operation's outcome (operationOutcome): ok,
// blocked or failed.
function finishOperation(name, outcome, message) {
  const op = liveOps.get(name) || { name, action: "operation", started: Date.now() };
  op.finished = Date.now();
  op.outcome = outcome;
  op.message = message || opStateText(op);
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
    const since = op.finished ? `${fmtDuration(opElapsed(op))} total` : `${fmtDuration(opElapsed(op))} elapsed`;
    return `<div class="op-card">
      <span class="op-dot ${op.outcome}" aria-hidden="true"></span>
      <b>${esc(op.action)}</b>
      <span>${esc(op.name)}</span>
      <span class="${operationOutcomeStyles[op.outcome].cls}">${esc(state)}</span>
      <span class="muted">${esc(since)}</span>
      ${op.message ? `<span class="muted">${esc(op.message)}</span>` : ""}
    </div>`;
  }).join("");
  setHTMLIfChanged(box, html);
}

function serviceSearchText(s) {
  const monitorText = s.monitored ? "monitoring enabled" : "monitoring paused";
  const missing = Array.isArray(s.observability_missing) ? s.observability_missing.join(" ") : "";
  return `${displayName(s)} ${s.name || ""} ${s.display_name || ""} ${categoryOf(s, defaultCategoryService)} ${s.unit || ""} ${serviceDisplayState(s)} ${monitorText} ${missing}`.toLowerCase();
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
    case actionPause: return !!(busy || locked || st !== backendStatusActive);
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
    case actionPause: return st !== backendStatusActive ? "service is not running" : "";
    case actionReload:
      if (!s.can_reload) return "service does not support reload";
      return st !== backendStatusActive ? "service is not running" : "";
    default: return "";
  }
}

function actionHintID(kind, name, action, suffix = "hint") {
  return `${kind}-${name}-${action}-${suffix}`;
}

// hiddenDescription is the visually hidden text an aria-describedby names.
function hiddenDescription(id, text) {
  return tpl`<span id="${id}" class="visually-hidden">${text}</span>`;
}

function actionHint(id, disabled, reason) {
  if (!disabled || !reason) return nothing;
  return hiddenDescription(id, reason);
}

// actionDescribedBy names the disabled-reason hint and, when the button carries
// one, its other description (an operation outcome mark).
function actionDescribedBy(id, disabled, reason, otherID = "") {
  return [disabled && reason ? id : "", otherID].filter(Boolean).join(" ") || nothing;
}

function servicePowerAction(s) {
  const st = (s.status || backendStatusUnknown).toLowerCase();
  // A failed or inactive unit normally needs a guarded restart. Repair remains
  // an explicit fallback for residual init/runtime state, never the default.
  if (serviceRepairAvailable(s)) return actionRestart;
  return st === backendStatusActive || st === targetStatePaused ? actionStop : actionStart;
}

// serviceSuspendAction is the action the pause toggle sends: resume a paused
// VM or container, otherwise pause it.
function serviceSuspendAction(s) {
  return (s.status || "").toLowerCase() === targetStatePaused ? actionResume : actionPause;
}

// serviceSuspendButton is one button for pause and resume: the glyph stays the
// pause bars and only its pressed state changes, the way a media pause key
// latches. The accessible name still says which operation a press sends.
function serviceSuspendButton(s) {
  const action = serviceSuspendAction(s);
  return { action, glyphAction: actionPause, extraClass: "suspend-btn", pressed: action === actionResume };
}

const noForecasts = Object.freeze([]);

// markingForecasts lists the service's operation forecast entries that mark a
// button: an action the engine would refuse (blocked) or that would fail.
function markingForecasts(s) {
  const forecasts = s && s.operation_forecast;
  if (!Array.isArray(forecasts) || !forecasts.length) return noForecasts;
  return forecasts.filter((f) => f && f.action && operationMarkOutcome(f.status));
}

// A changed current forecast pulses once; initial snapshots stay still.
function noteOperationForecasts(services) {
  const pulseEnd = firstSnapshotDone ? Date.now() + outcomePulseMs : 0;
  for (const s of Array.isArray(services) ? services : []) {
    if (!s || !s.name) continue;
    let seen = outcomeMarksSeen.get(s.name);
    // A service seen for the first time (the first snapshot, or one a reload
    // added) records what it already carries without pulsing it.
    const pulseUntil = seen ? pulseEnd : 0;
    if (!seen) {
      seen = { forecast: "", forecastPulseUntil: 0 };
      outcomeMarksSeen.set(s.name, seen);
    }
    const forecastKey = markingForecasts(s).map((f) => `${f.action}:${f.status}#${f.message || ""}`).sort().join("|");
    if (forecastKey && forecastKey !== seen.forecast) {
      seen.forecast = forecastKey;
      seen.forecastPulseUntil = pulseUntil;
    }
  }
}

// operationForecastText is the one sentence for forecast entries that share an
// outcome and a reason: the actions, what the engine would do with them and why.
function operationForecastText(outcome, actions, message) {
  const list = actions.join(", ");
  return `${list.charAt(0).toUpperCase()}${list.slice(1)} ${operationOutcomeStyles[outcome].forecast}${message ? ": " + message : ""}`;
}

// operationForecastSentences groups forecast entries by outcome and reason, in
// their order, into one sentence each.
function operationForecastSentences(forecasts) {
  const groups = new Map();
  for (const f of forecasts) {
    const outcome = operationMarkOutcome(f.status);
    const message = f.message || "";
    const key = `${outcome}#${message}`;
    if (!groups.has(key)) groups.set(key, { outcome, message, actions: [] });
    groups.get(key).actions.push(f.action);
  }
  return [...groups.values()].map((g) => ({ outcome: g.outcome, text: operationForecastText(g.outcome, g.actions, g.message) }));
}

// noOperationMarks is the shared result for the common row without a mark;
// nothing adds to it.
const noOperationMarks = new Map();

// Buttons describe what an action would encounter now. Historical failures stay
// in Last operation and Events; they cannot override a fresh forecast.
function serviceOperationMarks(s, shown) {
  const forecasts = markingForecasts(s);
  if (!forecasts.length) return noOperationMarks;
  const seen = outcomeMarksSeen.get(s.name) || {};
  const now = Date.now();
  const marks = new Map();
  for (const f of forecasts) {
    if (!shown.includes(f.action) || marks.has(f.action)) continue;
    const outcome = operationMarkOutcome(f.status);
    marks.set(f.action, { outcome, text: operationForecastText(outcome, [f.action], f.message), pulse: seen.forecastPulseUntil > now });
  }
  return marks;
}

function expandToggleAriaLabel(name, open, subject) {
  return `${open ? "Collapse" : "Expand"} ${subject} for ${name}`;
}

// expandToggle is a row's disclosure: the chevron and the button, named after
// the row, that opens or closes its expansion. content is what the button
// shows when it is more than the name; subject says what the expansion holds.
function expandToggle(key, label, subject, content = label, cls = "row-toggle") {
  const open = expanded.has(key);
  return tpl`<span class="exp" aria-hidden="true">${open ? "▾" : "▸"}</span><button type="button" class="${cls}" data-exp-toggle="${key}" aria-expanded="${open}" aria-controls="${open ? "exp-" + key : nothing}" aria-label="${expandToggleAriaLabel(label, open, subject)}">${content}</button>`;
}

// expansionRow is the row an open expansion lives in, or null while it is
// closed. An expansion a loader fills later (a service's or a watch's detail)
// gets a cell with no binding of its own, so the table's render and the
// loader's never write to the same place; one that is plain markup is passed
// as content.
function expansionRow(key, cols, content) {
  if (!expanded.has(key)) return null;
  return content === undefined
    ? tpl`<tr class="exp-row" id="exp-${key}" data-exp="${key}"><td colspan="${cols}"><div class="exp-body"></div></td></tr>`
    : tpl`<tr class="exp-row" id="exp-${key}" data-exp="${key}"><td colspan="${cols}"><div class="exp-body">${content}</div></td></tr>`;
}

function groupToggleAriaLabel(category, count, collapsed) {
  return `${collapsed ? "Expand" : "Collapse"} ${category} group (${count} items)`;
}

// actionIcon is one stroke icon, the same 16px outline as the top bar. Row
// actions use it so a phone does not substitute a colour emoji for a character.
function actionIcon(content) {
  return svg`<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${content}</svg>`;
}

const iconPlay = actionIcon(svg`<polygon points="6 3 20 12 6 21 6 3"/>`);
const iconStop = actionIcon(svg`<rect x="5" y="5" width="14" height="14" rx="1"/>`);
const iconRestart = actionIcon(svg`<path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/>`);
const iconReload = actionIcon(svg`<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/>`);
const iconPause = actionIcon(svg`<rect x="6" y="4" width="4" height="16" rx="1"/><rect x="14" y="4" width="4" height="16" rx="1"/>`);
const iconMonitor = actionIcon(svg`<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/>`);
const iconUnmonitor = actionIcon(svg`<path d="M9.88 9.88a3 3 0 1 0 4.24 4.24"/><path d="M10.73 5.08A10.43 10.43 0 0 1 12 5c6.5 0 10 7 10 7a13.16 13.16 0 0 1-1.67 2.68"/><path d="M6.61 6.61A13.526 13.526 0 0 0 2 12s3.5 7 10 7a9.74 9.74 0 0 0 5.39-1.61"/><line x1="2" x2="22" y1="2" y2="22"/>`);
const iconBell = actionIcon(svg`<path d="M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9"/><path d="M10 21h4"/>`);
const iconBellOff = actionIcon(svg`<path d="M9 3a6 6 0 0 1 9 5c0 2 .3 4 .8 5M6 6v2c0 7-3 7-3 9h14M10 21h4M2 2l20 20"/>`);
const iconRepair = actionIcon(svg`<path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z"/>`);

// actionMeta is the one vocabulary of action buttons: the icon and, per
// surface, the accessible label. A surface without an entry falls back to
// "<action> <surface> <name>", so a new action renders before it is named
// here — it just reads generically until it is.
// The remaining text glyphs (mount, unmount, notifier test) carry U+FE0E, the
// text presentation selector: without it a phone draws them as colour emoji.
const glyphPlay = "▶\uFE0E";
const actionMeta = {
  [actionStart]: { glyph: iconPlay, service: (n) => `Start service ${n}` },
  [actionRepair]: { glyph: iconRepair, service: (n) => `Repair residual service state ${n}` },
  [actionStop]: { glyph: iconStop, service: (n) => `Stop service ${n}` },
  [actionRestart]: { glyph: iconRestart, service: (n) => `Restart service ${n}` },
  [actionResume]: { glyph: iconPlay, service: (n) => `Resume service ${n}`, watch: (n) => `Resume RAID reconstruction for watch ${n}` },
  [actionReload]: { glyph: iconReload, service: (n) => `Reload service ${n}` },
  [actionMonitor]: { glyph: iconMonitor, service: (n) => `Monitor service ${n}`, watch: (n) => `Monitor watch ${n}` },
  [actionUnmonitor]: { glyph: iconUnmonitor, service: (n) => `Unmonitor service ${n}`, watch: (n) => `Unmonitor watch ${n}` },
  [actionExpand]: { watch: (n) => `Expand storage for watch ${n}` },
  [actionSilence]: { watch: (n) => `Silence alarm for watch ${n} until its state changes` },
  [actionProbe]: { watch: (n) => `Probe watch ${n}` },
  [actionPause]: { glyph: iconPause, service: (n) => `Pause service ${n}`, watch: (n) => `Pause RAID reconstruction for watch ${n}` },
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
  const generation = dashboardGeneration;
  const s = (allServices || []).find((item) => item && item.name === name) || {};
  const meta = (s.buttons || []).find((b) => b && b.name === button) || {};
  const label = meta.label || button;
  const done = await runAction({
    confirm: () => promptConfirm({
      title: `${label}?`,
      message: `Run the configured "${label}" command for ${displayName(s) || name} now?`,
      okLabel: label,
      danger: true,
    }),
    request: () => fetch(serviceButtonAPI(name, button), targetPostOptions({}, generation)),
    ok: (body) => body.message || `${label}: ok`,
    fail: label,
    then: null,
  });
  if (done !== null) scheduleRefresh();
}

// serviceActionButton renders one service action button. opts.displayAction
// changes its presentation while keeping the engine action for dispatch.
// A toggle passes
// opts.glyphAction to keep one glyph across the actions it alternates,
// opts.pressed for its latched state and opts.extraClass for its styling.
// opts.mark (serviceOperationMarks) outlines the button amber (blocked) or red
// (failed) with a badge, pulsing while its window is open, and describes it.
function serviceActionButton(s, action, busy, compact = false, title = "", opts = {}) {
  const displayAction = opts.displayAction || action;
  const label = svcActionAriaLabel(s, displayAction);
  const glyph = compact ? serviceActionGlyph(opts.glyphAction || displayAction) : "";
  const disabled = serviceActionDisabled(s, action, busy);
  const reason = serviceActionDisabledReason(s, action, busy);
  const hintID = actionHintID("svc", s.name, action);
  const mark = opts.mark;
  const outcomeID = mark ? actionHintID("svc", s.name, action, "outcome") : "";
  const classes = [
    compact ? "icon-btn" : "", opts.extraClass || "",
    mark ? `outcome-${mark.outcome}` : "", mark && mark.pulse ? outcomePulseClass : "",
  ].filter(Boolean).join(" ");
  const pressed = "pressed" in opts ? (opts.pressed ? domBoolTrue : domBoolFalse) : nothing;
  const tip = [title || (compact ? label : ""), mark ? mark.text : ""].filter(Boolean).join("\n");
  const outcomeHint = mark ? hiddenDescription(outcomeID, mark.text) : nothing;
  const badge = mark ? tpl`<span class="outcome-badge" aria-hidden="true">${operationOutcomeStyles[mark.outcome].badge}</span>` : nothing;
  return tpl`${actionHint(hintID, disabled, reason)}${outcomeHint}<button class="${classes}" ?disabled=${disabled} data-service="${s.name}" data-service-action="${action}" aria-pressed="${pressed}" title="${tip || nothing}" aria-label="${label}" aria-describedby="${actionDescribedBy(hintID, disabled, reason, outcomeID)}">${glyph ? tpl`<span aria-hidden="true">${glyph}</span>` : displayAction}${badge}</button>`;
}

// serviceRowParts builds one service's main <tr> and its optional operator
// commands and expansion rows.
function serviceRowParts(s) {
  const state = serviceState(s);
  const label = displayName(s);
  const op = liveOps.get(s.name);
  const busy = serviceBusy(s);
  const stateParts = serviceStateParts(s);
  // With a liveOps entry the note names the action and its elapsed time. Without
  // one, operation_active still tells us the engine holds the lock, so the row
  // says so instead of looking idle while it churns.
  const busyText = op
    ? tpl`<div id="svc-${s.name}-busy" class="svc-busy ${operationOutcomeStyles[op.outcome].cls}" role="status" aria-live="polite">${op.action} ${opStateText(op)} · ${fmtDuration(opElapsed(op))}${op.message ? tpl` <span class="muted">${op.message}</span>` : nothing}</div>`
    : (s.operation_active
      ? tpl`<div id="svc-${s.name}-busy" class="svc-busy inactive" role="status" aria-live="polite">operation in progress</div>`
      : nothing);
  let actions;
  if (!s.enabled) {
    actions = tpl`<span class="muted">disabled in config</span>`;
  } else {
    const powerAction = servicePowerAction(s);
    const powerDisplayAction = (s.status || "").toLowerCase() === backendStatusInactive ? actionStart : powerAction;
    const alsoApply = (s.also_apply || []).length;
    const powerTitle = alsoApply
      ? `${svcActionAriaLabel(s, powerDisplayAction)}; also applies to: ${s.also_apply.join(", ")}`
      : svcActionAriaLabel(s, powerDisplayAction);
    // The row's buttons, in order; the operation outcome marks go on the same
    // list, so a mark only ever lands on a button the row shows. A space after
    // each keeps a gap wide enough for a neighbour's outcome badge.
    const buttons = [
      { action: powerAction, displayAction: powerDisplayAction, title: powerTitle },
      powerAction !== actionRestart && { action: actionRestart },
      s.can_pause && serviceSuspendButton(s),
      { action: actionReload },
      { action: s.monitored ? actionUnmonitor : actionMonitor },
      serviceRepairAvailable(s) && { action: actionRepair },
    ].filter(Boolean);
    const marks = serviceOperationMarks(s, buttons.map((b) => b.action));
    actions = me.can_act
      ? buttons.map((b) => tpl`${serviceActionButton(s, b.action, busy, true, b.title, { ...b, mark: marks.get(b.action) })} `)
      : readOnlyNote;
  }
  const hasCommands = s.enabled && me.can_act && (s.buttons || []).length > 0;
  const key = serviceExpansionKey(s.name);
  const pinned = svcPinned.has(s.name);
  const pin = tpl`<button type="button" class="icon-btn pin-btn${pinned ? " pinned" : ""}" data-service-pin="${s.name}" aria-pressed="${pinned ? domBoolTrue : domBoolFalse}" title="${pinned ? "Unpin from top" : "Pin to top"}" aria-label="${(pinned ? "Unpin " : "Pin ") + label + (pinned ? " from the top of the list" : " to the top of the list")}">${pinned ? "★" : "☆"}</button>`;
  const rowClass = serviceRowClass(state);
  const cols = panelColumnCount(listPanels.services);
  const main = tpl`<tr id="svc-row-${s.name}" class="clickable ${rowClass} ${hasCommands ? "has-commands" : ""}" data-exp-key="${key}">
    <td data-col="name"><div class="svc-main">${expandToggle(key, label, "service details", label, "name row-toggle")}${pin}</div>${stateParts.hasReason ? tpl`<div class="svc-state-note">${stateParts.reason}</div>` : nothing}${busyText}</td>
    <td data-col="state">${stateParts.badge}</td>
    <td data-col="uptime">${serviceUptimeCell(s)}</td>
    <td data-col="cpu">${serviceCpuCell(s)}</td>
    <td data-col="memory">${serviceMemCell(s)}</td>
    <td data-col="fds">${serviceFDsCell(s)}</td>
    <td data-col="io">${serviceIoCell(s)}</td>
    <td data-col="last">${lastEventCell(s)}</td>
    <td data-col="strays">${serviceStraysCell(s)}</td>
    <td data-col="actions" class="actions">${actions}</td>
  </tr>`;
  // The configured operator commands get a row of their own: three labelled
  // buttons stacked in the actions column made the service four lines tall,
  // and a full row lays them out on one.
  const commands = hasCommands
    ? tpl`<tr class="svc-commands-row ${rowClass}"><td colspan="${cols}"><div class="svc-commands">${serviceCustomButtons(s, busy)}</div></td></tr>`
    : null;
  return { main, commands, exp: expansionRow(key, cols) };
}

function serviceRowClass(state) {
  if (state === targetStateFailed) return "row-failing";
  if (state === targetStatePaused) return "row-paused";
  return [targetStateWarning, targetStateRestartRequired].includes(state) ? "row-warning" : "";
}

function serviceRowHTML(s) {
  const parts = serviceRowParts(s);
  return [parts.main, parts.commands, parts.exp].filter(Boolean);
}

// render receives fresh data on each refresh; cache it, then render through the
// active filter. Calls with no argument re-render the cache (filter changes).
function render(services) {
  if (services) {
    allServices = services;
    forgetGoneServiceExpansions();
    noteOperationForecasts(services);
  }
  scheduleGlobalTargetSync();
  renderServices();
  applyHash();
}

// orderServices is the service panels' order hook: failing services lead when
// no column is chosen (a stable sort keeps the backend order within each
// half), and pinned services lead whatever the order.
function orderServices(list, sorted) {
  if (!sorted) list.sort((a, b) => (isFailing(b) ? 1 : 0) - (isFailing(a) ? 1 : 0));
  if (svcPinned.size) list.sort((a, b) => (svcPinned.has(b.name) ? 1 : 0) - (svcPinned.has(a.name) ? 1 : 0));
}

function toggleServicePin(name) {
  if (!name) return;
  if (svcPinned.has(name)) svcPinned.delete(name);
  else svcPinned.add(name);
  saveUIState();
  renderServices();
}

function renderServices() {
  preserveExpansionCells(isServiceExpansionKey);
  servicePanels.forEach(renderListPanel);
  reassertExpansions();
  refreshChrome();
}

// renderExpansionTarget updates only the panel that owns an expansion. Keeping
// other lit-html roots untouched avoids invalidating a nested expansion while
// its own panel is rendering.
function renderExpansionTarget(key) {
  const kind = targetKindOfKey(key);
  if (kind) kind.render();
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
// setFragment writes the address bar's fragment for a navigation the dashboard
// has just carried out itself — an expansion opened or closed, a panel shown —
// so that applyHash, which runs after every render, does not carry it out a
// second time and pull the operator back from the view they move to next.
function setFragment(fragment) {
  history.replaceState(null, "", fragment ? "#" + fragment : location.pathname + location.search);
  hashApplied = true;
}

// dropExpansion forgets an open expansion and what was drawn for it.
function dropExpansion(key) {
  expanded.delete(key);
  expCache.delete(key);
  expDetailCache.delete(key);
  expCellCache.delete(key);
}

// forgetGoneServiceExpansions closes the expansions of services the daemon no
// longer lists. A saved one outlives its service (renamed, removed from the
// configuration) with no row left to close it, and its detail request would
// fail on every refresh, leaving each one reported as partial.
function forgetGoneServiceExpansions() {
  const names = new Set(allServices.map((s) => s.name));
  const gone = [...expanded].filter((key) => isServiceExpansionKey(key) && !names.has(expansionName(key, expansionPrefixService)));
  if (!gone.length) return;
  gone.forEach(dropExpansion);
  saveUIState();
}

function toggleExpand(key) {
  if (expanded.has(key)) {
    dropExpansion(key);
    if (location.hash === "#" + key) setFragment("");
  } else {
    expanded.add(key);
    if (targetKindOfKey(key)) setFragment(key); // shareable deep-link
  }
  renderExpansionTarget(key);
  saveUIState();
}

function openServiceExpansion(name, scroll) {
  if (!name) return;
  const key = serviceExpansionKey(name);
  if (!expanded.has(key)) expanded.add(key);
  setFragment(key);
  openSectionForService(name);
  renderServices();
  if (scroll) {
    const el = document.getElementById("svc-row-" + name);
    if (el) el.scrollIntoView({ block: "center" });
  }
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

// applyHash opens the target named in a #svc:|#wat:|#app:|#lib: URL fragment
// or a section id such as #services-section. It runs after each render and on
// hashchange, but a fragment navigates once (hashApplied): it opens its panel
// and scrolls when it is first seen with its target present, and later renders
// only keep the expansion open — otherwise a fragment left in the address bar
// would drag the operator back from wherever they scrolled to.
let hashApplied = false;
function applyHash() {
  const h = decodeURIComponent(location.hash.slice(1));
  if (!h) return;
  const section = document.getElementById(h);
  if (section && section.classList.contains("panel")) {
    if (hashApplied || section.classList.contains("panel-hidden")) return;
    revealPanel(section);
    hashApplied = true;
    return;
  }
  const kind = targetKindOfKey(h);
  if (!kind) return;
  const name = h.slice(kind.prefix.length);
  const item = kind.items().find((candidate) => candidate && candidate.name === name);
  if (!item) return;
  const p = kind.panelOf(item);
  if (!hashApplied) revealPanel(p.dom.section, false);
  scheduleHashExpansion(h);
  if (!hashApplied) {
    const row = document.getElementById(p.rowId(item));
    if (row) row.scrollIntoView({ block: "center" });
    hashApplied = true;
  }
}
window.addEventListener(domEventHashChange, () => { hashApplied = false; applyHash(); });

// rowClick expands a row from a click anywhere on it, except on interactive
// elements (action buttons and links) which keep their own behaviour.
function rowClick(event, key, toggle = toggleExpand) {
  if (closestFrom(event, "button, a, input, select, summary")) return;
  toggle(key);
}

// Expansion detail cells are rendered only through litRender into the cell
// found here (by loadExpansionFor, renderWatchExpansionInto and
// reassertExpansions): the row template leaves the <td> empty (no binding), so
// the outer #rows/watch render and these loaders never fight over the same cell.
function expansionCell(key) {
  const tr = document.getElementById("exp-" + key);
  // The content lives in the cell's .exp-body wrapper, never in the <td>
  // itself: the wrapper is what keeps a wide expansion from widening the table.
  return tr ? tr.querySelector(":scope > td > .exp-body") : null;
}

// renderWatchExpansionInto renders one watch expansion cell from an
// already-fetched events response, shared by the first-open fetch and the
// per-poll refresh so expanded watches never each download their own copy.
// watchExpansionEvents keeps the events each open watch expansion was last
// drawn with, so an in-place re-render (a kill badge) needs no new download.
const watchExpansionEvents = new Map();

function renderWatchExpansionInto(key, events) {
  const name = expansionName(key, expansionPrefixWatch);
  watchExpansionEvents.set(key, events || []);
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

// rerenderWatchExpansion redraws one open watch expansion from the current
// watch list and the events it last showed.
function rerenderWatchExpansion(name) {
  const key = expansionKey(expansionPrefixWatch, name);
  if (!expanded.has(key)) return;
  renderWatchExpansionInto(key, watchExpansionEvents.get(key) || []);
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
// procMemCell shows a process's resident memory as a share of host RAM when the
// host total is known, else the plain figure; hostMem is read once per table.
function procMemCell(p, hostMem) {
  if (!p.rss) return tpl`<td>—</td>`;
  if (!(hostMem > 0)) return tpl`<td>${fmtBytes(p.rss)}</td>`;
  return tpl`<td>${usageBarMini(pctClamp((Number(p.rss) || 0) / hostMem * percentScale), fmtBytes(p.rss))}</td>`;
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
  const title = "in the service's control group but claimed by no process selector, and not part of the principal process's tree; a stop or restart terminates it unless the service keeps its children (stop_policy.keep_children)";
  return tpl`<span class="inactive" title="${title}">stray</span>`;
}

function procTreeLabel(row) {
  const depth = Number(row.depth || 0);
  const p = row.p || {};
  const branch = depth > 0
    ? tpl`<span class="proc-branch" role="img" title="child process of PID ${p.ppid || ""}" aria-label="child process of PID ${p.ppid || ""}"></span>`
    : nothing;
  return tpl`<span class="proc-tree${depth > 0 ? " proc-tree-child" : ""}" style="--proc-depth:${depth}">${branch}${procLabel(p)}</span>`;
}

function serviceUptimeCell(s) {
  const up = s ? fmtDuration(s.uptime_seconds) : "";
  if (!up) return mutedDash;
  const title = s.started_at ? `started ${fmtTime(s.started_at)}` : nothing;
  return tpl`<span title="${title}">${up}</span>`;
}

function cpuInline(cpu, ready, numCPU, cpuThread) {
  if (!ready) return numCPU ? tpl`<span class="muted">measuring…</span>` : mutedDash;
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
  if (serviceHasNoResidentProcess(s)) return mutedDash;
  return cpuInline(s && s.cpu, !!(s && s.cpu_ready), s && s.num_cpu, s && s.cpu_thread);
}

function memoryInline(rss) {
  rss = Number(rss) || 0;
  if (!rss) return mutedDash;
  const hostMem = hostMemTotalBytes();
  if (hostMem > 0) return usageBarMini(pctClamp(rss / hostMem * percentScale), fmtBytes(rss), `${fmtBytes(rss)} resident memory`);
  return tpl`<b>${fmtBytes(rss)}</b>`;
}

function serviceMemCell(s) {
  if (serviceHasNoResidentProcess(s)) return mutedDash;
  return memoryInline(s && s.rss);
}

function serviceFDsCell(s) {
  if (serviceHasNoResidentProcess(s)) return mutedDash;
  if (!(s && s.fds)) return mutedDash;
  return tpl`<span title="open file descriptors">${fmtNum(s.fds, 0)}</span>`;
}

function ioRWInline(read, write) {
  read = Number(read) || 0;
  write = Number(write) || 0;
  if (!read && !write) return mutedDash;
  return tpl`<span title="read / write">${fmtBytes(read)} / ${fmtBytes(write)}</span>`;
}

function serviceIoCell(s) {
  if (serviceHasNoResidentProcess(s)) return mutedDash;
  return ioRWInline(s && s.io_read, s && s.io_write);
}

function serviceStrayCount(s) {
  return Number(s && s.strays) || 0;
}

// Strays are diagnostic only: there is no action on the count itself.
const strayCountTitle = "processes in this service's control group that no configured selector claims; a stop or restart terminates them unless the service keeps its children (stop_policy.keep_children)";

// serviceStraysCell shows the count. A service with none reads as a dash like
// every other empty metric: the number is only interesting when it is not zero.
function serviceStraysCell(s) {
  const n = serviceStrayCount(s);
  if (!n) return mutedDash;
  return tpl`<span class="bad" title="${strayCountTitle}">${fmtNum(n, 0)}</span>`;
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
// renderWatchProcessesSection lists the processes an unowned_processes watch
// reports, each with the kill button its identity allows. The rows carry no
// command line (the watch publishes the resolved executable only), so procLabel
// shows the exe, or the previous exe of a binary replaced on disk.
function renderWatchProcessesSection(w) {
  const procs = w.processes || []; // already sorted by pid by the daemon
  if (!procs.length) {
    return w.check_type === watchTypeUnownedProcesses ? tpl`<div class="muted">No unowned processes.</div>` : nothing;
  }
  const hostMem = hostMemTotalBytes();
  return tpl`<table class="detail-compact-table watch-process-table">
    <caption class="visually-hidden">Unowned processes</caption>
    <thead><tr><th scope="col">PID</th><th scope="col">CMD</th><th scope="col">User</th><th scope="col">Reason</th><th scope="col" title="CPU used by this process, normalized to one core">CPU</th><th scope="col">Mem</th><th scope="col"><span class="visually-hidden">Action</span></th></tr></thead>
    <tbody>${procs.map((p) => tpl`<tr class="watch-process-row">
      <td>${p.pid}</td>
      <td>${procLabel(p)}</td>
      <td class="muted">${p.user || ""}</td>
      <td>${p.reason || ""}</td>
      ${procCpuCell(p)}
      ${procMemCell(p, hostMem)}
      <td>${watchProcessKillButton(w.name, p)}</td>
    </tr>`)}</tbody></table>`;
}

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
  return tpl`<h2 class="expansion-heading">Graphs <span class="muted">${winButtons(metricWins, win, "setWatchMetricWin", "Graph time window", w.name)}</span></h2>
    <div class="metric-grid">${panels}</div>`;
}

// watchMetricLabel titles a watch metric panel from its declared human label,
// falling back to the published field name. Units stay in the chart and summary.
function watchMetricLabel(metric) {
  return metric.label || String(metric.name || "").replace(/_/g, " ");
}

// loadWatchMetrics fills every panel of a watch's Graphs section through the one
// panel loader, exactly as a service detail fills its check metrics.
function loadWatchMetrics(w, generation = dashboardGeneration) {
  const list = (w && w.metrics) || [];
  if (!list.length) return Promise.resolve(true);
  const key = watchMetricsWindowKey(w.name);
  return Promise.all(list.map((metric) => metric.band
    ? loadSLAPanel(watchBandKey(w.name, metric.name), generation,
      { windowKey: key, warn: isAdvisorySeverity(metric.severity) })
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
  return tpl`<h2 class="expansion-heading">Availability <span class="muted">${winButtons(metricWins, win, "setSLAWin", "Availability time window", key)}</span></h2>
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
    if (cell == null) return `<span class="sla-bar-seg sla-gap" role="img" title="${esc(when + " · " + slaNoData)}" aria-label="${esc(when)}: ${slaNoData}"></span>`;
    const pctText = fmtPct(cell.pct);
    const affected = slaAffectedText(cell.down, cell.downBuckets);
    const tip = held
      ? `${when} · ${pctText} · held (no sample this sub-span)`
      : `${when} · ${pctText} · ${b.up}/${b.total} · ${affected}`;
    // A warning-severity state band caps its failing colour at amber
    // (sla-down-low is the warn token): a RAID array rebuilding is a thing to
    // watch, not an outage, and red is reserved for the bands that mean one.
    const band = opts.warn && cell.down > 0 ? "sla-down-low" : slaDownBand(cell.down);
    return `<span class="sla-bar-seg ${band}" role="img" title="${esc(tip)}" aria-label="${esc(when)}: ${esc(pctText)} available, ${esc(affected)}${held ? " (held)" : ""}"></span>`;
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
    value: metric.value,
    band: !!metric.band,
    severity: metric.severity || "",
    label: metric.label || "",
  })));
}

// serviceScopedWatchMetricEntries returns the graphable values produced by
// watches declared inside one service. Their canonical names are
// "service:watch" and their scope is explicit, so no catalog-specific names or
// second probe are needed to attach them to the service detail.
function serviceScopedWatchMetricEntries(service) {
  const prefix = `${service}:`;
  return (allWatches || [])
    .filter((w) => w && w.scope === watchScopeService && w.name.startsWith(prefix))
    .flatMap((watch) => (watch.metrics || [])
      .filter((metric) => !metric.band)
      .map((metric) => ({ watch, metric })));
}

function metricHasCurrentValue(metric) {
  return metric && metric.value !== null && metric.value !== undefined && Number.isFinite(Number(metric.value));
}

function serviceScopedWatchMetricLabel(entry) {
  const name = displayName(entry.watch);
  const lineMetrics = (entry.watch.metrics || []).filter((metric) => !metric.band);
  if (lineMetrics.length === 1 && entry.metric.name === "value") return name;
  return `${name} · ${watchMetricLabel(entry.metric)}`;
}

function serviceCurrentMetricCells(d) {
  const checks = serviceCheckMetrics(d)
    .filter(metricHasCurrentValue)
    .map((metric) => ({ label: serviceCheckMetricLabel(metric), metric }));
  const watches = serviceScopedWatchMetricEntries(d.name)
    .filter((entry) => metricHasCurrentValue(entry.metric))
    .map((entry) => ({ label: serviceScopedWatchMetricLabel(entry), metric: entry.metric }));
  return [...checks, ...watches].map(({ label, metric }) => tpl`<div>
    <span class="muted">${label}</span><br><b>${fmtMetricValue(metric.value, metric.unit)}</b>
  </div>`);
}

function serviceCheckMetricDomID(service, check, metric, suffix) {
  return detailDomId(service, `metric-${detailDomKey(check + ":" + metric)}-${suffix}`);
}

function serviceLatencyDomID(service, check, suffix) {
  return detailDomId(service, `lat-${detailDomKey(check)}-${suffix}`);
}

function serviceCheckMetricLabel(metric) {
  const check = String(metric.check || "").replace(/[-_]/g, " ");
  if (metric.name === "value") return check;
  return `${check} · ${metric.label || watchMetricLabel(metric)}`;
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
  if (c.ok) return tpl`<span class="ok">ok</span>${age}`;
  // An advisory — declared optional, or a failure graded below an outage —
  // reads amber: it is worth seeing, not an outage. It names its grade.
  if (c.optional || isAdvisorySeverity(c.severity)) {
    // An advisory below warning (debug, info) names its level.
    const label = c.optional || c.severity === targetStateWarning ? "warn" : c.severity;
    return tpl`<span class="inactive">${label}</span>${age}`;
  }
  if (c.severity === severityCritical) return tpl`<span class="sev-critical-text">crit</span>${age}`;
  return tpl`<span class="bad">fail</span>${age}`;
}

// checkSLAHTML renders the SLA cell, or an explicit n/a for a check that will
// never have one. The strip itself arrives from loadCheckSLA, on the window the
// detail's selector is on.
function checkSLAHTML(service, c) {
  if (verdictlessCheck(c)) return tpl`<span class="muted" title="This check reports no availability">n/a</span>`;
  return tpl`<div id="${checkSLADomId(service, c.name)}" class="muted sla-check-strip">loading…</div>`;
}

// pendingSessionActions is the session analogue of liveOps: a close or kill
// this browser started. From the confirmation until the daemon's inventory no
// longer lists the session, its action cell says what is happening instead of
// offering the button again. A failed request retains its reason beside the button.
const pendingSessionActions = new Map();
// Confirmed row actions share the daemon's service/watch operation scope.
// Separate targets remain concurrent; the server still revalidates every action.
const pendingRowTargets = new Map();
// sessionActionHoldMs caps a pending entry whose session the inventory never
// drops (a refresh that keeps failing), so the button cannot stay hidden.
const sessionActionHoldMs = 2 * 60 * millisecondsPerSecond;
const sessionActionQueued = "queued";
const sessionActionClosing = "closing";
const sessionActionKilling = "killing";
// sessionActionKeySeparator joins a session action key's parts; NUL never
// occurs in a service, check or identity.
const sessionActionKeySeparator = "\u0000";
const sessionActionKeyTerminal = "terminal";
const sessionActionKeyEmpty = "empty";

function sessionActionKey(...parts) {
  return parts.map((part) => String(part ?? "")).join(sessionActionKeySeparator);
}
const sshSessionActionKey = (service, pid, startTicks) => sessionActionKey(sessionKindSSH, service, pid, startTicks);
const terminalSessionActionKey = (service, check, identity) => sessionActionKey(sessionActionKeyTerminal, service, check, identity);
const emptySourceActionKey = (service, check) => sessionActionKey(sessionActionKeyEmpty, service, check);
const dbQueryActionKey = (service, watch, identity) => sessionActionKey(sessionKindDatabase, service, watch, identity);
const sessionActionKeyWatchProcess = "watch-process";
const watchProcessActionKey = (watch, pid, startTicks) => sessionActionKey(sessionActionKeyWatchProcess, watch, pid, startTicks);

function pendingSessionAction(key) {
  const pending = pendingSessionActions.get(key);
  if (!pending) return null;
  if (pending.done && !pending.error && Date.now() - pending.started > sessionActionHoldMs) {
    pendingSessionActions.delete(key);
    return null;
  }
  return pending;
}

function sessionActionBadge(verb, title) {
  return tpl`<span class="session-busy inactive" role="status" aria-live="polite" title="${title || ""}">${verb}…</span>`;
}

// pendingSessionBadge is the action cell of a session with an action in
// flight, or null when the regular button applies.
function pendingSessionBadge(key) {
  const pending = pendingSessionAction(key);
  return pending && !pending.error ? sessionActionBadge(pending.verb, pending.label) : null;
}

function rowActionFeedback(key, button) {
  const error = pendingSessionAction(key)?.error;
  return error ? tpl`<span class="row-action-error bad" role="alert">${error}</span>${button}` : button;
}

// presentSessionActionKeys lists every session the inventory still shows, so a
// finished action is forgotten once its session is gone.
function presentSessionActionKeys(inventory) {
  const keys = new Set();
  for (const session of inventory.ssh || []) keys.add(sshSessionActionKey(session.service, session.pid, session.start_ticks));
  for (const source of inventory.sources || []) {
    for (const issue of source.issues || []) keys.add(sshSessionActionKey(source.service, issue.pid, issue.start_ticks));
    if (source.can_close_empty) keys.add(emptySourceActionKey(source.service, source.check));
  }
  for (const session of inventory.terminal || []) keys.add(terminalSessionActionKey(session.service, session.check, session.identity));
  for (const query of inventory.database || []) keys.add(dbQueryActionKey(query.service, query.watch, query.identity));
  // A watch's listed processes share the pending map: a kill is forgotten
  // once the watch no longer lists the process it signalled.
  for (const w of allWatches || []) {
    for (const p of w.processes || []) keys.add(watchProcessActionKey(w.name, p.pid, p.start_ticks));
  }
  return keys;
}

function forgetFinishedSessionActions(inventory) {
  if (!pendingSessionActions.size) return;
  const present = presentSessionActionKeys(inventory);
  for (const [key, pending] of pendingSessionActions) {
    if (pending.done && !present.has(key)) pendingSessionActions.delete(key);
  }
}

function sshSessionCloseButton(service, session) {
  const pending = pendingSessionBadge(sshSessionActionKey(service, session.pid, session.start_ticks));
  if (pending) return pending;
  if (!me.can_act) return readOnlyNote;
  const label = `Close SSH session ${session.terminal || ""} of ${session.user || "unknown user"}`;
  if (!session.can_close) return tpl`<button class="icon-btn danger-btn" disabled aria-label="${label}" title="${session.message || "Session identity cannot be verified safely"}">${closeGlyph}</button>`;
  return rowActionFeedback(sshSessionActionKey(service, session.pid, session.start_ticks), tpl`<button class="icon-btn danger-btn" data-ssh-session-close="1" data-ssh-service="${service}" data-ssh-session-pid="${session.pid}" data-ssh-session-start-ticks="${session.start_ticks}" data-ssh-session-terminal="${session.terminal}" data-ssh-session-user="${session.user || ""}" data-ssh-session-managed="${!!session.managed_by_logind}" aria-label="${label}" title="${label}">${closeGlyph}</button>`);
}

// closeGlyph is the one mark every session close button shows; the button's
// aria-label and title carry what it closes.
const closeGlyph = tpl`<span aria-hidden="true">✕</span>`;

function terminalSessionCloseButton(session) {
  const label = `Close ${session.multiplexer || "terminal"} session ${session.name || ""} of ${session.user || "unknown user"}`;
  return rowKillButton({
    pendingKey: terminalSessionActionKey(session.service, session.check, session.identity),
    canKill: session.can_close, unavailableTitle: nothing,
    button: () => tpl`<button class="icon-btn danger-btn" data-terminal-session-close="1"
    data-terminal-service="${session.service || ""}" data-terminal-check="${session.check || ""}"
    data-terminal-multiplexer="${session.multiplexer || ""}" data-terminal-session="${session.name || ""}"
    data-terminal-user="${session.user || ""}" data-terminal-identity="${session.identity || ""}" aria-label="${label}" title="${label}">${closeGlyph}</button>`,
  });
}

function sessionStateCell(state) {
  const [badgeState, label] = sessionStateBadges[state] || [targetStateWarning, state || sessionStateUnknown];
  return stateBadgeLabel(badgeState, label);
}

function emptySessionCloseButton(source) {
  if (source.state !== sessionSourceAvailable || !source.can_close_empty) return nothing;
  const label = `Close the empty ${source.kind || "terminal"} server of ${source.user || "unknown user"}`;
  return rowKillButton({
    pendingKey: emptySourceActionKey(source.service, source.check),
    canKill: true,
    button: () => tpl`<button class="icon-btn danger-btn" data-empty-session-close="1"
    data-empty-session-service="${source.service || ""}"
    data-empty-session-check="${source.check || ""}" aria-label="${label}" title="${label}">${closeGlyph}</button>`,
  });
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
  const sources = (inventory.sources || []).filter((source) => source.kind !== sessionKindDatabase);
  const ssh = (inventory.ssh || []).map((session) => ({
    kind: sessionKindSSH, service: session.service || "", user: session.user || "",
    name: session.terminal || "", pid: session.pid || 0, pidSort: session.pid || 0, state: session.residual ? sessionStateResidual : sessionStateActive,
    detail: session.residual ? "sudo terminal retained after SSH disconnected" : nothing,
    ...sessionUsageRow(session, true),
    action: sshSessionCloseButton(session.service || "", session),
  }));
  const terminal = (inventory.terminal || []).map((session) => ({
    kind: session.multiplexer || "", service: session.service || "", user: session.user || "",
    name: session.name || "", pid: (session.pids || []).join(", "), pidSort: (session.pids || [0])[0] || 0,
    state: session.state || sessionStateUnknown,
    // Only tmux reports a window count; a screen session says nothing under
    // its name rather than a placeholder dash.
    detail: session.state === sessionStateResidual ? "Executable removed; session server is still running"
      : Number.isInteger(session.windows) ? `${fmtNum(session.windows, 0)} window${session.windows === 1 ? "" : "s"}` : "",
    ...sessionUsageRow(session, session.has_idle),
    action: terminalSessionCloseButton(session),
  }));
  const issues = sources.flatMap((source) => (source.issues || []).map((issue) => ({
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
  const emptySources = sources
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

// rowKillButton is the action cell of one row the daemon may signal or close
// (a statement, a process, a terminal session, an empty terminal server): the
// pending badge while its action is in flight, "killing…" when the server is
// already stopping it, "read-only" for a viewer, "unavailable" (titled with the
// daemon's reason, when it gives one) when it cannot be acted on, else the
// caller's danger button. The button is passed in because lit binds only static attribute
// names and each row kind carries its own data-* identity.
function rowKillButton({ pendingKey, stopping, stoppingHint, canKill, unavailableTitle, button }) {
  const pending = pendingSessionBadge(pendingKey);
  if (pending) return pending;
  if (stopping) return sessionActionBadge(sessionActionKilling, stoppingHint);
  if (!me.can_act) return readOnlyNote;
  if (!canKill) return tpl`<span class="muted" title="${unavailableTitle}">unavailable</span>`;
  return rowActionFeedback(pendingKey, button());
}

// dbQueryKillButton cancels one running statement; the confirmation offers
// closing its whole connection instead. A host watch's statement has no
// service to act through, so the daemon reports it can_kill false.
function dbQueryKillButton(query) {
  const label = `Kill ${query.engine || "database"} statement ${query.id} of ${query.user || "unknown user"}`;
  return rowKillButton({
    pendingKey: dbQueryActionKey(query.service, query.watch, query.identity),
    // Cancelled from here, another tab, sermoctl or the server itself: the
    // daemon reports it is being stopped, which is not "cannot be killed".
    stopping: query.stopping, stoppingHint: "The server is stopping this statement",
    canKill: query.can_kill, unavailableTitle: "This statement cannot be cancelled from the dashboard",
    button: () => tpl`<button class="icon-btn danger-btn" data-db-query-kill="1"
      data-db-query-service="${query.service || ""}" data-db-query-watch="${query.watch || ""}"
      data-db-query-id="${query.id}" data-db-query-identity="${query.identity || ""}" aria-label="${label}" title="${label}">${closeGlyph}</button>`,
  });
}

// watchProcessKillButton signals one process an unowned_processes watch lists.
// The daemon decides can_kill per row from the watch's kill selector and the
// identity it could verify; kill_reason says why a row is unavailable.
function watchProcessKillButton(watch, p) {
  const label = `Kill process ${p.pid} of ${p.user || "unknown user"}`;
  return rowKillButton({
    pendingKey: watchProcessActionKey(watch, p.pid, p.start_ticks),
    canKill: p.can_kill, unavailableTitle: p.kill_reason || "This process cannot be signalled from the dashboard",
    button: () => tpl`<button class="icon-btn danger-btn" data-watch-process-kill="1"
      data-watch-process-watch="${watch}" data-watch-process-pid="${p.pid}" data-watch-process-start-ticks="${p.start_ticks || ""}"
      aria-label="${label}" title="${label}">${closeGlyph}</button>`,
  });
}

function dbQueryText(query) {
  return `${query.query || ""}${query.truncated ? "…" : ""}`;
}

function dbQueryExpansionKey(query) {
  return JSON.stringify([query.service || "", query.watch || "", query.engine || "", query.id, query.display_identity || query.identity || ""]);
}

function toggleDBQueryExpansion(key) {
  if (!(latestSessionInventory.database || []).some((query) => dbQueryExpansionKey(query) === key)) return;
  if (expandedDBQueries.has(key)) expandedDBQueries.delete(key);
  else expandedDBQueries.add(key);
  renderSessions();
}

function dbQueryDetail(row) {
  const query = row.query;
  const postgres = query.engine === "postgres";
  const fields = [
    ["Engine", query.engine, colDupPhone], ["Service", query.service || "Host watch"], ["Check", query.watch],
    ["Database", query.database, colDupPhone], ["Client", query.host],
    [postgres ? "Backend PID" : "Connection ID", query.id, colDupPhone],
    ...(!postgres ? [["OS TID", query.os_tid]] : []),
    ...(query.query_id ? [["Query ID", query.query_id]] : []),
    ["Command", query.command, colDupWide], ["State", query.state, colDupWide],
    ["Sample", query.at ? `${fmtTime(query.at)} · ${fmtAge(query.at)}` : ""],
    ["CPU thread", dbQueryCPUThreadCell(row), colDupWide], ["CPU total", sessionCPUCell(row), colDupWide],
    ["Memory", sessionMemoryCell(row), colDupWide], ["IO R/W", sessionIOCell(row), colDupWide],
  ];
  return tpl`<div class="exp-body db-query-detail">
    <div class="runtime-grid">${fields.map(([label, value, cls]) => tpl`<div class="${cls || nothing}"><span class="muted">${label}</span><br>${value || "—"}</div>`)}</div>
    <h4>SQL</h4>
    <pre class="db-query-text"><code>${query.query ? dbQueryText(query) : "No statement text available."}</code></pre>
    ${query.truncated ? tpl`<p class="muted">Statement truncated by the daemon.</p>` : nothing}
  </div>`;
}

function dbQueryRows(inventory) {
  const queries = (inventory.database || []).map((query) => {
    const state = [query.command, query.state].filter(Boolean).join(" · ");
    return {
      kind: query.engine || sessionKindDatabase, service: query.service || "", user: query.user || "",
      database: query.database || "", client: query.host || "", id: query.id || 0,
      state, running: query.elapsed_seconds || 0, long: !!query.long, matched: !!query.matched, alerted: !!query.alerted,
      search: [query.watch, query.query, query.os_tid, query.query_id].join(" "),
      key: dbQueryExpansionKey(query), query, stateCell: state || "—",
      cpu: query.cpu || 0, cpuThread: query.cpu_thread || 0, cpuReady: !!query.cpu_ready,
      memory: query.rss || 0, memoryReady: !!query.memory_ready,
      ioRead: query.io_read || 0, ioWrite: query.io_write || 0, ioReady: !!query.io_ready,
      action: dbQueryKillButton(query),
    };
  });
  // An available source with no statements is the normal quiet case; only a
  // source that is waiting or failing has something to say.
  const sources = (inventory.sources || [])
    .filter((source) => source.kind === sessionKindDatabase && source.state !== sessionSourceAvailable)
    .map((source) => {
      const state = source.state || targetStateCollecting;
      return {
        kind: sessionKindDatabase, service: source.service || "", user: "", database: "", client: "", id: 0,
        state, running: -1, long: false, search: [source.check, source.message].join(" "),
        message: `${[source.service || "host", source.check].filter(Boolean).join(":")}: ${source.message || (state === targetStateCollecting ? "Waiting for a sample" : "Statement list unavailable")}`,
        stateCell: sessionStateCell(state), action: nothing,
      };
    });
  return [...queries, ...sources];
}

const dbQuerySortKeys = {
  type: (row) => row.kind,
  user: (row) => row.user,
  database: (row) => row.database,
  client: (row) => row.client,
  id: (row) => row.id,
  state: (row) => row.state,
  running: (row) => row.running,
  cpu_thread: (row) => row.cpuReady ? row.cpuThread : -1,
  cpu: (row) => row.cpuReady ? row.cpu : -1,
  memory: (row) => row.memoryReady ? row.memory : -1,
  io: (row) => row.ioReady ? row.ioRead + row.ioWrite : -1,
};

function sortDBQueryRows(rows) {
  const value = dbQuerySortKeys[dbQuerySort.key];
  if (!value) return rows;
  rows.sort((a, b) => compareSortValues(value(a), value(b)) * dbQuerySort.dir
    || compareSortValues(a.id, b.id));
  return rows;
}

function dbQueryRunningCell(row) {
  if (row.running < 0) return mutedDash;
  if (!row.long) return fmtDuration(row.running);
  return tpl`<span class="lvl-warning" title="Running longer than the watch's min_duration">${fmtDuration(row.running)}</span>`;
}

function dbQueryCPUThreadCell(row) {
  return row.cpuReady ? usageBarMini(pctClamp(row.cpuThread), fmtPct(row.cpuThread), `${fmtPct(row.cpuThread)} of one logical CPU`) : sessionMetricPlaceholder(row);
}

function dbQueryRow(row) {
  if (!row.query) return tpl`<tr class="db-query-source"><td colspan="11">${row.stateCell} <span class="muted">${row.message}</span></td></tr>`;
  const open = expandedDBQueries.has(row.key);
  const detailID = `db-query-${detailDomKey(row.key)}`;
  const label = `${row.kind} statement ${row.id} of ${row.user || "unknown user"}`;
  return tpl`<tr class="clickable db-query-row${row.matched || row.alerted ? " row-warning" : ""}" data-db-query-key="${row.key}">
    <td class="db-col-type">${row.kind || "—"}</td>
    <td><button type="button" class="row-toggle" data-db-query-expand="${row.key}" aria-expanded="${open}" aria-controls="${open ? detailID : nothing}" aria-label="${expandToggleAriaLabel(label, open, "SQL details")}">
      <span class="exp" aria-hidden="true">${open ? "▾" : "▸"}</span><span class="db-cell-text" title="${row.user}">${row.user || "—"}</span>
    </button></td>
    <td class="db-col-database"><span class="db-cell-text" title="${row.database}">${row.database || "—"}</span></td>
    <td class="db-col-id mono">${row.id || "—"}</td>
    <td class="db-col-state"><span class="db-cell-text" title="${row.state}">${row.stateCell}</span></td>
    <td>${dbQueryRunningCell(row)}</td><td class="db-col-cpu-thread">${dbQueryCPUThreadCell(row)}</td><td class="db-col-cpu">${sessionCPUCell(row)}</td>
    <td class="db-col-memory">${sessionMemoryCell(row)}</td><td class="db-col-io">${sessionIOCell(row)}</td><td>${row.action}</td>
  </tr>${open ? tpl`<tr class="exp-row db-query-expansion" id="${detailID}"><td colspan="11">${dbQueryDetail(row)}</td></tr>` : nothing}`;
}

function sessionCountLabel(count, notices) {
  return `${count}${notices ? ` · ${countLabel(notices, "notice")}` : ""}`;
}

function renderDBQueries(rows, totalRows) {
  const block = $("#db-query-block");
  const body = $("#db-query-rows");
  const hasSource = (latestSessionInventory.sources || []).some((source) => source.kind === sessionKindDatabase)
    || (latestSessionInventory.database || []).length > 0;
  const show = hasSource && (sessionPanel.status === filterAll || sessionPanel.status === sessionKindDatabase);
  if (block) block.hidden = !show;
  const count = $("#db-queries-count");
  const statements = (latestSessionInventory.database || []).length;
  if (count) count.textContent = sessionCountLabel(statements, totalRows - statements);
  if (body) {
    sortDBQueryRows(rows);
    const content = rows.length ? rows.map(dbQueryRow) : tpl`<tr><td colspan="11" class="muted">No statements match the filter.</td></tr>`;
    litRender(content, body);
  }
  updateSortHeaders("db-queries");
}

// sessionFieldsMatch is the Sessions search: every listed field, any case.
function sessionFieldsMatch(fields) {
  return !sessionPanel.query || fields.join(" ").toLowerCase().includes(sessionPanel.query);
}

function dbQueryRowMatches(row) {
  return sessionFieldsMatch([row.kind, row.service, row.user, row.database, row.client, row.id, row.state, row.search]);
}

function sessionFilterCounts(rows, dbRows) {
  const counts = stateCounts(rows, (row) => row.kind, sessionTypeFilterStates);
  counts[sessionKindDatabase] = dbRows.length;
  counts[filterAll] += dbRows.length;
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

function sessionSortMissing(row, key) {
  const readyKey = sessionSortReadyKeys[key];
  return !!readyKey && !row[readyKey];
}

function sortSessionRows(rows) {
  const sort = sessionPanel.sort;
  const value = sessionSortKeys[sort.key];
  if (!sort.key || !value) return rows;
  rows.sort((a, b) => {
    const aMissing = sessionSortMissing(a, sort.key);
    const bMissing = sessionSortMissing(b, sort.key);
    if (aMissing !== bMissing) return aMissing ? 1 : -1;
    const primary = compareSortValues(value(a), value(b)) * sort.dir;
    if (primary !== 0) return primary;
    return compareSortValues(sessionSortKeys.session(a), sessionSortKeys.session(b));
  });
  return rows;
}

function sessionIdleCell(row) {
  return row.idleReady ? fmtDuration(row.idle) : mutedDash;
}

function sessionMetricPlaceholder(row) {
  return tpl`<span class="muted">${row.metricsExpected ? "measuring…" : "—"}</span>`;
}

function sessionCPUCell(row) {
  return row.cpuReady ? usageBarMini(pctClamp(row.cpu), fmtPct(row.cpu), `${fmtPct(row.cpu)} of host CPUs`) : sessionMetricPlaceholder(row);
}

function sessionMemoryCell(row) {
  return row.memoryReady ? memoryInline(row.memory) : mutedDash;
}

function sessionIOCell(row) {
  if (!row.ioReady) return sessionMetricPlaceholder(row);
  return tpl`<span title="read / write">${fmtBytesPerSecond(row.ioRead)} / ${fmtBytesPerSecond(row.ioWrite)}</span>`;
}

function renderSessions(inventory = latestSessionInventory) {
  // Filtering/sorting keeps expansions. A fresh sample must still identify the
  // exact statement, not just reuse its connection ID or SQL text.
  const fresh = inventory !== latestSessionInventory;
  latestSessionInventory = inventory || {};
  const present = new Set((latestSessionInventory.database || [])
    .filter((query) => !fresh || query.identity).map(dbQueryExpansionKey));
  for (const key of expandedDBQueries) if (!present.has(key)) expandedDBQueries.delete(key);
  forgetFinishedSessionActions(latestSessionInventory);
  const sources = latestSessionInventory.sources || [];
  const database = latestSessionInventory.database || [];
  const filter = sessionPanel.status;
  setPanelVisible(sessionPanel.dom.section, sources.length > 0 || database.length > 0);
  const rows = sessionRows(latestSessionInventory);
  const dbRows = dbQueryRows(latestSessionInventory);
  const filtered = rows.filter((row) => {
    if (filter !== filterAll && row.kind !== filter) return false;
    return sessionFieldsMatch([row.kind, row.service, row.user, row.name, row.pid, row.state]);
  });
  const dbFiltered = dbRows.filter(dbQueryRowMatches);
  const dbShown = filter === filterAll || filter === sessionKindDatabase ? dbFiltered : [];
  renderPanelToolbar(sessionPanel, {
    counts: sessionFilterCounts(rows, dbRows),
    groups: new Map(), visibleGroups: [],
    shown: filtered.length + dbShown.length, total: rows.length + dbRows.length,
  });
  sortSessionRows(filtered);
  // A host whose only session sources are db_queries watches, or the database
  // filter, has nothing for the terminal table to say.
  const table = $("#sessions-section .sessions-table");
  if (table) table.hidden = filter === sessionKindDatabase || !sources.some((source) => source.kind !== sessionKindDatabase);
  renderDBQueries(dbShown, dbRows.length);
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
  const count = sessionCount();
  if (sessionPanel.dom.count) sessionPanel.dom.count.textContent = sessionCountLabel(count, rows.length + dbRows.length - count);
  updateSortHeaders("sessions");
  refreshChrome();
}

// Connection context comes from structured readings, never from parsing messages.
const serviceConnectionFields = new Set(["host", "port", "socket", "protocol", "version", "connected_clients"]);

function checkConnectionTarget(c) {
  const values = new Map((c.readings || []).filter((r) => !r.error && !r.warning).map((r) => [r.field, r.value]));
  const socket = values.get("socket");
  if (socket) return { key: JSON.stringify(["socket", socket]), label: socket };
  const host = values.get("host"), port = values.get("port");
  if (!host || !port) return null;
  const address = host.includes(":") && !host.startsWith("[") ? `[${host}]` : host;
  return { key: JSON.stringify(["host", host, port]), label: `${address}:${port}` };
}

function serviceConnectionGroups(d) {
  const groups = new Map();
  for (const c of d.checks || []) {
    if (c.stale || c.skipped || !c.at) continue;
    const target = checkConnectionTarget(c);
    if (!target) continue;
    if (!groups.has(target.key)) groups.set(target.key, { ...target, checks: [] });
    groups.get(target.key).checks.push(c);
  }
  return [...groups.values()];
}

function connectionValues(group, field) {
  return [...new Set(group.checks.flatMap((c) => (c.readings || [])
    .filter((r) => r.field === field && r.value && !r.error && !r.warning).map((r) => r.value)))];
}

// Choose one real sample per protocol. Equal timestamps use the check name, so
// reordering the API cannot change the displayed reading. Never sum/average probes.
function connectionSamples(group) {
  const samples = new Map();
  const ordered = [...group.checks].sort((a, b) =>
    Date.parse(b.at) - Date.parse(a.at) || a.name.localeCompare(b.name));
  for (const c of ordered) {
    const protocol = (c.readings || []).find((r) => r.field === "protocol")?.value || c.type;
    const reading = (c.readings || []).find((r) => r.field === "connected_clients" && r.value && !r.error && !r.warning);
    if (reading && !samples.has(protocol)) samples.set(protocol, { check: c, reading, protocol });
  }
  return [...samples.values()];
}

function serviceConnectionDetail(groups) {
  if (!groups.length) return nothing;
  return tpl`<section class="service-connections" aria-label="Connection details">${groups.map((group) => {
    const protocols = connectionValues(group, "protocol");
    const versions = connectionValues(group, "version");
    const samples = connectionSamples(group);
    return tpl`<div class="connection-grid">
      <div><span class="muted">Target</span><br><b>${group.label}</b></div>
      ${protocols.length ? tpl`<div><span class="muted">Protocol</span><br><b>${protocols.join(" · ")}</b></div>` : nothing}
      ${versions.length ? tpl`<div><span class="muted">Reported version</span><br><b>${versions.join(" · ")}</b></div>` : nothing}
      ${samples.map(({ check, reading, protocol }) => tpl`<div>
        <span class="muted">Connections${samples.length > 1 ? ` · ${protocol}` : ""}</span><br>
        <b>${reading.value.endsWith(" connections") ? reading.value.slice(0, -" connections".length) : reading.value}</b>
        <div class="muted connection-source">${check.name} · ${fmtAge(check.at)}</div>
      </div>`)}
    </div>`;
  })}</section>`;
}

function checkReadingValue(r) {
  if (r.error) return tpl`<span class="watch-reading-value bad">${r.error}</span>`;
  if (r.warning) return tpl`<span class="watch-reading-value inactive">${r.warning}</span>`;
  return tpl`<b class="watch-reading-value${r.good ? " good" : ""}">${r.value || "—"}</b>`;
}

function renderCheckReadings(readings) {
  if (!readings.length) return nothing;
  return tpl`<dl class="check-readings">${readings.map((r) => tpl`<div>
    <dt>${r.label || r.field || "Sample"}</dt><dd>${checkReadingValue(r)}</dd>
  </div>`)}</dl>`;
}

function serviceCheckResult(c, shared) {
  // A hint belongs only to an ordinary healthy result. Fallback messages,
  // operator summaries and all diagnostics remain visible in full.
  const compact = c.summary && c.ok && !c.skipped && !c.stale && !verdictlessCheck(c);
  const readings = c.readings || [];
  const visible = readings.filter((r) => {
    if (r.field === "latency_ms") return false;
    if (r.error || r.warning) return true;
    if (shared && serviceConnectionFields.has(r.field)) return false;
    if (!compact) return true;
    if (c.type === "cert") return false;
    return !(["http", "https"].includes(c.type) && ["status", "protocol"].includes(r.field));
  });
  const message = compact ? c.summary : c.message;
  const messageView = message ? tpl`<span class="truncate check-message" title="${message}">${message}</span>` : nothing;
  return tpl`${renderCheckReadings(visible)}${messageView}
    ${!visible.length && !message ? "—" : nothing}`;
}

function serviceCheckRows(d, groups) {
  const sharedChecks = new Set(groups.flatMap((group) => group.checks));
  const checkRows = (d.checks || []).map((c) => {
    const age = c.at ? tpl` <span class="muted">· ${fmtAge(c.at)}</span>` : nothing;
    const state = checkStateHTML(c, age);
    return tpl`<tr><td>${c.name}</td><td class="muted">${c.type || ""}</td>
      <td>${state}${c.optional ? tpl` <span class="muted">(optional)</span>` : nothing}</td>
      <td class="sla-cell">${checkSLAHTML(d.name, c)}</td>
      <td class="muted">${serviceCheckResult(c, sharedChecks.has(c))}</td></tr>`;
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
          ${procMemCell(p, hostMem)}
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

function serviceScopedWatchMetricDomID(service, watch, metric, suffix) {
  return detailDomId(service, `watch-metric-${detailDomKey(watch + ":" + metric)}-${suffix}`);
}

// daemonMetricDomID names one daemon process-metric panel's summary or chart.
function daemonMetricDomID(key, suffix) {
  return `daemon-${key}-${suffix}`;
}

function serviceGraphDetail(d) {
  const noResidentProcess = !!d.no_resident_process;
  const measured = serviceMeasuredChecks(d);
  const checkMetrics = serviceCheckMetrics(d);
  const scopedWatchMetrics = serviceScopedWatchMetricEntries(d.name);
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
  const scopedWatchMetricPanels = scopedWatchMetrics.map((entry) => tpl`<div class="metric-panel"
      data-service-watch-metric="${entry.watch.name}" data-service-metric-name="${entry.metric.name}">
    ${metricPanelBody(serviceScopedWatchMetricDomID(d.name, entry.watch.name, entry.metric.name, "summary"),
      serviceScopedWatchMetricDomID(d.name, entry.watch.name, entry.metric.name, "chart"),
      serviceScopedWatchMetricLabel(entry))}
  </div>`);
  const win = serviceMetricState(d.name).window;
  return tpl`<h2>Graphs <span class="muted">${winButtons(metricWins, win, "setMetricWin", "Graph time window", d.name)}</span></h2>
    <div class="metric-grid">
      ${slaChartPanel(d.name)}
      ${latencyPanels}
      ${checkMetricPanels}
      ${scopedWatchMetricPanels}
      ${runtimeGraphPanels}
    </div>`;
}

// Keep the audit result readable without presenting it as current health.
function lastOperationCell(d) {
  const op = d && d.last_operation;
  if (!op || !op.time) return mutedDash;
  return tpl`<details class="operation-history"><summary>${eventLabel(op)} · ${fmtTimeTitled(op.time)}</summary>
    <div class="muted">Recorded result: ${op.status || backendStatusUnknown}</div>
    ${op.message ? tpl`<div>${op.message}</div>` : nothing}</details>`;
}

function operationForecastCell(forecasts) {
  return operationForecastSentences(forecasts).map((sentence, i) =>
    tpl`${i ? tpl`<br>` : nothing}<span class="${operationOutcomeStyles[sentence.outcome].cls}">${sentence.text}</span>`);
}

function serviceGeneralDetail(d, processGeneral) {
  const forecasts = markingForecasts(d);
  // Name leads the grid: the row's own heading was dropped as a repeat of the
  // component, and an expansion scrolled away from its row otherwise has nothing
  // identifying it. Name and State stay put at every width for that reason; every
  // other field that also exists as a column is deduplicated — see colDupWide.
  return tpl`<div class="runtime-grid">
      <div><span class="muted">Name</span><br><b>${displayName(d)}</b></div>
      <div><span class="muted">State</span><br>${serviceStateCell(d)}</div>
      <div><span class="muted">Category</span><br>${categoryBadge(categoryOf(d, defaultCategoryService))}</div>
      <div><span class="muted">Unit</span><br>${unitCell(d)}</div>
      <div><span class="muted">Backend</span><br>${d.backend || "—"}</div>
      <div class="${colDupWide}"><span class="muted">Uptime</span><br>${serviceUptimeCell(d)}</div>
      <div><span class="muted">Interval</span><br>${d.interval ? d.interval : mutedDash}</div>
      <div><span class="muted">Dry run</span><br><b>${d.dry_run ? "yes" : "no"}</b></div>
      <div><span class="muted">Policy</span><br>${policyCell(d)}</div>
      <div><span class="muted">Locks</span><br>${locksCell(d)}</div>
      <div class="${colDupPhone}"><span class="muted">Last event</span><br>${lastEventCell(d)}</div>
      <div><span class="muted">Last operation (history)</span><br>${lastOperationCell(d)}</div>
      ${forecasts.length ? tpl`<div><span class="muted">Operation forecast</span><br>${operationForecastCell(forecasts)}</div>` : nothing}
      <div class="${colDupPhone}"><span class="muted">Strays</span><br>${serviceStraysCell(d)}</div>
      <div><span class="muted">Next remediation</span><br>${nextRemediationCell(d)}</div>
      <div><span class="muted">Remediation</span><br>${renderRemediation(d.remediation)}</div>
      ${serviceCurrentMetricCells(d)}
      ${processGeneral}
    </div>`;
}

function renderServiceDetail(d) {
  const connections = serviceConnectionGroups(d);
  const checks = serviceCheckRows(d, connections);
  const locks = serviceLockDetail(d);
  const processes = serviceProcessDetail(d);
  const diagnosticsOpen = !window.matchMedia("(max-width: 640px)").matches;
  const disabledNote = !d.enabled
    ? tpl`<p class="muted bad">This service is disabled in configuration (enabled: false). Edit its YAML file and reload the daemon to activate it.</p>`
    : nothing;
  return tpl`<div class="service-detail" data-service-detail="${d.name}">
    ${disabledNote}
    ${serviceGeneralDetail(d, processes.general)}
    ${serviceConnectionDetail(connections)}
    ${serviceGraphDetail(d)}
    ${processes.section}
    <details class="detail-section" data-detail-section="checks" ?open=${diagnosticsOpen}>
    <summary><h2>Checks</h2></summary>
    <table class="detail-checks-table">
      <caption class="visually-hidden">Service checks</caption>
      <thead><tr><th scope="col">Check</th><th scope="col">Type</th><th scope="col">State</th><th scope="col">SLA</th><th scope="col">Message</th></tr></thead>
      <tbody>${checks}</tbody></table>
    </details>
    <h2>Named locks</h2>
    <table class="detail-locks-table">
      <caption class="visually-hidden">Service named locks</caption>
      <thead><tr><th scope="col">Name</th><th scope="col">State</th><th scope="col">TTL</th><th scope="col">Owner</th><th scope="col">Created</th><th scope="col">Blocks</th><th scope="col">Reason</th><th scope="col"><span class="visually-hidden">Actions</span></th></tr></thead>
      <tbody>${locks.rows}</tbody></table>${locks.warnings}
    <details class="detail-section" data-detail-section="rules" ?open=${diagnosticsOpen}>
    <summary><h2>Rules</h2></summary>
    ${renderRules(d.rules)}
    </details>
    <h2>Preflight ${servicePreflightButton(d)}</h2>
    <div id="${detailDomId(d.name, "preflight")}" class="muted">not run yet</div>
    <h2>Recent events</h2>
    <table class="events">
      <caption class="visually-hidden">Recent service events</caption>
      <thead><tr><th scope="col">Time</th><th scope="col" class="event-kind">Event</th><th scope="col">Message</th></tr></thead>
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
      { windowKey: d.name, warn: isAdvisorySeverity(metric.severity) })
    : loadCheckMetric(d.name, metric, generation)));
  pending.push(...serviceScopedWatchMetricEntries(d.name).map((entry) => loadMetricPanel(d.name,
    serviceScopedWatchMetricDomID(d.name, entry.watch.name, entry.metric.name, "summary"),
    serviceScopedWatchMetricDomID(d.name, entry.watch.name, entry.metric.name, "chart"),
    generation,
    (win) => watchMetricsAPI(entry.watch.name, entry.metric.name, win),
    (body, win) => metricPanelContent(body, entry.metric.unit, win, `${serviceScopedWatchMetricLabel(entry)} chart`),
    serviceScopedWatchMetricLabel(entry))));
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

// watchMonitorHint says who last changed the monitoring state and when. Each
// part takes its own line under the badge — "active", then "via config", then
// the age — so the cell never runs together into one long line.
function watchMonitorHint(w) {
  const source = w.monitor_source ? fmtMonitorSource(w.monitor_source) : "";
  const age = w.monitor_changed_at ? fmtAge(w.monitor_changed_at) : "";
  if (!source && !age) return nothing;
  if (!source) return tpl`<br><span class="muted">${age}</span>`;
  if (!age) return tpl`<br><span class="muted">${source}</span>`;
  return tpl`<br><span class="muted">${source}<br>${age}</span>`;
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
  return tpl`${stateBadge(watchStateText(w))}${w.alarm?.silenced ? tpl` <span class="muted" title="Notifications resume when the watch state changes">alarm silenced</span>` : nothing}`;
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

function watchMatchesStatus(w, status) {
  if (!watchStatusFilterStates.includes(status)) return true;
  if (status === targetStateFailed) return isWatchAttention(w);
  return watchStateText(w) === status;
}

function watchFilterCounts(watches) {
  const counts = stateCounts(watches, watchStateText, watchStatusFilterStates);
  counts[targetStateFailed] = (watches || []).filter(isWatchAttention).length;
  return counts;
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
    <div><span class="muted">Used</span><br>${usedPct == null ? mutedDash : usageBar(usedPct)} <b>${fmtBytes(d.used_bytes)}</b></div>
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
	const field = r.field || "";
	const longValue = ["issuer", "dns_names"].includes(field) ||
	  ["raid_controller_", "raid_cache_", "raid_volume_", "raid_drive_"].some((prefix) => field.startsWith(prefix));
    const value = checkReadingValue(r);
    return tpl`<div class="watch-reading${longValue ? " watch-reading-long" : ""}"><span class="muted">${label}</span><br>${value}</div>`;
  });
  return tpl`<div class="watch-grid">${cells}</div>`;
}

const storageWatchTypes = new Set(["diskio", "hdparm", "lvm", "raid", "smart", "ssacli", "stale_mounts", "storcli", "storage"]);
const networkWatchTypes = new Set(["conntrack", "firewall_rules", "icmp", "net", "route", "tcp_connections"]);
const securityWatchTypes = new Set(["cert", "file", watchTypeUnownedProcesses]);
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

function watchActionDisabledReason(w, action) {
  if (!w || !w.enabled) return "disabled in config";
  if (watchStateText(w) === targetStateStarting) return "watch is starting";
  switch (action) {
    case actionSilence:
      if (w.alarm?.silenced) return "alarm silenced until the watch state changes";
      if (!w.alarm?.active) return "no active alarm";
      return w.alarm.revision ? "" : "alarm state unavailable";
    case actionMonitor:
      if (w.monitored) return "watch is already monitored";
      return pendingMonitorToggles.has(watchExpansionKey(w.name)) ? "monitoring change is pending" : "";
    case actionUnmonitor:
      if (!w.monitored) return "watch is paused";
      return pendingMonitorToggles.has(watchExpansionKey(w.name)) ? "monitoring change is pending" : "";
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
  const reason = watchActionDisabledReason(w, action);
  const disabled = reason !== "";
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
  if (action === actionSilence && w.alarm?.silenced) return `Alarm silenced for watch ${displayName(w) || w.name} until its state changes`;
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
function findReading(w, field) {
  return ((w && w.readings) || []).find((x) => x && x.field === field);
}

function readingRaw(w, field) {
  const r = findReading(w, field);
  return r && r.value != null ? String(r.value) : "";
}

// watchLastCell renders the shared "Last activity" cell content.
function watchLastCell(w) {
  return activityDateCell({
    time: w && w.last_activity,
    kind: w && w.last_activity_kind,
    severity: w && w.last_activity_severity,
  }, true, lastActivityMaxAgeMs);
}

function watchLastCheckedCell(w) {
  const checked = activityDateCell({ time: w && w.last_checked_at });
  if (watchSampleState(w) !== targetStateStale) return checked;
  return tpl`${checked}<span class="watch-sample-note" title="The latest completed watch sample is older than its freshness limit.">${stateBadgeLabel(targetStateStale, targetStateStale)}</span>`;
}

// watchNextCheckCell shows when the daemon starts the watch's next scheduled
// check, as a date and as a countdown updateClocks advances each second.
// A manual refresh is an extra check and does not move it.
function watchNextCheckCell(w) {
  const next = w.next_check_at;
  if (!next) return tpl`<span class="muted">${!w.enabled ? "disabled" : (!w.monitored ? "paused" : "—")}</span>`;
  return tpl`${activityDateCell({ time: next }, false)}<span class="watch-clock muted" data-clock-until="${next}"></span>`;
}

// watchActionsCell renders the shared actions cell (expand / monitor / unmonitor).
function watchActionsCell(w) {
  if (!w.enabled) return tpl`<td class="actions">${mutedDash}</td>`;
  const probeBtn = (w.can_probe && me.can_act)
    ? watchActionButton(w, actionProbe, tpl`<span aria-hidden="true">◎</span>`, true)
    : nothing;
  const raidButtons = (w.can_control_raid && me.can_act)
    ? tpl`${watchActionButton(w, actionPause, "pause RAID")} ${watchActionButton(w, actionResume, "resume RAID")}`
    : nothing;
  const replicationBtn = (w.can_control_replication && me.can_act)
    ? watchActionButton(w, actionReplicationStart, "start replication")
    : nothing;
  const expandBtn = (w.expand && Number(w.expand.by_bytes) > 0 && me.can_act)
    ? watchActionButton(w, actionExpand, `${actionExpand} ${fmtBytes(w.expand.by_bytes)}`)
    : nothing;
  const monitorBtn = me.can_act
    ? (w.monitored
      ? watchActionButton(w, actionUnmonitor, tpl`<span aria-hidden="true">${iconUnmonitor}</span>`, true)
      : watchActionButton(w, actionMonitor, tpl`<span aria-hidden="true">${iconMonitor}</span>`, true))
    : readOnlyNote;
  const alarmBtn = me.can_act ? watchActionButton(w, actionSilence, tpl`<span aria-hidden="true">${w.alarm?.silenced ? iconBellOff : iconBell}</span>`, true) : nothing;
  const actions = tpl`${probeBtn} ${raidButtons} ${replicationBtn} ${expandBtn} ${alarmBtn} ${monitorBtn}`;
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

// watchRowParts builds the shared watch row shell and its optional expansion.
// Callers supply only the cells that vary between the generic and typed views.
function watchRowParts(w, cells, colCount) {
  const state = watchStateText(w);
  const key = watchExpansionKey(w.name);
  const row = tpl`<tr id="wat-row-${w.name}" class="clickable ${watchRowClass(state)}" data-exp-key="${key}">
    <td>${expandToggle(key, displayName(w), "watch details", tpl`${displayName(w)}${watchScopeSuffix(w)}`)}</td>
    ${cells}
    <td>${watchLastCheckedCell(w)}</td>
    <td>${watchLastCell(w)}</td>
    <td>${watchStateCell(w)}</td>
    ${watchActionsCell(w)}
  </tr>`;
  return { row, expRow: expansionRow(key, colCount) };
}

// storageUsageCell renders the occupied-space progress bar (with used/total
// byte breakdown) for a storage watch, or a clear error/placeholder.
function storageUsageCell(w) {
  const d = w.storage;
  if (!d) return mutedDash;
  if (d.sample_error) return tpl`<span class="bad">${d.sample_error}</span>`;
  const usedPct = storageUsedPct(d);
  if (usedPct == null) return mutedDash;
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
  if (!health) return mutedDash;
  const cls = healthCellClass(health.toLowerCase());
  return tpl`<span class="${cls}">${health}</span>`;
}

function healthCellClass(health) {
  if (healthValuesOK.has(health)) return healthStatusOK;
  // A verdict the drive never gave is not a failing verdict: it reads as the
  // same warning an unknown backend status does.
  if (health === healthValueUnknown) return healthValueUnknown;
  // A controller reporting only advisories — error counters on members whose
  // state is still OK — is amber, the same grade its row carries.
  if (health === targetStateWarning) return "inactive";
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
    ssacli: "health",
    storcli: "health",
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
  return text ? tpl`${text}` : mutedDash;
}

function watchConditionValue(w, field) {
  const condition = (w.conditions || []).find((item) => item && item.field === field);
  if (!condition) return "—";
  return [condition.op, condition.value].filter(Boolean).join(" ") || "—";
}

// A reading with a number sorts by it, so "900 B" stays below "1.2 GiB";
// otherwise by the leading number of its text, or the text itself.
function readingSortValue(w, field) {
  const r = findReading(w, field);
  if (r && Number.isFinite(r.number)) return r.number;
  const raw = readingRaw(w, field);
  const number = Number.parseFloat(raw);
  return Number.isFinite(number) ? number : raw.toLowerCase();
}

function storageFilesystemCell(w) {
  const filesystem = w.storage && w.storage.filesystem;
  return filesystem ? tpl`<code>${filesystem}</code>` : mutedDash;
}

function storageMountCell(w) {
  const mount = w.storage && w.storage.mount_point;
  return mount ? tpl`<code>${mount}</code>` : mutedDash;
}

function typedReadingCell(w, field) {
  return readingValue(w, field);
}

function meterColumn(key, label, format) {
  return {
    key, label,
    cell: (w) => {
      const value = w.meter && w.meter[key];
      return value == null ? mutedDash : tpl`${format(value)}`;
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
      return value == null ? mutedDash : tpl`${format(value)}`;
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
  storcli: {
    label: "MegaRAID hardware",
    columns: [
      { key: "health", label: "Health", cell: watchHealthCell, sort: (w) => readingRaw(w, "health").toLowerCase() },
      numericReadingColumn("raid_volumes", "Volumes"),
      numericReadingColumn("raid_drives", "Drives"),
	  numericReadingColumn("raid_progress_pct", "RAID progress"),
      numericReadingColumn("temperature", "Max temperature"),
      numericReadingColumn("raid_media_errors", "Media errors"),
      numericReadingColumn("raid_smart_alerts", "SMART alerts"),
    ],
  },
  ssacli: {
    label: "HPE Smart Array hardware",
    columns: [
      { key: "health", label: "Health", cell: watchHealthCell, sort: (w) => readingRaw(w, "health").toLowerCase() },
      numericReadingColumn("raid_volumes", "Volumes"),
      numericReadingColumn("raid_drives", "Drives"),
	  numericReadingColumn("raid_progress_pct", "RAID progress"),
      numericReadingColumn("temperature", "Max temperature"),
      numericReadingColumn("raid_media_errors", "Media errors"),
      numericReadingColumn("raid_smart_alerts", "SMART alerts"),
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
  coredump: {
    label: "Core dumps",
    columns: [numericReadingColumn("dumps", "Core dumps")],
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
  [watchTypeUnownedProcesses]: {
    label: "Unowned processes",
    columns: [
      numericReadingColumn("scanned", "Scanned"),
      numericReadingColumn("violation_count", "Unowned"),
      textReadingColumn("package_db", "Package DB"),
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
  stale_mounts: {
    label: "Stale mounts",
    columns: [
      numericReadingColumn("count", "Stale"),
      textReadingColumn("mounts", "Mounts"),
    ],
  },
  usb: {
    label: "USB devices",
    columns: [
      textReadingColumn("device", "Device"),
      textReadingColumn("model", "Model"),
      textReadingColumn("serial_number", "Serial"),
      textReadingColumn("bus_port", "Port"),
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
      numericReadingColumn("pending_bytes", "Pending"),
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

function watchTypeSort(type) {
  const saved = watchPanel.typeSorts[type];
  return saved && typeof saved.key === "string" ? saved : { key: "name", dir: 1 };
}

function setWatchTypeSort(type, key) {
  const sort = watchTypeSort(type);
  if (sort.key === key) sort.dir = -sort.dir;
  else { sort.key = key; sort.dir = 1; }
  watchPanel.typeSorts[type] = sort;
  updatePanel(watchPanel, {});
}

function setWatchTypeFilter(type, value) {
  watchPanel.typeFilters[type] = value || filterAll;
  updatePanel(watchPanel, {});
}

function watchTypeRows(type, watches, profile) {
  const filter = profile.filter;
  const selected = watchPanel.typeFilters[type] || filterAll;
  const list = filter && selected !== filterAll ? watches.filter((w) => filter.value(w) === selected) : [...watches];
  const sort = watchTypeSort(type);
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

function watchTypeFilterControl(type, watches, profile) {
  if (!profile.filter) return nothing;
  const counts = new Map();
  watches.forEach((w) => {
    const value = profile.filter.value(w);
    counts.set(value, (counts.get(value) || 0) + 1);
  });
  if (counts.size < 2) return nothing;
  const selected = counts.has(watchPanel.typeFilters[type]) ? watchPanel.typeFilters[type] : filterAll;
  return tpl`<label class="watch-type-filter">${profile.filter.label}
    <select data-watch-type-filter="${type}" aria-label="Filter ${watchTypeLabel(type)} by ${profile.filter.label}">
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

function renderWatchTypeTable(type, watches) {
  const profile = watchTypeProfile(type);
  const list = watchTypeRows(type, watches, profile);
  const sort = watchTypeSort(type);
  const columns = [{ key: "name", label: "Name" }, ...profile.columns, { key: "checked", label: "Last checked" }, { key: "last", label: "Last activity" }, { key: "state", label: "State" }, { label: "Actions" }];
  const rows = list.flatMap((watch) => typedWatchRowHTML(watch, profile));
  return tpl`<section class="watch-type-group">
    <div class="watch-type-heading"><h3>${watchTypeLabel(type)} <span class="muted">${watches.length}</span></h3>${watchTypeFilterControl(type, watches, profile)}</div>
    <table class="watch-table watch-type-table">
      <thead><tr>${columns.map((column) => column.key
        ? tpl`<th scope="col" class="sortable" tabindex="0" data-watch-type-column="${column.key}" data-watch-type-sort-type="${type}" data-watch-type-sort="${column.key}" aria-sort="${sortAriaValue(sort, column.key)}">${column.label}<span class="sort-ind">${sortArrow(sort, column.key)}</span></th>`
        : tpl`<th scope="col">${column.label}</th>`)}</tr></thead>
      <tbody>${rows.length ? rows : tpl`<tr><td colspan="${columns.length}" class="muted">No ${watchTypeLabel(type).toLowerCase()} watches match this filter.</td></tr>`}</tbody>
    </table>
  </section>`;
}

// renderWatchGroups is the watch panel's body: one collapsible heading per
// group and, under it, one table per check type.
function renderWatchGroups(watches) {
  return sortedGroupValues(watches, watchGroupOf).flatMap((group) => {
    const groupWatches = watches.filter((watch) => watchGroupOf(watch) === group);
    const types = sortedGroupValues(groupWatches, watchTypeKey);
    return [groupHeaderRow(watchPanel, group, groupWatches),
      watchPanel.collapsedGroups.has(group) ? nothing : tpl`<tr><td class="watch-group-cell">${types.map((type) => renderWatchTypeTable(type, groupWatches.filter((watch) => watchTypeKey(watch) === type)))}</td></tr>`];
  });
}

function renderWatches(watches) {
  if (watches) allWatches = watches;
  scheduleGlobalTargetSync();
  preserveExpansionCells(isWatchExpansionKey);
  renderListPanel(watchPanel);
  updateClocks();
  reassertExpansions();
  applyHash();
  refreshChrome();
}

// ---- Installed applications and libraries ---------------------------------
// Both are artifact panels: what is installed, its version and whether its
// check passes. An application adds its last activity, its recent events and,
// when it maps to a monitored service, that service's availability.
const appStateLabels = {
  [targetStateOK]: "Ok",
  [targetStateStarting]: "Starting",
  [targetStateWarning]: "Warning",
  [targetStateFailed]: "Failed",
};
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
  return tpl`<td data-col="state" class="status-cell status-${state}" title="${detail}">${stateBadgeLabel(state, appStatusLabel(a))}</td>`;
}

function artifactSearchText(item, surface) {
  return [
    displayName(item), item.name, item.display_name, categoryOf(item, surface), appStateText(item),
    item.status, item.version, item.user, item.group,
  ].filter(Boolean).join(" ").toLowerCase();
}

// renderApps lists the installed applications. The version column shows the
// short version; expanding a row reveals the full version string, binary
// location, permissions, user and group (all already in hand, so no extra
// request is needed).
function renderApps(apps) {
  if (apps) allApps = apps;
  renderArtifactPanel(listPanels.apps);
  // An application expansion is part of the rows template, so every render
  // repaints it and its events and availability are filled again.
  (allApps || []).forEach((app) => {
    if (!expanded.has(appExpansionKey(app.name))) return;
    loadAppEvents(app.name);
    if (app.keeps_sla) loadAppSLA(app.name);
  });
}

function renderLibraries(libraries) {
  if (libraries) allLibraries = libraries;
  renderArtifactPanel(listPanels.libraries);
}

function renderArtifactPanel(p) {
  scheduleGlobalTargetSync();
  renderListPanel(p);
  applyHash();
  refreshChrome();
}

function renderArtifactRow(item, p) {
  const state = appStateText(item);
  const rowClass = state === targetStateFailed ? "row-failing" : (state === targetStateWarning ? "row-warning" : "");
  const label = displayName(item);
  const key = p.expansionKey(item.name);
  const row = tpl`<tr id="${p.rowId(item)}" class="clickable ${rowClass}" data-exp-key="${key}">
    <td data-col="name">${expandToggle(key, label, p.detailLabel)}</td>
    ${p.columns.some((c) => c.key === "category") ? tpl`<td data-col="category">${categoryBadge(p.groupOf(item))}</td>` : nothing}
    ${appStatusCell(item)}
    <td data-col="version">${item.version_short || item.version || "—"}</td>
    ${p.columns.some((c) => c.key === "last") ? tpl`<td data-col="last">${lastEventCell(item)}</td>` : nothing}
  </tr>`;
  // An application's or a library's detail is plain markup, drawn with the row.
  const expansion = expanded.has(key) ? expansionRow(key, p.columns.length, p.renderExpansion(item)) : null;
  return expansion ? [row, expansion] : [row];
}

function renderArtifactExpansionDetails(artifact, surface) {
  const bin = artifact.binary ? tpl`<code>${artifact.binary}</code>` : tpl`<span class="muted">unknown</span>`;
  const perm = artifact.permissions ? tpl`<code>${artifact.permissions}</code>` : mutedDash;
  const usr = artifact.user || mutedDash;
  const grp = artifact.group || mutedDash;
  const source = artifact.version_source
    ? tpl`<code>${artifact.version_source}</code>`
    : (artifact.version ? tpl`<span class="muted">local</span>` : mutedDash);
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
  return tpl`${renderArtifactExpansionDetails(a, defaultCategoryApp)}
  ${a.keeps_sla ? renderSLASection(appSLAKey(a.name)) : nothing}
  <h3 class="expansion-heading">Recent events</h3>
  <table class="events">
    <caption class="visually-hidden">Recent application events</caption>
    <thead><tr><th scope="col">Time</th><th scope="col" class="event-kind">Event</th><th scope="col">Message</th></tr></thead>
    <tbody id="${eventsId}"></tbody></table>`;
}

function renderLibraryExpansion(library) {
  return renderArtifactExpansionDetails(library, defaultCategoryLibrary);
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
    <div><span class="muted">Next check</span><br>${watchNextCheckCell(w)}</div>
    <div><span class="muted">Hook</span><br>${hook}</div>
    <div><span class="muted">Notifies</span><br>${notifiers}</div>
    <div><span class="muted">Dry run</span><br><b>${w.dry_run ? "yes" : "no"}</b></div>
  </div>`;
  const live = tpl`${renderStorageWatch(w.storage)}${renderMeterWatch(w.meter)}${renderWatchReadings(w.readings)}${renderWatchProcessesSection(w)}${renderCountHoldersSection(w)}${renderWatchMetricsSection(w)}${w.keeps_sla ? renderSLASection(watchSLAKey(w.name)) : nothing}`;
  const conditions = renderConditionRows(w.conditions || []);
  if (!events || !events.length) return tpl`${cfg}${live}${conditions}<div class="muted">No recent activity.</div>`;
  const rows = eventRows(events.slice(0, 50), false, { prefix: `watch:${w.name}` });
  return tpl`${cfg}${live}${conditions}<table class="events events-compact-table">
    <caption class="visually-hidden">Recent watch activity</caption>
    <thead><tr><th scope="col">Time</th><th scope="col" class="event-kind">Event</th><th scope="col">Message</th></tr></thead>
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
  if (!items.length) return mutedDash;
  const extra = items.length > maxRows ? tpl`<span class="muted mount-usage-extra">+${items.length - maxRows} more</span>` : nothing;
  return tpl`<span title="${items.join("\n")}">${items.slice(0, maxRows).map((item) => tpl`<span class="mount-usage-item">${item}</span>`)}${extra}</span>`;
}

function mountProcessesCell(m) {
  if (m.blocker_error) return tpl`<span class="bad" title="${m.blocker_error}">error</span>`;
  return mountUsageCell(mountBlockers(m).map(mountProcessLabel));
}

function mountUsersCell(m) {
  if (m.blocker_error) return mutedDash;
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

function mountOperationReason(op) {
  if (!op) return "";
  return op.message || `${op.state || mountOperationState(op.action)} in progress`;
}

function mountPathCell(m) {
  const op = mountOperation(m);
  const note = op
    ? tpl`<span class="mount-operation" title="${mountOperationReason(op)}">${op.state || mountOperationState(op.action)}</span>`
    : nothing;
  return tpl`<code>${m.path || ""}</code>${note}`;
}

function mountSearchText(m) {
  return [
    displayName(m), m.name || "", m.display_name || "", categoryOf(m, defaultCategoryStorage), m.path || "",
    mountStateText(m), ...mountBlockers(m).map(mountProcessLabel), ...mountUserNames(m),
  ].join(" ").toLowerCase();
}

function mountRowId(m) {
  return `mount-row-${detailDomKey(m.name || m.path || "mount")}`;
}

function mountRow(m) {
  const mounted = !!m.mounted;
  const state = mountStateText(m);
  return tpl`<tr id="${mountRowId(m)}" tabindex="-1" title="${m.message || nothing}" class="${mountOperation(m) ? "mount-row-operating" : ""}">
    <td data-col="name">${m.display_name || m.name}</td>
    <td data-col="path">${mountPathCell(m)}</td>
    <td data-col="mounted">${mounted ? tpl`<span class="ok">yes</span>` : tpl`<span class="muted">no</span>`}</td>
    <td data-col="refcount">${m.refcounted === false ? tpl`<span class="muted">off</span>` : Number(m.refcount || 0)}</td>
    <td data-col="processes">${mountProcessesCell(m)}</td>
    <td data-col="users">${mountUsersCell(m)}</td>
    <td data-col="state"><span class="target-state ${mountStateClass(state, mounted)}">${state}</span></td>
    <td data-col="actions" class="actions" data-mount-row="${m.name || ""}">${mountActionButtons(m, mounted)}</td>
  </tr>`;
}

function renderMounts(mounts) {
  if (mounts) allMounts = mounts;
  scheduleGlobalTargetSync();
  renderListPanel(listPanels.mounts);
  refreshChrome();
}

function mountActionButton(m, action, glyph, label, title, disabled, hintId) {
  return tpl`<button class="icon-btn" data-mount="${m.name || ""}" data-mount-action="${action}" aria-label="${label}" title="${title}" ?disabled=${disabled} aria-describedby="${hintId || nothing}"><span aria-hidden="true">${glyph}</span></button>`;
}

function mountActionButtons(m, mounted) {
  if (!me.can_act) return readOnlyNote;
  const label = m.display_name || m.name || m.path || "mount";
  const operation = mountOperation(m);
  const busy = mountOperationReason(operation);
  if (!mounted) return mountActionButton(m, actionMount, glyphPlay, `Mount ${label}`, busy || `Mount ${label}`, !!operation);
  const blocked = mountUmountDisabledReason(m);
  const hintId = blocked ? `mount-${detailDomKey(m.name || m.path || "mount")}-umount-hint` : "";
  const disabled = !!(blocked || operation);
  return tpl`${blocked ? hiddenDescription(hintId, blocked) : nothing}${mountActionButton(m, actionUmount, "⏏\uFE0E", `Unmount ${label}`, blocked || busy || `Unmount ${label}`, disabled, hintId)}${mountActionButton(m, actionAlert, "!", `Alert users blocking ${label}`, blocked || busy || `Alert users blocking ${label}`, disabled, hintId)}`;
}

function mountUmountDisabledReason(m) {
  if (!m || m.can_umount !== false) return "";
  return m.umount_disabled_reason || "root filesystem cannot be unmounted";
}

function renderNotifiers(notifiers) {
  const section = $("#notifiers-section");
  const tbody = $("#notifier-rows");
  const cnt = $("#notifiers-count");
  if (!section || !tbody) return;
  const total = (notifiers || []).length;
  setPanelVisible(section, total > 0);
  if (cnt) cnt.textContent = total ? String(total) : "";
  refreshChrome();
  if (!total) return;
  const rows = notifiers.map((n) => {
    const enabled = n.enabled !== false;
    const state = enabled ? "enabled" : "disabled";
    const cls = enabled ? "state-monitored" : "state-disabled";
    const dest = n.summary ? esc(n.summary) : '<span class="muted">—</span>';
    const used = Number(n.used_by || 0);
    const watches = used ? String(used) : '<span class="muted">—</span>';
    // debug filters nothing: the notifier hears every level.
    const floor = severityLevels.includes(n.min_severity) && n.min_severity !== severityLevels[0]
      ? `<span class="sev sev-${esc(n.min_severity)}">≥ ${esc(n.min_severity)}</span>`
      : '<span class="muted">all</span>';
    const test = enabled && me.can_act
      ? `<button class="icon-btn" data-notifier-test="${esc(n.name)}" aria-label="Send test notification to ${esc(n.name)}" title="Send test notification to ${esc(n.name)}"><span aria-hidden="true">${glyphPlay}</span></button>`
      : '<span class="muted">—</span>';
    return `<tr><td>${esc(n.name)}</td><td>${esc(n.type)}</td><td>${floor}</td><td class="muted">${dest}</td><td>${watches}</td><td class="${cls}">${state}</td><td class="actions">${test}</td></tr>`;
  });
  setHTMLIfChanged(tbody, rows.join(""));
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
  set("#daemon-hostname", info.hostname);
  set("#daemon-os", info.os);
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
  if (!l.expires_at) return mutedDash;
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
  return mutedDash;
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

function lockReleaseButton(l) {
  if (!me.can_act) return nothing;
  const disabled = !l.releaseable;
  const reason = l.state === lockStateActive ? "lock is still active" : "lock cannot be released";
  const hintID = lockReleaseHintId(l);
  return tpl`${actionHint(hintID, disabled, reason)}<button class="danger-btn" ?disabled=${disabled} data-lock-release="1" data-lock-service="${l.service || ""}" data-lock-name="${l.name || ""}" aria-label="${lockReleaseLabel(l)}" aria-describedby="${actionDescribedBy(hintID, disabled, reason)}">release</button>`;
}

function lockServiceLink(l) {
  const svc = l.service || "";
  if (!svc) return mutedDash;
  return tpl`<button type="button" class="name row-toggle" data-service-open="${svc}" aria-label="Open service ${svc}">${svc}</button>`;
}

async function releaseLock(service, name) {
  const generation = dashboardGeneration;
  const label = name ? `${service}.${name}` : service;
  await runAction({
    confirm: () => promptConfirm({
      title: `Release lock ${label}?`,
      message: `Release inactive lock "${label}"?`,
      okLabel: "release",
      danger: true,
    }),
    request: () => fetch(lockReleaseAPI(service, name ? `?${apiQueryName}=${encodeURIComponent(name)}` : ""), targetPostOptions({}, generation)),
    ok: () => `released lock ${label}`,
    fail: `release ${label}`,
  });
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
    refreshChrome();
    return;
  }
  setPanelVisible(section, true);
  if (cnt) cnt.textContent = String(locks.length);
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
  refreshChrome();
}

function renderActivity(sum) {
  if (!sum) return;
  latestActivity = sum;
  refreshChrome();
}

function attnAriaLabel(it) {
  const parts = [it.title];
  if (it.detail) parts.push(it.detail);
  parts.push(`Open ${panelTargetLabel(it.target)}`);
  return parts.join(". ");
}


// tileAriaLabel names a tile for assistive tech: what it reads — spelled out
// where the visible value is shorthand ("17 of 21" for "17 / 21"), and "loading"
// while it has no reading yet — and the section a press leads to.
function tileAriaLabel({ name, label, reading, sub, view }) {
  if (reading === undefined) return `${name || label} loading`;
  return [`${name || label}: ${reading}`, sub, `Go to the ${view} section`].filter(Boolean).join(". ");
}

function tileGaugeId(key) {
  return `tile-${key}-gauge`;
}

// firstMatch returns the value paired with the first truthy condition, or the
// fallback when none match. The overview picks a tile's class and subtitle from
// ordered priority lists; written as nested ternaries those chains are the same
// logic but unreadable.
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

// overviewTile renders one band tile. A tile reads its view and leads to it:
// a press scrolls to the view's first panel.
const overviewTile = (opts) => tpl`
  <button class="tile ${opts.cls || ""}" data-open-view="${opts.view}" aria-label="${tileAriaLabel(opts)}" aria-describedby="${opts.describedBy || nothing}">
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
  const daemon = source.daemon || {};
  const hostMetrics = source.hostMetrics || [];
  const svcs = allServices || [];
  const enabled = svcs.filter((s) => s.enabled);
  const failedSvcs = svcs.filter((s) => serviceDisplayState(s) === targetStateFailed);
  const startingSvcs = svcs.filter((s) => serviceDisplayState(s) === targetStateStarting);
  const collectingSvcs = svcs.filter((s) => serviceDisplayState(s) === targetStateCollecting);
  // Warning services need operator attention: either a process-attribution gap
  // or a failed init unit whose verified workload remains healthy.
  const warningSvcs = svcs.filter((s) => serviceDisplayState(s) === targetStateWarning);
  const restartRequiredSvcs = svcs.filter((s) => serviceDisplayState(s) === targetStateRestartRequired);
  const activeSvcs = enabled.filter((s) => overviewActiveServiceStates.includes(serviceDisplayState(s)));
  const watches = allWatches || [];
  const enabledWatches = watches.filter((w) => w && w.enabled);
  const failedWatches = watches.filter(isWatchAttention);
  const warningWatches = watches.filter(isWatchWarning);
  const staleWatches = watches.filter(isWatchSampleStale);
  const startingWatches = watches.filter((w) => watchStateText(w) === targetStateStarting);
  const startingApps = (allApps || []).filter((a) => appStateText(a) === targetStateStarting);
  const daemonStarting = ready && ready.status === daemonStatusStarting && ready.ready === false;
  const settling = daemonStarting || startingSvcs.length > 0 || startingWatches.length > 0 || startingApps.length > 0;
  return {
    ready, live, daemon, hostMetrics, enabled, failedSvcs, startingSvcs, collectingSvcs,
    warningSvcs, restartRequiredSvcs, activeSvcs, watches, enabledWatches, failedWatches,
    warningWatches, staleWatches, startingWatches, startingApps, daemonStarting, settling,
  };
}

// loginsText is the host's interactive logins as the daemon counts them
// (console and SSH), the subtitle of the Sessions tile.
function loginsText(daemon) {
  if (daemon.sessions) return `logins: ${Number(daemon.sessions.console) || 0} console · ${Number(daemon.sessions.ssh) || 0} SSH`;
  if (daemon.active_users != null) return `${daemon.active_users || 0} users`;
  return "";
}

// sessionsListed reports whether the Sessions panel has anything to show, and
// with it whether the band carries a Sessions tile.
function sessionsListed() {
  return !!(sessionPanel.dom && panelVisible(sessionPanel.dom.section));
}

// The view tiles, one builder each. A view with nothing to list has no tile,
// and its builder returns null.

function servicesTile(data) {
  // A host with no service, container, VM or lock has no Services panel to go to.
  if (!viewPanels(viewServices).some(panelVisible)) return null;
  const {
    enabled, failedSvcs, startingSvcs, collectingSvcs, warningSvcs, restartRequiredSvcs, activeSvcs,
    startingWatches, startingApps, daemonStarting, settling,
  } = data;
  const settlingText = settlingSub(startingSvcs.length, [
    [daemonStarting, "daemon starting"],
    [startingWatches.length, `${startingWatches.length} watch starting`],
    [startingApps.length, `${startingApps.length} app starting`],
  ]);
  return {
    label: "Services",
    value: tpl`${activeSvcs.length}<small> / ${enabled.length}</small>`,
    cls: firstMatch([
      [failedSvcs.length, "t-crit"],
      [collectingSvcs.length || warningSvcs.length || restartRequiredSvcs.length, "t-warn"],
      [settling, ""],
      [enabled.length, "t-ok"],
    ], ""),
    sub: firstMatch([
      [failedSvcs.length, `${failedSvcs.length} failed`],
      [settlingText, settlingText],
      [collectingSvcs.length, `${collectingSvcs.length} collecting`],
      [warningSvcs.length, `${warningSvcs.length} need attention`],
      [restartRequiredSvcs.length, `${restartRequiredSvcs.length} restart required`],
      [enabled.length === 0, "none enabled"],
    ], "all active"),
    view: viewServices,
    name: "Services active",
    reading: `${activeSvcs.length} of ${enabled.length}`,
  };
}

function watchesTile(data) {
  const {
    watches, enabledWatches, failedWatches, warningWatches, staleWatches,
    startingSvcs, startingWatches, startingApps, daemonStarting, settling,
  } = data;
  if (!watches.length) return null;
  const settlingText = settlingSub(startingWatches.length, [
    [daemonStarting, "daemon starting"],
    [startingSvcs.length, `${startingSvcs.length} svc starting`],
    [startingApps.length, `${startingApps.length} app starting`],
  ]);
  // A single predicate over the enabled set: subtracting the failed/warning/stale
  // list lengths (which span all watches and can overlap) could go negative.
  const watchesUp = enabledWatches.filter((w) => !isWatchAttention(w) && !isWatchWarning(w) && !isWatchSampleStale(w)).length;
  return {
    label: "Watches",
    value: tpl`${watchesUp}<small> / ${enabledWatches.length}</small>`,
    cls: firstMatch([
      [failedWatches.length, "t-crit"],
      [warningWatches.length || staleWatches.length, "t-warn"],
      [settling, ""],
    ], "t-ok"),
    sub: firstMatch([
      [failedWatches.length, `${failedWatches.length} firing`],
      [warningWatches.length, `${warningWatches.length} warning`],
      [staleWatches.length, `${staleWatches.length} stale`],
      [settlingText, settlingText],
    ], "quiet"),
    view: viewWatches,
    reading: `${watchesUp} of ${enabledWatches.length}`,
  };
}

function mountsTile() {
  const mounts = allMounts || [];
  if (!mounts.length) return null;
  const mounted = mounts.filter((m) => m.mounted).length;
  const errors = mounts.filter((m) => mountBaseStateText(m) === mountStateError).length;
  return {
    label: "Mounts",
    value: tpl`${mounted}<small> / ${mounts.length}</small>`,
    // A unit that is not mounted is not a fault by itself — many are mounted on
    // demand — so only an error colours the tile.
    cls: errors ? "t-crit" : "",
    sub: firstMatch([
      [errors, `${errors} in error`],
      [mounted < mounts.length, `${mounts.length - mounted} not mounted`],
    ], "all mounted"),
    view: viewMounts,
    reading: `${mounted} of ${mounts.length} mounted`,
  };
}

function sessionsTile(daemon) {
  if (!sessionsListed()) return null;
  const count = String(sessionCount());
  return { label: "Sessions", value: count, sub: loginsText(daemon), view: viewSessions, reading: count };
}

function softwareTile() {
  const software = [...(allApps || []), ...(allLibraries || [])];
  if (!software.length) return null;
  const failed = software.filter((item) => appStateText(item) === targetStateFailed).length;
  const warning = software.filter((item) => appStateText(item) === targetStateWarning).length;
  return {
    label: "Software",
    value: String(software.length),
    cls: firstMatch([[failed, "t-crit"], [warning, "t-warn"]], "t-ok"),
    sub: firstMatch([[failed, `${failed} failed`], [warning, `${warning} warning`]], "all ok"),
    view: viewSoftware,
    reading: String(software.length),
  };
}

// Every host has an event log, so the band always carries its tile.
function eventsTile() {
  const errors = (latestActivity && latestActivity.errors) || 0;
  const lastKind = latestActivity && latestActivity.last_event_kind;
  const unit = errors === 1 ? "error" : "errors";
  return {
    label: "Events",
    value: tpl`${errors}<small> ${unit}</small>`,
    cls: errors ? "t-warn" : "",
    sub: lastKind ? `last: ${lastKind}` : "",
    view: viewEvents,
    reading: `${errors} recent ${unit}`,
  };
}

// overviewViewTiles are the tiles that lead to a view of targets, in band
// order.
function overviewViewTiles(data) {
  return [
    servicesTile(data), watchesTile(data), mountsTile(), sessionsTile(data.daemon), softwareTile(), eventsTile(),
  ].filter(Boolean);
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
  { metric: hostMetricTotalCPU, gauge: metricNameCPU, label: "CPU", name: "Host CPU" },
  { metric: hostMetricTotalMemory, gauge: "mem", label: "Memory", name: "Host memory", capacity: true },
  { metric: hostMetricTotalSwap, gauge: "swap", label: "Swap", name: "Host swap", capacity: true, requireTotal: true, level: overviewSwapLevel },
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
    view: viewDaemon,
    name: descriptor.name,
    reading: loading ? undefined : fmtPct(pct),
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
      view: viewDaemon,
      reading: loading ? undefined : fmtNum(load.absolute, 2),
      describedBy: loading ? nothing : gaugeId,
    });
  }
  return tiles;
}

function renderOverview(ctx) {
  const band = $("#overview");
  if (!band) return;
  const data = overviewInventory(ctx);
  const hostTiles = overviewHostTileOptions(data);
  // The host tiles lead to the Daemon panel; a daemon that reports no host
  // reading still needs one.
  if (!hostTiles.length) {
    const status = data.ready.status === healthStatusOK ? "running" : (data.ready.status || "—");
    hostTiles.push({ label: "Daemon", value: status, reading: status, view: viewDaemon });
  }
  const tiles = [...overviewViewTiles(data), ...hostTiles];
  // The stylesheet lays the band out as whole rows — one on a wide screen, two
  // on a narrower one — and needs the columns of each layout.
  band.style.setProperty("--tiles-wide", String(tiles.length));
  band.style.setProperty("--tiles-half", String(Math.ceil(tiles.length / 2)));
  litRender(tiles.map(overviewTile), band);
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

// daemonStateIcons are the marks of the daemon's lifecycle in the bar, as the
// stroke paths of a 24x24 icon: a pulse while it runs, a clock while it starts,
// a warning sign in panic mode and a power sign while it shuts down.
const daemonStateIcons = {
  running: '<path d="M22 12h-4l-3 9L9 3l-3 9H2"/>',
  starting: '<circle cx="12" cy="12" r="9"/><polyline points="12 7 12 12 15.5 14"/>',
  panic: '<path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0Z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/>',
  stopping: '<path d="M18.36 6.64a9 9 0 1 1-12.73 0"/><line x1="12" y1="2" x2="12" y2="12"/>',
};

// daemonStateView is how the daemon's own lifecycle reads in the bar — not the
// health of what it monitors, which the brand dot and the tiles carry. Running
// is the icon alone; every other state adds its word, so none rests on the
// icon's colour. The label is the full sentence for a tooltip or a screen
// reader, with the startup progress the daemon reports.
function daemonStateView(ready) {
  const status = ready.status || (ready.ready ? healthStatusOK : "");
  if (ready.panic) return { icon: "panic", cls: "status-panic", text: "panic mode", label: "Daemon in panic mode: hooks, alerts and automatic remediation are suspended" };
  if (status === daemonStatusStarting) return { icon: "starting", cls: "status-starting", text: "starting", label: ready.message ? `Daemon ${ready.message}` : "Daemon starting" };
  if (status === daemonStatusShuttingDown) return { icon: "stopping", cls: "inactive", text: "stopping", label: "Daemon shutting down" };
  if (status === healthStatusOK) return { icon: "running", cls: healthStatusOK, text: "", label: "Daemon running" };
  return { icon: "starting", cls: healthStatusMuted, text: status || "—", label: `Daemon ${status || "state unknown"}` };
}

function renderStatus(ctx) {
  const bar = $("#statusbar");
  if (!bar) return;
  try {
    const { ready, live, locks, daemon, hostMetrics } = ctx || {};
    latestReady = ready || {};
    latestLocks = locks || [];
    latestHostMetrics = hostMetrics || [];
    if (daemon && daemon.hostname) rememberDashboardHostname(daemon.hostname);

    // The bar's first line is the host: its name, how long it has been up and
    // the daemon's state as an icon. What is fixed about the host (type,
    // backend, OS) reads once beside it and in full in the Daemon panel.
    // Counts and vital signs live on the tiles, so they are not repeated.
    const host = $("#host-name");
    if (host) host.textContent = daemon.hostname || "";
    const state = daemonStateView(ready);
    const logins = sessionsListed() ? "" : loginsText(daemon);
    setHTMLIfChanged($("#host-status"),
      `<span class="stat stat-uptime" title="Host uptime">up <b>${esc(fmtDuration(daemon.host_uptime_seconds) || "—")}</b></span>`
      + `<span class="daemon-state ${state.cls}" role="img" aria-label="${esc(state.label)}" title="${esc(state.label)}">`
      + `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${daemonStateIcons[state.icon]}</svg>`
      + `${state.text ? `<span>${esc(state.text)}</span>` : ""}</span>`
      // The logins are the Sessions tile's subtitle; a host with nothing to
      // list in that panel has no such tile, so they read here instead.
      + (logins ? `<span class="stat stat-sessions">${esc(logins)}</span>` : ""));
    const stats = [];
    const hostType = hostTypeDisplay(daemon.host_type);
    if (hostType.label) {
      const title = hostType.title ? ` title="${esc(hostType.title)}"` : "";
      stats.push(["type", `<span${title}>${esc(hostType.label)}</span>`]);
    }
    if (ready.backend) stats.push(["backend", esc(ready.backend)]);
    if (daemon.os) stats.push(["os", esc(daemon.os)]);
    setHTMLIfChanged(bar, stats.map(([key, text]) => `<span class="stat stat-${key}">${text}</span>`).join(""));
    updatePanicView(ready.panic);
    renderOverview({ ready, live, daemon, locks, hostMetrics });

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
    refreshChrome();
  } catch {
    // Beside the host, the one place of the bar every layout shows.
    const status = $("#host-status");
    if (status) status.textContent = "status unavailable";
  }
}

async function act(name, action) {
  const generation = dashboardGeneration;
  let noCascade = false;
  if (isServiceConfirmAction(action) && !(await confirmAction(name, action))) return;
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
    const res = await fetch(serviceAPI(name, apiActionSuffix(action, q)), targetPostOptions({}, generation));
    const body = await jsonOrThrow(res);
    if (tracked) finishOperation(name, eventStatusOK, body.message || "operation completed");
  } catch (e) {
    // A refusal's result status tells a blocked operation (the engine touched
    // nothing) from a failed one; any other error is a failure.
    const outcome = operationMarkOutcome(e.resultStatus) || targetStateFailed;
    if (tracked) finishOperation(name, outcome, e.message);
    setStatus(`${action} ${name}${outcome === eventStatusBlocked ? " blocked" : ""}: ${e.message}`, operationOutcomeStyles[outcome].tone);
  } finally {
    releaseMonitorToggleAfterRefresh(toggleKey, renderServices);
  }
}

async function closeSSHSession(name, pid, startTicks, terminal, user, managedByLogind) {
  const generation = dashboardGeneration;
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
  await postSessionClose(`close SSH session ${label}`, sshSessionCloseAPI(name, sessionPID, sessionStartTicks, terminal, managedByLogind), generation,
    sshSessionActionKey(name, sessionPID, sessionStartTicks), sessionActionClosing, name);
}

// postPendingRowAction runs one confirmed close or kill of a listed row. Like a
// service operation it shows its progress in place: the row's action cell reads
// "closing…"/"killing…" from the request until the list drops the row. rerender
// redraws the view that owns the row so the badge appears at once.
async function postPendingRowAction(statusLabel, endpoint, generation, key, verb, rerender, target) {
  const existing = pendingSessionAction(key);
  if (existing && !existing.error) return;
  const previous = pendingRowTargets.get(target);
  let release;
  const completion = new Promise((resolve) => { release = resolve; });
  pendingRowTargets.set(target, completion);
  const pending = { verb: previous ? sessionActionQueued : verb, label: statusLabel, started: Date.now(), done: false };
  pendingSessionActions.set(key, pending);
  try {
    setStatus("");
    rerender();
    if (previous) await previous;
    pending.verb = verb;
    rerender();
    const res = await fetch(endpoint, targetPostOptions({}, generation));
    const body = await jsonOrThrow(res);
    pending.done = true;
    pending.started = Date.now();
    pending.label = `${statusLabel}: ${body.message || feedbackStatusOK}`;
    setStatus(pending.label, feedbackStatusOK);
    load();
  } catch (e) {
    pending.done = true;
    pending.error = e.message;
    rerender();
    setStatus(`${statusLabel}: ${e.message}`, feedbackStatusErr);
  } finally {
    if (pendingRowTargets.get(target) === completion) pendingRowTargets.delete(target);
    release();
  }
}

// postSessionClose is postPendingRowAction for the session inventory.
function postSessionClose(statusLabel, endpoint, generation, key, verb, service) {
  return postPendingRowAction(statusLabel, endpoint, generation, key, verb, renderSessions, serviceExpansionKey(service));
}

async function closeTerminalSession(service, check, multiplexer, session, user, identity) {
  const generation = dashboardGeneration;
  if (!service || !check || !session || !user || !identity || !terminalSessionKinds.includes(multiplexer)) {
    setStatus("close terminal session: invalid session identity; refresh and try again", feedbackStatusErr);
    return;
  }
  const label = `${multiplexer} ${session} for ${user}`;
  if (!(await promptConfirm({
    title: `Close ${label}?`,
    message: "This closes the entire session, including all its windows, panes and running programs. Attached clients will disconnect. It cannot be undone.",
    okLabel: "close session",
    danger: true,
  }))) return;
  await postSessionClose(`close ${label}`, terminalSessionCloseAPI(service, check, multiplexer, session, user, identity), generation,
    terminalSessionActionKey(service, check, identity), sessionActionClosing, service);
}

async function closeEmptySessionSource(service, check) {
  const generation = dashboardGeneration;
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
  await postSessionClose(`close empty ${label}`, emptyTerminalSessionCloseAPI(service, check), generation,
    emptySourceActionKey(service, check), sessionActionClosing, service);
}

// killDBQuery confirms against the statement the operator is looking at: the
// row is re-read from the latest inventory, and its identity travels with the
// request so the daemon refuses a statement that has since changed.
async function killDBQuery(service, watch, id, identity) {
  const generation = dashboardGeneration;
  const queryID = Number(id);
  const query = (latestSessionInventory.database || []).find((candidate) =>
    candidate.service === service && candidate.watch === watch && candidate.identity === identity);
  if (!service || !watch || !identity || !Number.isSafeInteger(queryID) || queryID <= 0 || !query || !query.can_kill) {
    setStatus("kill statement: statement changed; refresh and try again", feedbackStatusErr);
    return;
  }
  const engine = query.engine || "database";
  const who = `${query.user || "unknown user"}${query.database ? ` on ${query.database}` : ""}`;
  const choice = { label: "Kill the whole connection instead of only this statement", okLabel: "kill connection" };
  if (!(await promptConfirm({
    title: `Cancel ${engine} statement ${queryID}?`,
    message: `${who}, running for ${fmtDuration(query.elapsed_seconds || 0)}. Cancelling stops only this statement; `
      + "killing the connection also ends the client's session and rolls back its open transaction. Neither can be undone.",
    detail: dbQueryText(query),
    okLabel: "cancel query",
    choice,
    danger: true,
  }))) return;
  const mode = choice.checked ? dbQueryKillModeConnection : dbQueryKillModeQuery;
  const statusLabel = `${mode === dbQueryKillModeConnection ? "kill connection" : "cancel query"} ${engine} ${queryID}`;
  await postSessionClose(statusLabel, dbQueryKillAPI(service, watch, queryID, identity, mode), generation,
    dbQueryActionKey(service, watch, identity), sessionActionKilling, service);
}

// killWatchProcess confirms against the row the operator is looking at: it is
// re-read from the latest watch list, and its pid plus start ticks travel with
// the request so the daemon refuses a process that has since been recycled.
async function killWatchProcess(watch, pid, startTicks) {
  const generation = dashboardGeneration;
  const processPID = Number(pid);
  const ticks = String(startTicks || "");
  const w = (allWatches || []).find((item) => item && item.name === watch);
  const p = w && (w.processes || []).find((candidate) =>
    Number(candidate.pid) === processPID && String(candidate.start_ticks || "") === ticks);
  if (!watch || !Number.isSafeInteger(processPID) || processPID <= 0 || !/^[1-9]\d*$/.test(ticks) || !p || !p.can_kill) {
    setStatus("kill process: process changed; refresh and try again", feedbackStatusErr);
    return;
  }
  const choice = { label: "Escalate to SIGKILL if it survives the TERM grace", okLabel: "kill" };
  if (!(await promptConfirm({
    title: `Kill process ${processPID}?`,
    message: `${p.user || "unknown user"} ${p.exe || p.exe_previous || "unknown executable"}, listed as ${p.reason || "unowned"}. `
      + "Sermo sends SIGTERM through the watch's verified identity; this cannot be undone.",
    okLabel: "kill",
    choice,
    danger: true,
  }))) return;
  await postPendingRowAction(`kill process ${processPID}`, watchProcessKillAPI(watch, processPID, ticks, choice.checked), generation,
    watchProcessActionKey(watch, processPID, ticks), sessionActionKilling, () => rerenderWatchExpansion(watch), watchExpansionKey(watch));
}

async function actWatch(name, action) {
  const generation = dashboardGeneration;
  const alarmRevision = (allWatches || []).find((w) => w.name === name)?.alarm?.revision || "";
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
    const suffix = apiActionSuffix(action) + (action === actionSilence ? `?revision=${encodeURIComponent(alarmRevision)}` : "");
    const res = await fetch(watchAPI(name, suffix), targetPostOptions(headers, generation));
    const { body, failed, status } = await actionResult(res);
    if (action === actionProbe) {
      applyWatchProbeResult(name, body, failed);
      // A probe that found an advisory reads amber, exactly like its row.
      const probeStatus = !failed ? feedbackStatusOK : (isAdvisorySeverity(body.severity) ? feedbackStatusWarn : feedbackStatusErr);
      setStatus(`${action} watch ${name}: ${body.message || (failed ? "failed" : feedbackStatusOK)}`, probeStatus);
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
  // An advisory result keeps the amber its row gets everywhere else.
  next.state = !failed ? targetStateOK : (isAdvisorySeverity(body.severity) ? targetStateWarning : targetStateFailed);
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
  const generation = dashboardGeneration;
  const done = await runAction({
    confirm: () => promptConfirm({
      title: `Test notifier ${name}?`,
      message: `Send a clearly marked test notification through "${name}"?`,
      okLabel: "Send test",
      danger: false,
    }),
    request: () => fetch(notifierTestAPI(name), targetPostOptions({}, generation)),
    ok: (body) => body.message || `test notification sent to ${name}`,
    fail: `test notifier ${name}`,
    then: null,
  });
  if (done !== null) load();
}

async function fetchMountBlockers(name, generation) {
  const { stale, res } = await fetchPinned(mountBlockersAPI(name), generation);
  if (stale) throw new Error("configuration changed; refresh and try again");
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

async function confirmMountUnmount(name, generation) {
  const info = await fetchMountBlockers(name, generation);
  if (info.can_umount === false) {
    setStatus(`umount ${name}: ${info.umount_disabled_reason || info.message || "unmount is disabled"}`, feedbackStatusWarn);
    return null;
  }
  return promptMountUnmount(name, info);
}

async function confirmMountAlert(name, generation) {
  const info = await fetchMountBlockers(name, generation);
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
  const generation = dashboardGeneration;
  if (!name) return;
  let postAction = action;
  let query = "";
  const tracked = action === actionMount || action === actionUmount;
  try {
    if (action === actionUmount) {
      const opts = await confirmMountUnmount(name, generation);
      if (!opts) return;
      const params = new URLSearchParams();
      if (opts.force) params.set(apiQueryForce, queryBoolOne);
      if (opts.lazy) params.set(apiQueryLazy, queryBoolOne);
      if (opts.kill) params.set(apiQueryKill, queryBoolOne);
      const encoded = params.toString();
      query = encoded ? `?${encoded}` : "";
    }
    if (action === actionAlert && !(await confirmMountAlert(name, generation))) return;
  } catch (e) {
    setStatus(`${action} ${name}: ${e.message}`, feedbackStatusErr);
    return;
  }

  setStatus("");
  if (tracked) startMountOperation(name, action);
  try {
    const res = await fetch(mountAPI(name, apiActionSuffix(postAction, query)), targetPostOptions({}, generation));
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
let promptConfirmChoice = null;
// promptConfirmChoiceChange relabels the OK button for the open dialog's
// choice; the checkbox's one listener calls it (no inline handlers: CSP).
let promptConfirmChoiceChange = null;

// promptConfirm is the shared yes/no dialog for destructive or irreversible
// actions. Native <dialog> handles focus and Esc; callers await the boolean.
// promptConfirm asks one question. opts.detail adds a preformatted block (a
// statement, a command line). opts.choice = { label, okLabel } adds one
// checkbox that turns the OK button into opts.choice.okLabel while ticked;
// when confirmed, opts.choice.checked holds what the operator chose.
function promptConfirm(opts) {
  const dlg = $("#simple-confirm");
  const title = $("#simple-confirm-title");
  const msg = $("#simple-confirm-message");
  const detail = $("#simple-confirm-detail");
  const okBtn = $("#simple-confirm-ok");
  const o = opts || {};
  if (!dlg || typeof dlg.showModal !== "function") {
    const text = [o.title, o.message, o.detail].filter(Boolean).join("\n\n");
    return Promise.resolve(window.confirm(text || "Continue?"));
  }
  if (title) title.textContent = o.title || "Confirm";
  if (msg) msg.textContent = o.message || "";
  if (detail) {
    detail.textContent = o.detail || "";
    detail.hidden = !o.detail;
  }
  const setOKLabel = (okLabel) => {
    if (!okBtn) return;
    okBtn.textContent = okLabel;
    okBtn.setAttribute("aria-label", okLabel === "confirm" ? "Confirm action" : `Confirm: ${okLabel}`);
  };
  const baseLabel = o.okLabel || "confirm";
  if (okBtn) okBtn.className = o.danger ? "danger-btn" : "";
  setOKLabel(baseLabel);
  const choiceWrap = $("#simple-confirm-choice-wrap");
  const choice = $("#simple-confirm-choice");
  const choiceLabel = $("#simple-confirm-choice-label");
  promptConfirmChoice = o.choice || null;
  if (choiceWrap && choice) {
    choiceWrap.hidden = !o.choice;
    choice.checked = false;
    if (choiceLabel) choiceLabel.textContent = o.choice ? o.choice.label : "";
    promptConfirmChoiceChange = o.choice ? () => setOKLabel(choice.checked ? o.choice.okLabel || baseLabel : baseLabel) : null;
    if (!choice.dataset.choiceBound) {
      choice.addEventListener("change", () => {
        if (promptConfirmChoiceChange) promptConfirmChoiceChange();
      });
      choice.dataset.choiceBound = "1";
    }
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
  const choice = $("#simple-confirm-choice");
  if (promptConfirmChoice) promptConfirmChoice.checked = !!ok && !!choice && choice.checked;
  promptConfirmChoice = null;
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

async function confirmAction(name, action, generation = dashboardGeneration) {
  const dlg = $("#action-confirm");
  if (!dlg || typeof dlg.showModal !== "function") {
    return promptConfirm({
      title: `${action} ${name}?`,
      message: `${action} "${name}"?`,
      okLabel: action,
      danger: isDangerServiceAction(action),
    });
  }
  confirmCtx = { name, action, generation, detail: null, lastEvent: null, preflight: null };
  confirmNoCascade = false;
  $("#confirm-title").textContent = `${action.toUpperCase()} ${name}`;
  $("#confirm-subtitle").textContent = "Review the current service context before sending the operation.";
  litRender(tpl`<span class="muted">loading…</span>`, $("#confirm-body"));
  const actionBtn = $("#confirm-action-btn");
  if (actionBtn) {
    actionBtn.disabled = true;
    actionBtn.textContent = `${action} ${name}`;
    actionBtn.setAttribute("aria-label", `Confirm: ${action} ${name}`);
  }
  syncConfirmPreflightButton(action, { loading: true });
  const cascadeWrap = $("#confirm-no-cascade-wrap");
  const cascadeBox = $("#confirm-no-cascade");
  if (cascadeWrap) cascadeWrap.classList.add("is-hidden");
  if (cascadeBox) cascadeBox.checked = false;

  try {
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
    if (actionBtn) actionBtn.disabled = false;
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
  const failingChecks = (d.checks || []).filter((c) => c.ran && !c.ok && !c.optional && !isAdvisorySeverity(c.severity) && !verdictlessCheck(c));
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
    ? tpl`${fmtTimeTitled(ev.time)} · ${eventBadge(ev)} <span class="muted">${eventDetail(ev)} ${ev.message || ""}</span>`
    : tpl`<span class="muted">none recorded</span>`;
  const preRows = pre ? tpl`<div class="confirm-preflight-block">${preflightRows(pre.checks || [])}</div>` : nothing;
  const warning = ctx.action === actionRepair
    ? "Repair is a manual recovery for a failed or inactive service. It removes only a proven-stale runtime pidfile, clears a failed init state and then uses the normal guarded start path."
    : ctx.action === actionRestart
    ? "A safe restart stops the unit, verifies residual processes, then starts only if the stop phase is clean."
    : ctx.action === actionStart
      ? "Start will run through locks, guards and configured checks before the service is started."
    : ctx.action === actionPause
      ? "Pause freezes the VM or container in place: it stops serving until resumed, and its monitoring is paused until then. Locks and guards still apply."
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
    const res = await fetch(servicePreflightAPI(ctx.name), targetPostOptions({}, ctx.generation));
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
  if (!summary.count) return "";
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
    const action = r.action ? r.action : mutedDash;
    return tpl`<tr>
      <td>${r.name}</td>
      <td class="muted"><span class="rule-field-label" aria-hidden="true">Type</span>${r.type || ""}</td>
      <td><span class="rule-field-label" aria-hidden="true">Action</span>${action}</td>
      <td><span class="rule-field-label" aria-hidden="true">Condition</span>${cond}</td>
      <td class="muted"><span class="rule-field-label" aria-hidden="true">Window</span>${r.window || ""}</td>
      <td><span class="rule-field-label" aria-hidden="true">Progress</span>${r.progress || ""}</td>
      <td><span class="rule-field-label" aria-hidden="true">State</span>${ruleState(r)}</td></tr>`;
  });
  return tpl`<table class="detail-rules-table">
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

function sortedGroupValues(list, groupOf) {
  return [...new Set(list.map(groupOf))].sort((a, b) =>
    a.localeCompare(b, undefined, { numeric: true, sensitivity: "base" }));
}

function closestFrom(event, selector) {
  let target = event.target;
  if (target && target.nodeType !== 1) target = target.parentElement;
  return target && target.closest ? target.closest(selector) : null;
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

// closeMenu hides the settings popover after an action that opens no dialog
// (a dialog closes it by itself).
function closeMenu() {
  const menu = $("#app-menu");
  if (menu && typeof menu.hidePopover === "function" && menu.matches(":popover-open")) menu.hidePopover();
}

async function toggleBrowserNotify() {
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
}

// initStaticHandlers wires the controls the shell declares once. Everything a
// render produces (rows, toolbars, column headers) is handled by delegation in
// initDelegatedHandlers.
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
  for (const id of ["#refresh-now", "#refresh-pending"]) {
    const button = $(id);
    if (button) button.addEventListener(domEventClick, refreshNow);
  }

  const notifyToggle = $("#notify-toggle");
  if (notifyToggle) {
    syncNotifyToggle();
    notifyToggle.addEventListener(domEventClick, toggleBrowserNotify);
  }

  const shortcutToggle = $("#shortcut-toggle");
  if (shortcutToggle) {
    shortcutToggle.checked = keyboardShortcutsEnabled();
    shortcutToggle.addEventListener(domEventChange, () => setKeyboardShortcutsEnabled(shortcutToggle.checked));
  }

  eventFilterFields.forEach(({ id }) => {
    const el = $(id);
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

  bindStopClick($("#reload-btn"), () => { closeMenu(); reloadConfig(); });
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
  // The hand-written sortable tables get their keyboard focus and aria-sort.
  ["sessions", "db-queries", "events"].forEach(updateSortHeaders);
}

function sortHeaderClicked(th) {
  const table = th.closest("[data-sort-scope]");
  if (table) toggleSort(table.dataset.sortScope, th.dataset.sort || "");
}

// windowSetters are the time-window pickers, by the kind their buttons carry.
const windowSetters = { setMetricWin, setSLAWin, setWatchMetricWin, setDaemonMetricWin };

// Delegated click routes: the first selector matching an ancestor of the click
// target wins. The plain-row fallthrough stays outside because rowClick also
// needs the event.
const clickRoutes = [
  ["[data-logout]", () => logout()],
  ["[data-open-view]", (el) => openView(el.dataset.openView || "")],
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
  ["[data-db-query-kill]", (el) => killDBQuery(
    el.dataset.dbQueryService || "", el.dataset.dbQueryWatch || "",
    el.dataset.dbQueryId || "", el.dataset.dbQueryIdentity || "")],
  ["[data-db-query-expand]", (el) => toggleDBQueryExpansion(el.dataset.dbQueryExpand || "")],
  ["[data-watch-process-kill]", (el) => killWatchProcess(
    el.dataset.watchProcessWatch || "", el.dataset.watchProcessPid || "", el.dataset.watchProcessStartTicks || "")],
  ["[data-service-action][data-service]", (el) => act(el.dataset.service || "", el.dataset.serviceAction || "")],
  ["[data-service-button][data-service]", (el) => pressServiceButton(el.dataset.service || "", el.dataset.serviceButton || "")],
  ["[data-watch-action][data-watch]", (el) => actWatch(el.dataset.watch || "", el.dataset.watchAction || "")],
  ["[data-mount-action][data-mount]", (el) => actMount(el.dataset.mount || "", el.dataset.mountAction || "")],
  ["[data-notifier-test]", (el) => testNotifier(el.dataset.notifierTest || "")],
  ["[data-refresh-panel]", (el) => refreshPanel(el)],
  ["[data-service-pin]", (el) => toggleServicePin(el.dataset.servicePin || "")],
  ["[data-service-open]", (el) => openServiceExpansion(el.dataset.serviceOpen || "", true)],
  ["[data-lock-release]", (el) => releaseLock(el.dataset.lockService || "", el.dataset.lockName || "")],
  ["[data-preflight-service]", (el) => runPreflight(el.dataset.preflightService || "")],
  ["[data-window-kind][data-window-value]", (el) => {
    const setWindow = windowSetters[el.dataset.windowKind];
    if (setWindow) setWindow(el.dataset.windowValue || "", el.dataset.windowService || "");
  }],
  ["[data-watch-type-sort-type][data-watch-type-sort]",
    (el) => setWatchTypeSort(el.dataset.watchTypeSortType || "", el.dataset.watchTypeSort || "name")],
  ["th.sortable[data-sort]", sortHeaderClicked],
  // A list panel's toolbar and group headings name their panel through the
  // enclosing data-panel.
  ["[data-panel] [data-filter]", (el) => updatePanel(panelOfElement(el), { status: el.dataset.filter || filterAll })],
  ["[data-panel] [data-panel-group]", (el) => { const p = panelOfElement(el); updatePanel(p, { grouped: !p.grouped }); }],
  ["[data-panel] [data-panel-groups]", (el) => toggleAllPanelGroups(panelOfElement(el))],
  ["[data-panel] [data-group-name]", (el) => togglePanelGroup(panelOfElement(el), el.dataset.groupName || "")],
  ["[data-exp-toggle]", (el) => toggleExpand(el.dataset.expToggle || "")],
];

function initDelegatedHandlers() {
  document.addEventListener(domEventChange, (e) => {
    const typeFilter = closestFrom(e, "[data-watch-type-filter]");
    if (typeFilter) setWatchTypeFilter(typeFilter.dataset.watchTypeFilter || "", typeFilter.value);
    const category = closestFrom(e, "[data-panel] [data-panel-category]");
    if (category) updatePanel(panelOfElement(category), { category: category.value || filterAll });
  });

  document.addEventListener(domEventInput, (e) => {
    const search = closestFrom(e, "[data-panel] [data-panel-search]");
    if (search) updatePanel(panelOfElement(search), { query: search.value.trim().toLowerCase() });
  });

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
    const queryRow = closestFrom(e, "[data-db-query-key]");
    if (queryRow) rowClick(e, queryRow.dataset.dbQueryKey || "", toggleDBQueryExpansion);
  });

  // An outcome mark pulses once: when its animation ends the button drops the
  // class, and lit leaves it dropped, since the bound class string only changes
  // when the pulse window closes.
  document.addEventListener(domEventAnimationEnd, (e) => {
    if (e.animationName === outcomePulseClass && e.target instanceof Element) e.target.classList.remove(outcomePulseClass);
  });

  document.addEventListener(domEventKeydown, (e) => {
    const search = e.key === keyEscape && closestFrom(e, "[data-panel] [data-panel-search]");
    if (search) {
      // Escape clears a panel's search.
      search.value = "";
      updatePanel(panelOfElement(search), { query: "" });
      return;
    }
    if (e.key !== keyEnter && e.key !== keySpace) return;
    // A sortable column header is operated like a button.
    const typeSort = closestFrom(e, "th[data-watch-type-sort-type][data-watch-type-sort]");
    const sortHeader = typeSort ? null : closestFrom(e, "th.sortable[data-sort]");
    if (!typeSort && !sortHeader) return;
    e.preventDefault();
    if (typeSort) setWatchTypeSort(typeSort.dataset.watchTypeSortType || "", typeSort.dataset.watchTypeSort || "name");
    else sortHeaderClicked(sortHeader);
  });
}

initListPanels();
restoreUIState();
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
  updateClocks();
  if (!connOK) { showDisconnected(); return; } // keep the banner's age fresh
  const el = $("#last-refresh");
  if (!el) return;
  const text = lastRefresh ? `fully updated ${fmtSince(Date.now() - lastRefresh)} ago` : "";
  if (el.textContent === text) return;
  el.textContent = text;
}

// Clocks use plain spans or textContent property bindings, so updating their
// text never deletes lit-html's child part markers.
function updateClocks() {
  const now = Date.now();
  const clocks = [
    ["[data-probe-started-at]", (el) => "· " + watchProbeElapsed(el.dataset.probeStartedAt || "")],
    ["[data-clock-since]", (el) => clockSince(el.dataset.clockSince || "", now, Number(el.dataset.clockMaxAge || Infinity))],
    ["[data-clock-until]", (el) => checkClockUntil(el.dataset.clockUntil || "", now)],
  ];
  for (const [selector, text] of clocks) {
    document.querySelectorAll(selector).forEach((el) => {
      const value = text(el);
      if (el.textContent !== value) el.textContent = value;
    });
  }
}

function clockSince(time, now, maxAgeMs = Infinity) {
  if (now - Date.parse(time) > maxAgeMs) return "—";
  return fmtElapsedTime(time, now);
}

// checkClockUntil counts down to the next check; once it is due the daemon is
// running it, and the next refresh brings the following time.
function checkClockUntil(time, now) {
  const at = Date.parse(time);
  if (Number.isNaN(at)) return "";
  const remaining = (at - now) / millisecondsPerSecond;
  return remaining >= 1 ? `in ${fmtCountdown(remaining)}` : "due now";
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

// activeSearchBox returns the search input of the topmost open list panel, or
// of the first one when all are closed. Panels are
// found by their data-panel shell, so a new one joins the "/" shortcut for free.
function activeSearchBox() {
  const sections = [...document.querySelectorAll("[data-panel]")].filter((section) =>
    panelVisible(section) && section.querySelector("[data-panel-search]"));
  const section = sections.find((el) => el.open) || sections[0];
  return section ? { section, box: section.querySelector("[data-panel-search]") } : null;
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


// Remember which panels the operator left open/closed across reloads. Each
// panel restores its saved state on load and saves on every toggle; one with
// no saved state keeps its HTML default. Other disclosures (the events
// panel's "more") follow what they hold rather than a saved state.
(function initCollapse() {
  document.querySelectorAll("details.panel[id]").forEach((el) => {
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
