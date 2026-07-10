const httpMethodPost = "POST";
const csrfHeader = "X-Sermo-CSRF";
const csrfHeaderValue = "1";

export const apiApplicationsPath = "api/applications";
export const apiActivityPath = "api/activity";
const apiDashboardPath = "api/dashboard";
export const apiDaemonPath = "api/daemon";
const apiDaemonMetricsPath = "api/daemon/metrics";
const apiEventsPath = "api/events";
const apiEventsClearPath = "api/events/clear";
export const apiHostPath = "api/host";
export const apiLocksPath = "api/locks";
export const apiMonitoringPath = "api/monitoring";
export const apiOpsPath = "api/ops";
export const apiMountsPath = "api/mounts";
export const apiNotifiersPath = "api/notifiers";
const apiPanicPath = "api/panic";
export const apiReloadPath = "api/reload";
export const apiServicesPath = "api/services";
const apiStateCompactPath = "api/state/compact";
export const apiWatchesPath = "api/watches";
export const apiWhoamiPath = "api/whoami";

const apiQueryCheck = "check";
export const apiQueryBeforeID = "before_id";
export const apiQueryKill = "kill";
export const apiQueryKind = "kind";
export const apiQueryLimit = "limit";
export const apiQueryName = "name";
export const apiQueryNoCascade = "no_cascade";
export const apiQueryOnlyErrors = "only_errors";
export const apiQueryPage = "page";
export const apiQueryService = "service";
export const apiQuerySince = "since";
export const apiQueryStatus = "status";
export const apiQueryWatch = "watch";

const eventRecentLimit = "200";
export const apiEventsRecentPath = `${apiEventsPath}?${apiQueryLimit}=${eventRecentLimit}`;
const apiSuffixBlockers = "/blockers";
const apiSuffixEvents = "/events";
const apiSuffixMetrics = "/metrics";
const apiSuffixPreflight = "/preflight";
const apiSuffixRelease = "/release";
const apiSuffixRuntime = "/runtime";
const apiSuffixSLA = "/sla";
export const readyVerbosePath = "readyz?verbose";
export const liveVerbosePath = "livez?verbose";

export function csrfPostOptions() {
  return { method: httpMethodPost, headers: { [csrfHeader]: csrfHeaderValue } };
}

function apiEntityPath(base, name, suffix = "") {
  return `${base}/${encodeURIComponent(name)}${suffix}`;
}

export function apiActionSuffix(action, query = "") { return `/${action}${query}`; }
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
export function panicAPI(enable) { return `${apiPanicPath}/${enable ? "on" : "off"}`; }
export function serviceAPI(name, suffix = "") { return apiEntityPath(apiServicesPath, name, suffix); }
export function serviceEventsAPI(name, limit) { return serviceAPI(name, apiLimitSuffix(apiSuffixEvents, limit)); }
export function serviceMetricsAPI(name, check, since) {
  return serviceAPI(name, `${apiSuffixMetrics}?${apiQueryCheck}=${encodeURIComponent(check)}&${apiQuerySince}=${since}`);
}
export function servicePreflightAPI(name) { return serviceAPI(name, apiSuffixPreflight); }
export function serviceRuntimeAPI(name, since) { return serviceAPI(name, apiSinceSuffix(apiSuffixRuntime, since)); }
export function serviceSLAAPI(name, since) { return serviceAPI(name, apiSinceSuffix(apiSuffixSLA, since)); }
export function stateCompactAPI(query = "") { return `${apiStateCompactPath}${query}`; }
export function watchAPI(name, suffix = "") { return apiEntityPath(apiWatchesPath, name, suffix); }
