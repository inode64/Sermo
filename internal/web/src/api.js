const httpMethodPost = "POST";
const csrfHeader = "X-Sermo-Csrf";
const csrfHeaderValue = "1";
export const apiHeaderGeneration = "X-Sermo-Generation";
export const apiHeaderConfirm = "X-Sermo-Confirm";

export const apiApplicationsPath = "api/applications";
export const apiActivityPath = "api/activity";
const apiDashboardPath = "api/dashboard";
export const apiDaemonPath = "api/daemon";
const apiDaemonMetricsPath = "api/daemon/metrics";
const apiEventsPath = "api/events";
const apiEventsClearPath = "api/events/clear";
export const apiHostPath = "api/host";
export const apiLibrariesPath = "api/libraries";
export const apiLocksPath = "api/locks";
export const apiMonitoringPath = "api/monitoring";
export const apiMountsPath = "api/mounts";
export const apiNotifiersPath = "api/notifiers";
const apiPanicPath = "api/panic";
export const apiReloadPath = "api/reload";
export const apiServicesPath = "api/services";
export const apiSessionsPath = "api/sessions";
const apiStateCompactPath = "api/state/compact";
export const apiStreamPath = "api/stream";
export const apiWatchesPath = "api/watches";
export const apiWhoamiPath = "api/whoami";

const apiQueryCheck = "check";
const apiQueryMetric = "metric";
export const apiQueryBefore = "before";
export const apiQueryBeforeID = "before_id";
export const apiQueryForce = "force";
export const apiQueryKill = "kill";
export const apiQueryLazy = "lazy";
export const apiQueryKind = "kind";
export const apiQueryLimit = "limit";
export const apiQueryName = "name";
export const apiQueryNoCascade = "no_cascade";
export const apiQueryOnlyErrors = "only_errors";
export const apiQueryService = "service";
export const apiQuerySince = "since";
export const apiQueryStartTicks = "start_ticks";
export const apiQueryManagedByLogind = "managed_by_logind";
export const apiQueryStatus = "status";
export const apiQueryTerminal = "terminal";
export const apiQueryIdentity = "identity";
export const apiQueryMultiplexer = "multiplexer";
export const apiQuerySession = "session";
export const apiQueryUser = "user";
export const apiQueryWatch = "watch";

const eventRecentLimit = "200";
export const apiEventsRecentPath = `${apiEventsPath}?${apiQueryLimit}=${eventRecentLimit}`;
const apiSuffixBlockers = "/blockers";
const apiSuffixEvents = "/events";
const apiSuffixMetrics = "/metrics";
const apiSuffixPreflight = "/preflight";
const apiSuffixRelease = "/release";
const apiSuffixRuntime = "/runtime";
const apiSuffixSessions = "/sessions";
const apiSuffixSLA = "/sla";
const apiSuffixTest = "/test";
const apiSuffixTerminalSessions = "/terminal-sessions";
const apiActionClose = "close";
const apiActionCloseEmpty = "close-empty";
const apiQueryVerbose = "verbose";
export const readyVerbosePath = `readyz?${apiQueryVerbose}`;
export const liveVerbosePath = `livez?${apiQueryVerbose}`;

export function csrfPostOptions(headers = {}) {
  return { method: httpMethodPost, headers: { [csrfHeader]: csrfHeaderValue, ...headers } };
}

function apiEntityPath(base, name, suffix = "") {
  return `${base}/${encodeURIComponent(name)}${suffix}`;
}

function terminalSessionAPI(service, check, suffix) {
  return serviceAPI(service, `${apiSuffixTerminalSessions}/${encodeURIComponent(check)}${suffix}`);
}

export function apiActionSuffix(action, query = "") { return `/${action}${query}`; }
export function serviceButtonAPI(name, button) { return serviceAPI(name, `/button/${encodeURIComponent(button)}`); }
function apiLimitSuffix(base, limit) { return `${base}?${apiQueryLimit}=${limit}`; }
function apiSinceSuffix(base, since) { return `${base}?${apiQuerySince}=${since}`; }

export function applicationEventsAPI(name, limit) {
  return apiEntityPath(apiApplicationsPath, name, apiLimitSuffix(apiSuffixEvents, limit));
}
export function dashboardAPI(since) { return `${apiDashboardPath}?${apiQuerySince}=${since}`; }
export function daemonMetricsAPI(since) { return `${apiDaemonMetricsPath}?${apiQuerySince}=${since}`; }
export function eventsAPI(params) { return `${apiEventsPath}?${params.toString()}`; }
export function eventsClearAPI(query = "") { return `${apiEventsClearPath}${query}`; }
export function lockReleaseAPI(service, query = "") {
  return apiEntityPath(apiLocksPath, service, `${apiSuffixRelease}${query}`);
}
export function mountAPI(name, suffix = "") { return apiEntityPath(apiMountsPath, name, suffix); }
export function mountBlockersAPI(name) { return mountAPI(name, apiSuffixBlockers); }
export function notifierTestAPI(name) { return apiEntityPath(apiNotifiersPath, name, apiSuffixTest); }
export function panicAPI(enable) { return `${apiPanicPath}/${enable ? "on" : "off"}`; }
export function serviceAPI(name, suffix = "") { return apiEntityPath(apiServicesPath, name, suffix); }
export function serviceEventsAPI(name, limit) { return serviceAPI(name, apiLimitSuffix(apiSuffixEvents, limit)); }
export function serviceMetricsAPI(name, check, since, metric = "") {
  const metricQuery = metric ? `&metric=${encodeURIComponent(metric)}` : "";
  return serviceAPI(name, `${apiSuffixMetrics}?${apiQueryCheck}=${encodeURIComponent(check)}&${apiQuerySince}=${since}${metricQuery}`);
}
export function servicePreflightAPI(name) { return serviceAPI(name, apiSuffixPreflight); }
export function serviceRuntimeAPI(name, since) { return serviceAPI(name, apiSinceSuffix(apiSuffixRuntime, since)); }
export function serviceSLAAPI(name, since, check = "", metric = "") {
  const checkQuery = check ? `&${apiQueryCheck}=${encodeURIComponent(check)}` : "";
  const metricQuery = metric ? `&${apiQueryMetric}=${encodeURIComponent(metric)}` : "";
  return serviceAPI(name, `${apiSinceSuffix(apiSuffixSLA, since)}${checkQuery}${metricQuery}`);
}
export function sshSessionCloseAPI(name, pid, startTicks, terminal, managedByLogind = false) {
  const query = new URLSearchParams({ [apiQueryStartTicks]: String(startTicks), [apiQueryTerminal]: terminal });
  if (managedByLogind) query.set(apiQueryManagedByLogind, "true");
  return serviceAPI(name, `${apiSuffixSessions}/${encodeURIComponent(pid)}/close?${query.toString()}`);
}
export function terminalSessionCloseAPI(service, check, multiplexer, session, user, identity) {
  const query = new URLSearchParams({
    [apiQueryMultiplexer]: multiplexer, [apiQuerySession]: session,
    [apiQueryUser]: user, [apiQueryIdentity]: identity,
  });
  return terminalSessionAPI(service, check, `/${apiActionClose}?${query.toString()}`);
}
export function emptyTerminalSessionCloseAPI(service, check) {
  return terminalSessionAPI(service, check, `/${apiActionCloseEmpty}`);
}
export function stateCompactAPI(query = "") { return `${apiStateCompactPath}${query}`; }
export function watchAPI(name, suffix = "") { return apiEntityPath(apiWatchesPath, name, suffix); }
export function watchSLAAPI(name, since, metric = "") {
  const metricQuery = metric ? `&${apiQueryMetric}=${encodeURIComponent(metric)}` : "";
  return watchAPI(name, `${apiSinceSuffix(apiSuffixSLA, since)}${metricQuery}`);
}
// A watch has exactly one check, so its metric series is named by ?metric= alone.
export function watchMetricsAPI(name, metric, since) {
  return watchAPI(name, `${apiSinceSuffix(apiSuffixMetrics, since)}&${apiQueryMetric}=${encodeURIComponent(metric)}`);
}
