import { html as tpl, render as litRender, nothing } from "./vendor/lit-html.js";

const $ = (s) => document.querySelector(s);

function setStatus(msg, kind) {
  const el = $("#err");
  if (!el) return;
  const text = msg || "";
  const statusCls = text ? (kind === "ok" ? "status-ok" : kind === "warn" ? "status-warn" : "status-err") : "";
  const prevCls = el.classList.contains("status-ok") ? "status-ok"
    : el.classList.contains("status-warn") ? "status-warn"
    : el.classList.contains("status-err") ? "status-err" : "";
  if (el.textContent === text && prevCls === statusCls) return;
  el.textContent = text;
  el.classList.remove("status-err", "status-ok", "status-warn");
  if (statusCls) el.classList.add(statusCls);
}

let me = { can_act: true, role: "admin", auth: false };

async function loadMe() {
  try {
    const res = await fetch("api/whoami");
    if (res.ok) me = await res.json();
  } catch (e) { /* keep defaults */ }
  if (!me.auth) { $("#me").innerHTML = ""; }
  else if (me.role === "admin") { $("#me").textContent = "(admin)"; }
  else { $("#me").innerHTML = 'read-only &middot; <a href="login">log in</a>'; }
  // Show admin-only controls (reload config, clear event log).
  const reloadBtn = $("#reload-btn");
  if (reloadBtn) reloadBtn.classList.toggle("admin-hidden", !me.can_act);
  updateEventAdminControls();
  updateActivityAdminControls();
  updateStateCompactControls();
  updatePanicControls();
}

// Connection state: when a fetch fails the table is dimmed and a "disconnected,
// retrying" banner (with the age of the last good update) replaces the refresh
// status, instead of silently showing stale data.
let connOK = true;
let lastLoadOk = Date.now();
let loadSeq = 0;
function showDisconnected() {
  document.body.classList.add("disconnected");
  const age = lastLoadOk ? ` (last update ${fmtSince(Date.now() - lastLoadOk)} ago)` : "";
  setStatus("Disconnected — retrying…" + age, "warn");
}

// load refreshes the dashboard in two phases: first the lightweight status and
// service panels, then the panels that can be cold and probe-heavy. This keeps a
// first visit after daemon start from waiting on /api/watches before showing the
// operator the main service/status view. Each endpoint still degrades
// independently to "keep the last render" on a transient error.
async function load() {
  const seq = ++loadSeq;
  healthIconReady = false;
  const sameLoad = () => seq === loadSeq;
  const [services, mounts, notifiers, daemon, daemonMetrics, locks, activity, ready, live, mon, ops, hostMetrics] = await Promise.all([
    fetch("api/services").then((r) => { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); }).catch(() => null),
    getJSON("api/mounts", null),        // configured mount units
    getJSON("api/notifiers", null),     // what watches can send to
    getJSON("api/daemon", null),        // daemon / engine settings panel
    getJSON(`api/daemon/metrics?since=${daemonMetricWindow}`, null),
    getJSON("api/locks", null),         // global runtime locks (active and stale)
    getJSON("api/activity", null),      // quick activity summary
    fetchReadyReport(),
    getJSON("livez?verbose", {}),
    getJSON("api/monitoring", {}),
    getJSON("api/ops", {}),
    getJSON("api/host", []),
  ]);
  if (!sameLoad()) return;
  if (services) {
    render(services);
    connOK = true;
    lastLoadOk = Date.now();
    document.body.classList.remove("disconnected");
    setStatus("");
  } else {
    connOK = false;
    showDisconnected();
  }
  if (mounts) renderMounts(mounts);
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
    ops,
    locks: locks || latestLocks,
    daemon: daemon || {},
    hostMetrics: hostMetrics || [],
  });
  applyHash();
  if (connOK) {
    renderOpsPanel(ops);
    loadEvents();
    healthIconReady = true;
    renderAttention();
  } else {
    healthIconReady = true;
    setFavicon("warning");
  }

  lastRefresh = Date.now();
  tickRefreshAge();

  getJSON("api/watches", null).then((watches) => {
    if (!sameLoad() || !watches) return;
    renderWatches(watches);
    if (connOK) renderAttention();
  });

  getJSON("api/applications", null).then((apps) => {
    if (!sameLoad() || !apps) return;
    renderApps(apps);
    if (connOK) renderAttention();
  });
}

async function reloadConfig() {
  setStatus("");
  const btn = $("#reload-btn");
  if (btn) btn.disabled = true;
  try {
    const res = await fetch("api/reload", {
      method: "POST",
      headers: { "X-Sermo-CSRF": "1" },
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok || body.ok === false) {
      throw new Error(body.message || ("HTTP " + res.status));
    }
    setStatus("config reload requested", "ok");
    // next auto-refresh (or manual load) will pick up any service changes
    setTimeout(load, 800);
  } catch (e) {
    setStatus("reload failed: " + e.message, "err");
  } finally {
    if (btn) btn.disabled = false;
  }
}

// renderOpsPanel updates the services-panel slot summary from data load() already
// fetched; updateLiveOps still polls /api/ops while browser-local ops are active.
function renderOpsPanel(o) {
  if (!o) return;
  liveOpsSlots = o;
  if (liveOps.size) renderOperationLive();
  const el = $("#ops");
  if (!el) return;
  if (!o.total) {
    el.textContent = "";
    return;
  }
  const saturated = o.in_use >= o.total;
  const cls = saturated ? "failed" : "";
  el.innerHTML = `Operation slots: <span class="${cls}">${o.in_use}/${o.total}</span> in use`;
}

const EVENT_FILTER_DEBOUNCE_MS = 300;
let eventFilterTimer = null;

async function loadEvents() {
  try {
    const params = new URLSearchParams({ limit: "500" });
    const add = (id, key) => {
      const el = $("#" + id);
      const v = el ? el.value.trim() : "";
      if (v) params.set(key, v);
    };
    add("event-service", "service");
    add("event-watch", "watch");
    add("event-kind", "kind");
    add("event-status", "status");
    if ($("#event-errors") && $("#event-errors").checked) params.set("only_errors", "1");
    const res = await fetch("api/events?" + params.toString());
    if (!res.ok) return;
    allEvents = await res.json();
    renderGlobalEvents();
  } catch (e) { /* keep the last feed on a transient error */ }
}

function scheduleLoadEvents() {
  if (eventFilterTimer) clearTimeout(eventFilterTimer);
  eventFilterTimer = setTimeout(() => {
    eventFilterTimer = null;
    loadEvents();
  }, EVENT_FILTER_DEBOUNCE_MS);
}

function flushLoadEvents() {
  if (eventFilterTimer) {
    clearTimeout(eventFilterTimer);
    eventFilterTimer = null;
  }
  saveUIState();
  loadEvents();
}

function eventFilterKey(e) {
  if (e.key === "Enter") flushLoadEvents();
  if (e.key === "Escape") clearEventFilters();
}

function clearEventFilters() {
  ["event-service", "event-watch", "event-kind", "event-status"].forEach((id) => {
    const el = $("#" + id);
    if (el) el.value = "";
  });
  const err = $("#event-errors");
  if (err) err.checked = false;
  saveUIState();
  loadEvents();
}

function updateEventAdminControls() {
  const show = !!me.can_act;
  const btn = $("#event-clear");
  const before = $("#event-before");
  if (btn) btn.classList.toggle("admin-hidden", !show);
  if (before) before.classList.toggle("admin-hidden", !show);
}

function updateActivityAdminControls() {
  const btn = $("#activity-clear");
  if (btn) btn.classList.toggle("admin-hidden", !me.can_act);
}

function updateStateCompactControls() {
  const show = !!me.can_act;
  const btn = $("#state-compact-btn");
  const before = $("#state-before");
  if (btn) btn.classList.toggle("admin-hidden", !show);
  if (before) before.classList.toggle("admin-hidden", !show);
}

// ---- Panic mode ----
let panicOn = false;
let panicResolve = null;

// updatePanicControls shows the footer button only to operators who can act.
function updatePanicControls() {
  const btn = $("#panic-btn");
  if (btn) btn.classList.toggle("admin-hidden", !me.can_act);
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
    const res = await fetch(`api/panic/${enable ? "on" : "off"}`, {
      method: "POST",
      headers: { "X-Sermo-CSRF": "1" },
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok || body.ok === false) throw new Error(body.message || ("HTTP " + res.status));
    updatePanicView(enable);
    setStatus(body.message || (enable ? "panic mode enabled" : "panic mode disabled"), enable ? "err" : "ok");
    await load();
  } catch (e) {
    setStatus(`panic mode: ${e.message}`, "err");
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
    const q = before ? `?before=${encodeURIComponent(before)}` : "";
    const res = await fetch(`api/events/clear${q}`, {
      method: "POST",
      headers: { "X-Sermo-CSRF": "1" },
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok || body.ok === false) throw new Error(body.message || ("HTTP " + res.status));
    const n = Number(body.pruned) || 0;
    setStatus(n ? `cleared ${n} event${n === 1 ? "" : "s"}` : "no events to clear", "ok");
    await load();
  } catch (e) {
    setStatus(`events clear: ${e.message}`, "err");
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
    const q = before ? `?before=${encodeURIComponent(before)}` : "";
    const res = await fetch(`api/state/compact${q}`, {
      method: "POST",
      headers: { "X-Sermo-CSRF": "1" },
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok || body.ok === false) throw new Error(body.message || ("HTTP " + res.status));
    const n = Number(body.pruned) || 0;
    setStatus(n ? `compacted state: pruned ${n} row${n === 1 ? "" : "s"}` : (body.message || "state compact completed"), "ok");
    await load();
  } catch (e) {
    setStatus(`state compact: ${e.message}`, "err");
  } finally {
    if (btn) btn.disabled = false;
  }
}

function eventKey(prefix, e, i) {
  return `${prefix}:${i}:${e.time || ""}:${e.service || ""}:${e.watch || ""}:${e.kind || ""}:${e.action || ""}:${e.status || ""}`;
}

function toggleEventMsg(key) {
  if (eventExpanded.has(key)) eventExpanded.delete(key);
  else eventExpanded.add(key);
  renderGlobalEvents();
  expanded.forEach((expKey) => {
    if (expKey.startsWith("svc:")) loadServiceEvents(expKey.slice(4));
    else if (expKey.startsWith("app:")) loadAppEvents(expKey.slice(4));
  });
}

function eventMessageHTML(e, key) {
  const msg = e.message || "";
  const msgId = key + "-msg";
  const msgOpen = eventExpanded.has(key);
  const truncated = msg.length > 160 && !msgOpen;
  const text = truncated
    ? tpl`<span id="${msgId}" class="event-msg">${msg.slice(0, 160)}<span class="muted">…</span> <button type="button" data-event-toggle="${key}" aria-expanded="false" aria-controls="${msgId}" aria-label="Show full event message">more</button></span>`
    : tpl`<span id="${msgId}" class="event-msg">${msg}${msg.length > 160 ? tpl` <button type="button" data-event-toggle="${key}" aria-expanded="true" aria-controls="${msgId}" aria-label="Show less of event message">less</button>` : nothing}</span>`;
  // Bounded stdout/stderr of the failing command, collapsed behind an "output"
  // toggle so the multi-line blob does not clutter the row by default.
  const out = e.output || "";
  if (!out) return text;
  const okey = key + ":out";
  const outId = okey + "-panel";
  const outOpen = eventExpanded.has(okey);
  return outOpen
    ? tpl`${text} <button type="button" data-event-toggle="${okey}" aria-expanded="true" aria-controls="${outId}" aria-label="Hide command output">hide output</button><pre id="${outId}" class="event-output">${out}</pre>`
    : tpl`${text} <button type="button" data-event-toggle="${okey}" aria-expanded="false" aria-controls="${outId}" aria-label="Show command output">output</button>`;
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

function renderGlobalEvents() {
  const tbody = $("#events");
  if (!tbody) return;
  updateEvSortIndicators();
  const events = sortedEvents(allEvents || []);
  const cnt = $("#event-count");
  if (cnt) cnt.textContent = events.length ? `${events.length} shown` : "";
  updateSectionNav();
  const grouped = $("#event-group") && $("#event-group").checked;
  if (!grouped) {
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
    const groupKey = `grp:${gi}:${eventGroupKey(head)}`;
    const panelId = `event-grp-panel-${gi}`;
    const open = eventExpanded.has(groupKey);
    return [
      tpl`<tr class="event-group">
        <td colspan="4"><button type="button" class="row-toggle" data-event-toggle="${groupKey}" aria-expanded="${open ? "true" : "false"}" aria-controls="${panelId}"><span class="exp" aria-hidden="true">${open ? "▾" : "▸"}</span>${who} <span class="muted">${action} · ${g.length} event${g.length === 1 ? "" : "s"}${statuses ? " · " + statuses : ""}</span></button></td>
      </tr>`,
      open ? eventRows(g, true, { prefix: "group" + gi, panelId }) : nothing,
    ];
  }), tbody);
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
      <td class="t">${fmtTime(e.time)}</td>
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
    case "eligible": return "ok";
    case "cooldown":
    case "rate limit":
    case "blocked": return "inactive";
    case "disabled":
    case "paused":
    case "pending": return "muted";
    default: return "muted";
  }
}

function policyCell(s) {
  // remediation_state is always sent (decorateRemediation covers every path).
  const state = s.remediation_state || "unknown";
  // A paused service shows its state in the State column; don't repeat it here.
  if (state === "paused") return tpl`<span class="muted">—</span>`;
  const cd = s.policy_cooldown ? tpl`<div class="muted mono">${s.policy_cooldown}</div>` : nothing;
  return tpl`<div class="policy-cell"><span class="${policyStateClass(state)}">${state}</span>${cd}</div>`;
}

function locksCell(s) {
  const locks = s.active_locks || [];
  if (!locks.length) return tpl`<span class="muted count-badge">0</span>`;
  const label = locks.length === 1 ? locks[0] : locks.join(", ");
  return tpl`<span class="bad count-badge" title="${label}">${locks.length}</span>`;
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
  if (["failed", "error", "preflight_failed", "postflight_failed", "orphan_processes"].includes(st)) return "crit";
  if (st === "blocked") return "warn";
  if (["error", "hook-failed", "notify-failed", "expand-failed", "kill-failed"].includes(k)) return "crit";
  if (["alert", "firing", "suppressed", "panic-suppressed", "notify-suppressed", "expand-skipped"].includes(k)) return "warn";
  if (["action", "cascade", "hook", "notify", "recovered", "expand", "kill"].includes(k)) return "ok";
  if (k === "dry-run") return "info";
  return "muted";
}

function activityDateCell(e) {
  const time = e && e.time;
  if (!time) return tpl`<span class="muted">—</span>`;
  const kind = e.kind || "";
  const detail = [kind, e.action, e.status].filter(Boolean).join(" ");
  const title = [fmtTime(time), detail, e.message || ""].filter(Boolean).join(" · ");
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
  if (state === "eligible") return tpl`<span class="ok">now</span>`;
  if (state === "pending") return tpl`<span class="muted">${state}</span>`; // paused -> "—" (shown in State)
  return tpl`<span class="muted">—</span>`;
}

// Service list state: latest fetched data plus the active search/status filter,
// so typing or switching a filter re-renders from cache without a refetch.
let allServices = [];
let svcQuery = "";
let svcStatus = "all"; // all | disabled | stopped | started | starting | collecting | monitored | failed
let svcCategory = "all";
let svcGrouped = false;
let svcCollapsedGroups = new Set();
let allMounts = [];
let mountQuery = "";
let mountStatus = "all";
let mountCategory = "all";
let mountSort = { key: "", dir: 1 };
let expanded = new Set(); // open expansions, keyed "svc:<name>" / "wat:<name>" / "app:<name>"
let appGrouped = false;
let appCollapsedGroups = new Set();
let metricWindow = "24h";
let daemonMetricWindow = "24h";
let allWatches = [];
const watchPanels = {
  storage: {
    query: "", status: "all", type: "all", sort: { key: "", dir: 1 }, defaultSortByName: true,
    section: "#storage-section", rows: "#storage-rows", count: "#storage-count",
    filterCount: "#storage-filter-count", filters: "#storage-filters", search: "#storage-search", typeSelect: "#storage-type",
    allTypesLabel: "all storage types", empty: "No storage watches.", emptyFiltered: "No storage watches match the filter.",
    // Storage watches all share one check_type, so filter by filesystem type
    // (xfs, ext4, vfat, …) instead — the dropdown lists every distinct filesystem.
    typeOf: (w) => (w.storage && w.storage.filesystem) || "",
    cols: 7,
    rowHTML: storageRowHTML,
  },
  network: {
    query: "", status: "all", type: "all", sort: { key: "", dir: 1 }, defaultSortByName: true,
    section: "#network-section", rows: "#network-rows", count: "#network-count",
    filterCount: "#network-filter-count", filters: "#network-filters", search: "#network-search", typeSelect: "#network-type",
    allTypesLabel: "all network types", empty: "No network watches.", emptyFiltered: "No network watches match the filter.",
    cols: 6,
  },
  cert: {
    query: "", status: "all", type: "all", sort: { key: "", dir: 1 }, defaultSortByName: true,
    section: "#cert-section", rows: "#cert-rows", count: "#cert-count",
    filterCount: "#cert-filter-count", filters: "#cert-filters", search: "#cert-search", typeSelect: "#cert-type",
    allTypesLabel: "all certificate types", empty: "No certificate watches.", emptyFiltered: "No certificate watches match the filter.",
    // Certificate watches all share one check_type, so filter by public-key
    // algorithm (RSA, ECDSA, Ed25519, …). The dropdown only appears once more
    // than two distinct key types are present (typeFilterMin), and the panel has
    // an extra Key type column so cols is 8 for its expansion/empty rows.
    typeOf: (w) => readingRaw(w, "public_key_algorithm"),
    typeFilterMin: 3, cols: 8,
    rowHTML: certRowHTML,
  },
  diskio: {
    query: "", status: "all", type: "all", sort: { key: "", dir: 1 }, defaultSortByName: true,
    section: "#diskio-section", rows: "#diskio-rows", count: "#diskio-count",
    filterCount: "#diskio-filter-count", filters: "#diskio-filters", search: "#diskio-search",
    // No type dropdown: disk I/O watches only ever have the one check type.
    empty: "No disk I/O watches.", emptyFiltered: "No disk I/O watches match the filter.",
    cols: 8,
    rowHTML: diskioRowHTML,
  },
  host: {
    query: "", status: "all", type: "all", sort: { key: "", dir: 1 }, defaultSortByName: false,
    section: "#watches-section", rows: "#watch-rows", count: "#watches-count",
    filterCount: "#watch-count", filters: "#watch-filters", search: "#watch-search", typeSelect: "#watch-type",
    allTypesLabel: "all host types", empty: "No watches.", emptyFiltered: "No watches match the filter.",
    cols: 6,
  },
};

const UI_STATE_KEY = "sermo-ui-state";
const KEYBOARD_SHORTCUTS_KEY = "sermo-keyboard-shortcuts";

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
    if (typeof s.mountQuery === "string") mountQuery = s.mountQuery;
    if (typeof s.mountStatus === "string") mountStatus = normalizeMountStatusFilter(s.mountStatus);
    if (typeof s.mountCategory === "string") mountCategory = s.mountCategory;
    if (s.mountSort && typeof s.mountSort.key === "string") {
      mountSort = { key: s.mountSort.key, dir: s.mountSort.dir === -1 ? -1 : 1 };
    }
    if (typeof s.appQuery === "string") appQuery = s.appQuery;
    if (typeof s.appStatus === "string") appStatus = s.appStatus;
    if (s.appSort && typeof s.appSort.key === "string") {
      appSort = { key: s.appSort.key, dir: s.appSort.dir === -1 ? -1 : 1 };
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
      }
    }
    if (Array.isArray(s.expanded)) {
      expanded = new Set(s.expanded.filter((k) => typeof k === "string"));
    }
    if (typeof s.metricWindow === "string") metricWindow = s.metricWindow;
    if (typeof s.daemonMetricWindow === "string") daemonMetricWindow = s.daemonMetricWindow;
    if (typeof s.appGrouped === "boolean") appGrouped = s.appGrouped;
    if (Array.isArray(s.svcCollapsedGroups)) svcCollapsedGroups = new Set(s.svcCollapsedGroups);
    if (Array.isArray(s.appCollapsedGroups)) appCollapsedGroups = new Set(s.appCollapsedGroups);
    if (s.eventFilters && typeof s.eventFilters === "object") {
      const ef = s.eventFilters;
      const setVal = (id, v) => { const el = $(id); if (el && typeof v === "string") el.value = v; };
      setVal("#event-service", ef.service);
      setVal("#event-watch", ef.watch);
      setVal("#event-kind", ef.kind);
      setVal("#event-status", ef.status);
      const err = $("#event-errors");
      if (err && typeof ef.onlyErrors === "boolean") err.checked = ef.onlyErrors;
      const grp = $("#event-group");
      if (grp && typeof ef.group === "boolean") grp.checked = ef.group;
    }
  } catch (_) {}
}

function saveUIState() {
  try {
    localStorage.setItem(UI_STATE_KEY, JSON.stringify({
      svcQuery, svcStatus, svcCategory, svcGrouped, svcSort,
      mountQuery, mountStatus, mountCategory, mountSort,
      appQuery, appStatus, appSort, appGrouped,
      metricWindow, daemonMetricWindow,
      expanded: [...expanded],
      svcCollapsedGroups: [...svcCollapsedGroups],
      appCollapsedGroups: [...appCollapsedGroups],
      eventFilters: {
        service: ($("#event-service") || {}).value || "",
        watch: ($("#event-watch") || {}).value || "",
        kind: ($("#event-kind") || {}).value || "",
        status: ($("#event-status") || {}).value || "",
        onlyErrors: !!($("#event-errors") && $("#event-errors").checked),
        group: !($("#event-group") && !$("#event-group").checked),
      },
      watchPanels: Object.fromEntries(Object.entries(watchPanels).map(([k, p]) => [k, {
        query: p.query, status: p.status, type: p.type, sort: p.sort,
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
  const mountSearch = $("#mount-search");
  if (mountSearch) mountSearch.value = mountQuery;
  syncFilterButtons("#mount-filters", "mf", mountStatus);
  const mountCategorySelect = $("#mount-category");
  if (mountCategorySelect) mountCategorySelect.value = mountCategory;
  const appSearch = $("#app-search");
  if (appSearch) appSearch.value = appQuery;
  syncFilterButtons("#app-filters", "af", appStatus);
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
let expCache = {};         // last rendered expansion HTML per key (avoids flicker)
let expDetailCache = {};   // last /api/services/{name} JSON per svc expansion key
let svcExpandRefreshTick = 0;
const SVC_EXPAND_FULL_EVERY = 6; // rebuild expansion HTML every N refresh cycles
let eventExpanded = new Set();
const liveOps = new Map(); // operations started from this browser session, keyed by service
let liveOpsTimer = null;
let liveOpsSlots = null;
let latestLocks = [];
let latestActivity = null;
let latestReady = null;
let latestHostMetrics = [];   // last /api/host readings (for process memory bars)
// Defer favicon/title until load() has the full dashboard snapshot. Avoids a
// green default flashing to red while panels hydrate.
let healthIconReady = false;

function targetStateClass(state) {
  switch (state) {
    case "disabled": return "state-disabled";
    case "running": return "state-running";
    case "started": return "state-started";
    case "paused": return "state-paused";
    case "stopped": return "state-stopped";
    case "warning": return "state-warning";
    case "ok": return "state-ok";
    case "monitored": return "state-monitored";
    case "collecting": return "state-collecting";
    case "failed": return "state-failed";
    case "starting": return "state-starting";
    case "stopping":
    case "restarting":
    case "resuming":
    case "reloading":
      return "state-starting";
    default: return "muted";
  }
}

function stateBadge(state) {
  return stateBadgeLabel(state, state || "unknown");
}

function stateBadgeLabel(state, label) {
  const st = state || "unknown";
  return tpl`<span class="target-state ${targetStateClass(st)}">${label || st}</span>`;
}

function stateRank(state) {
  switch (state) {
    case "disabled": return 0;
    case "stopped": return 1;
    case "starting": return 2;
    case "collecting": return 3;
    case "started": return 4;
    case "monitored": return 5;
    case "failed": return 7;
    case "running": return 2;
    case "paused": return 3;
    case "ok": return 5;
    case "warning": return 6;
    case "stopping": return 1;
    case "restarting": return 1;
    case "resuming": return 1;
    case "reloading": return 1;
    default: return 5;
  }
}

// serviceState reads the server-computed state (app.ServiceState). The UI is
// embedded in the same binary, so the field is always present — deriving it
// again here would just be a second copy of that logic that could drift.
function serviceState(s) {
  return (s && s.state) || "unknown";
}

function operationState(action) {
  switch (action) {
    case "start": return "starting";
    case "stop": return "stopping";
    case "restart": return "restarting";
    case "resume": return "resuming";
    case "reload": return "reloading";
    default: return "working";
  }
}

function serviceDisplayState(s) {
  const op = s && liveOps.get(s.name);
  if (op && !op.finished) return operationState(op.action);
  return serviceState(s);
}

function serviceStateBadge(s) {
  const st = serviceDisplayState(s);
  const missing = (st === "collecting" && s && Array.isArray(s.observability_missing) && s.observability_missing.length)
    ? `Collecting ${s.observability_missing.join(", ")}`
    : "";
  return missing ? tpl`<span title="${missing}">${stateBadge(st)}</span>` : stateBadge(st);
}

function serviceStateCell(s) {
  return serviceStateBadge(s);
}

function isFailing(s) { return serviceState(s) === "failed"; }
function isServiceAttention(s) {
  const st = serviceState(s);
  return st === "failed";
}
function isWatchAttention(w) {
  const st = watchStateText(w);
  return st === "failed";
}
function openPanelTarget(target) {
  if (target === "failed-services") {
    const sec = $("#services-section");
    if (sec) sec.open = true;
    setSvcStatus("failed");
    sec && sec.scrollIntoView({ block: "start", behavior: "smooth" });
    return;
  }
  if (target === "starting-services") {
    const sec = $("#services-section");
    if (sec) sec.open = true;
    setSvcStatus("starting");
    sec && sec.scrollIntoView({ block: "start", behavior: "smooth" });
    return;
  }
  if (target === "collecting-services") {
    const sec = $("#services-section");
    if (sec) sec.open = true;
    setSvcStatus("collecting");
    sec && sec.scrollIntoView({ block: "start", behavior: "smooth" });
    return;
  }
  if (target === "monitored-services") {
    const sec = $("#services-section");
    if (sec) sec.open = true;
    setSvcStatus("monitored");
    sec && sec.scrollIntoView({ block: "start", behavior: "smooth" });
    return;
  }

  if (target === "failed-apps") {
    const sec = $("#apps-section");
    if (sec) sec.open = true;
    setAppStatus("failed");
    sec && sec.scrollIntoView({ block: "start", behavior: "smooth" });
    return;
  }
  if (target === "starting-apps") {
    const sec = $("#apps-section");
    if (sec) sec.open = true;
    setAppStatus("starting");
    sec && sec.scrollIntoView({ block: "start", behavior: "smooth" });
    return;
  }
  if (target === "failed-watches") {
    // Storage and network watches live in their own panels; a firing one could be
    // in any of the three, so open all and scroll to whichever actually holds it.
    const storage = $("#storage-section");
    const network = $("#network-section");
    const sec = $("#watches-section");
    [storage, network, sec].forEach((s) => { if (s) s.open = true; });
    setAllWatchStatuses("failed");
    const firing = (w) => isWatchAttention(w);
    let dest = sec;
    if (storage && panelVisible(storage) && (allWatches || []).some((w) => isStorageWatch(w) && firing(w))) dest = storage;
    else if (network && panelVisible(network) && (allWatches || []).some((w) => isNetworkWatch(w) && firing(w))) dest = network;
    dest && dest.scrollIntoView({ block: "start", behavior: "smooth" });
    return;
  }
  if (target === "starting-watches") {
    const storage = $("#storage-section");
    const network = $("#network-section");
    const sec = $("#watches-section");
    [storage, network, sec].forEach((s) => { if (s) s.open = true; });
    setAllWatchStatuses("starting");
    sec && sec.scrollIntoView({ block: "start", behavior: "smooth" });
    return;
  }
  const el = $("#" + target);
  if (!el) return;
  if (el.tagName === "DETAILS") el.open = true;
  el.scrollIntoView({ block: "start", behavior: "smooth" });
}
// themeHealthColor reads the active --ok/--warn/--crit tokens so the favicon and
// brand dot track light/dark scheme instead of hard-coded palette literals.
function themeHealthColor(status) {
  const root = getComputedStyle(document.documentElement);
  if (status === "critical" || status === "crit") return root.getPropertyValue("--crit").trim() || "#cf222e";
  if (status === "warning" || status === "warn") return root.getPropertyValue("--warn").trim() || "#9a6700";
  if (status === "starting") return root.getPropertyValue("--text-2").trim() || "#8b96a5"; // neutral grey while the daemon settles
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
function renderAttention() {
  const box = $("#attention");
  if (!box) return;
  const items = [];
  const failing = (allServices || []).filter(isServiceAttention);
  if (failing.length) {
    items.push({
      level: "critical",
      title: failing.length === 1 ? "1 service needs attention" : `${failing.length} services need attention`,
      detail: failing.slice(0, 4).map((s) => s.name).join(", ") + (failing.length > 4 ? ` and ${failing.length - 4} more` : ""),
      target: "failed-services",
    });
  }
  const failingWatches = (allWatches || []).filter(isWatchAttention);
  if (failingWatches.length) {
    items.push({
      level: "critical",
      title: failingWatches.length === 1 ? "1 watch firing" : `${failingWatches.length} watches firing`,
      detail: failingWatches.slice(0, 4).map((w) => displayName(w) || w.name).join(", ") + (failingWatches.length > 4 ? ` and ${failingWatches.length - 4} more` : ""),
      target: "failed-watches",
    });
  }
  const failingApps = (allApps || []).filter((a) => appStateText(a) === "failed");
  if (failingApps.length) {
    items.push({
      level: "critical",
      title: failingApps.length === 1 ? "1 application failed" : `${failingApps.length} applications failed`,
      detail: failingApps.slice(0, 4).map((a) => displayName(a) || a.name).join(", ") + (failingApps.length > 4 ? ` and ${failingApps.length - 4} more` : ""),
      target: "failed-apps",
    });
  }
  const activeLocks = (latestLocks || []).filter((l) => l.state === "active");
  if (activeLocks.length) {
    items.push({
      level: "critical",
      title: activeLocks.length === 1 ? "1 active lock" : `${activeLocks.length} active locks`,
      detail: activeLocks.slice(0, 4).map((l) => [l.service, l.name].filter(Boolean).join(":")).join(", "),
      target: "locks-section",
    });
  }
  const staleLocks = (latestLocks || []).filter((l) => l.state === "stale");
  if (staleLocks.length) {
    items.push({
      level: "warning",
      title: staleLocks.length === 1 ? "1 stale lock" : `${staleLocks.length} stale locks`,
      detail: staleLocks.slice(0, 4).map((l) => [l.service, l.name].filter(Boolean).join(":")).join(", "),
      target: "locks-section",
    });
  }
  if (liveOpsSlots && liveOpsSlots.total > 0 && liveOpsSlots.in_use >= liveOpsSlots.total) {
    items.push({
      level: "warning",
      title: "Operation slots saturated",
      detail: `${liveOpsSlots.in_use}/${liveOpsSlots.total} slots in use`,
      target: "services-section",
    });
  }
  if (latestReady && latestReady.ready === false && latestReady.status === "shutting_down") {
    items.push({
      level: "warning",
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
      level: "warning",
      title: latestActivity.errors === 1 ? "1 recent error" : `${latestActivity.errors} recent errors`,
      detail: latestActivity.last_event_kind ? `last: ${latestActivity.last_event_kind}` : "see recent activity",
      target: "activity-section",
    });
  }
  // While the daemon is settling (starting), the tab favicon is neutral grey and
  // other health signals are premature, so it overrides the ok/warning/critical
  // colour. Startup progress lives in the status bar (`status: starting`).
  const startingNow = latestReady && latestReady.ready === false && latestReady.status === "starting";
  if (startingNow) {
    setFavicon("starting");
    if (healthIconReady) document.title = "Sermo · starting";
  } else if (!items.length) {
    setFavicon("ok");
    if (healthIconReady) document.title = "Sermo · services";
    box.classList.add("live-hidden");
    setHTMLIfChanged(box, "");
    return;
  } else {
    setFavicon(items.some((it) => it.level === "critical") ? "critical" : "warning");
    if (healthIconReady) document.title = `(${items.length}) Sermo · services`;
  }
  box.classList.remove("live-hidden");
  const html = `
    <div class="attn-head">
      <b>Attention required</b>
      <span class="muted">${items.length} signal${items.length === 1 ? "" : "s"}</span>
    </div>
    <div class="attn-list">${items.map((it) => `
      <button class="attn-item ${esc(it.level)}" data-panel-target="${esc(it.target)}" aria-label="${esc(attnAriaLabel(it))}">
        <div class="attn-title ${it.level === "critical" ? "bad" : "inactive"}">${esc(it.title)}</div>
        ${it.detail ? `<div class="attn-detail">${esc(it.detail)}</div>` : ""}
      </button>
    `).join("")}</div>`;
  setHTMLIfChanged(box, html);
}
function isTrackedOperation(action) { return action === "start" || action === "stop" || action === "restart" || action === "reload" || action === "resume"; }
function serviceBusy(name) {
  const op = liveOps.get(name);
  return !!op && !op.finished;
}
function opElapsed(op) {
  const end = op.finished || Date.now();
  return Math.max(0, Math.floor((end - op.started) / 1000));
}
function opStateText(op) {
  if (!op.finished) return "running";
  return op.ok ? "completed" : "failed";
}
function beginOperation(name, action) {
  liveOps.set(name, {
    name,
    action,
    started: Date.now(),
    finished: 0,
    ok: false,
    message: "waiting for operation slot",
  });
  ensureLiveOpsTimer();
  updateLiveOps();
  renderOperationLive();
  renderServices();
}
function finishOperation(name, ok, message) {
  const op = liveOps.get(name) || { name, action: "operation", started: Date.now() };
  op.finished = Date.now();
  op.ok = !!ok;
  op.message = message || (ok ? "completed" : "failed");
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
  if (!liveOpsTimer) liveOpsTimer = setInterval(updateLiveOps, 1000);
}
function stopLiveOpsTimerIfIdle() {
  if (liveOpsTimer && liveOps.size === 0) {
    clearInterval(liveOpsTimer);
    liveOpsTimer = null;
  }
}
async function updateLiveOps() {
  liveOpsSlots = await getJSON("api/ops", liveOpsSlots || {});
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
  const slotText = liveOpsSlots && liveOpsSlots.total != null
    ? `<div class="muted op-slots-summary">Operation slots: <b class="${(liveOpsSlots.in_use || 0) >= (liveOpsSlots.total || 1) ? 'failed' : ''}">${liveOpsSlots.in_use || 0}/${liveOpsSlots.total || 0}</b> in use</div>`
    : "";
  box.classList.remove("live-hidden");
  const html = slotText + ops.map((op) => {
    const state = opStateText(op);
    const cls = op.finished ? (op.ok ? "ok" : "failed") : "";
    const since = op.finished ? `${opElapsed(op)}s total` : `${opElapsed(op)}s elapsed`;
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

function serviceMatches(s) {
  const category = categoryOf(s, "service");
  if (svcCategory !== "all" && category !== svcCategory) return false;
  if (svcQuery) {
    const monitorText = s && s.monitored ? "monitoring enabled" : "monitoring paused";
    const missing = Array.isArray(s && s.observability_missing) ? s.observability_missing.join(" ") : "";
    const hay = `${displayName(s)} ${s.name || ""} ${s.display_name || ""} ${category} ${s.unit || ""} ${serviceDisplayState(s)} ${monitorText} ${missing}`.toLowerCase();
    if (!hay.includes(svcQuery)) return false;
  }
  switch (svcStatus) {
    case "disabled":
    case "stopped":
    case "started":
    case "starting":
    case "collecting":
    case "monitored":
    case "failed":      return serviceDisplayState(s) === svcStatus;
    default:            return true; // "all"
  }
}

function setSvcQuery(v) { svcQuery = (v || "").trim().toLowerCase(); renderServices(); saveUIState(); }
function setSvcCategory(v) { svcCategory = v || "all"; renderServices(); saveUIState(); }

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
    const key = b.dataset.f || b.dataset.wf || b.dataset.af || b.dataset.mf;
    if (counts[key] !== undefined) b.innerHTML = `${key} <span class="muted">${counts[key]}</span>`;
  });
}

function normalizeServiceStatusFilter(v) {
  switch (v) {
    case "running":
      return "all";
    case "paused":
      return "stopped";
    default:
      return v || "all";
  }
}

function normalizeWatchStatusFilter(v) {
  return v || "all";
}

function normalizeMountStatusFilter(v) {
  switch (v) {
    case "active":
    case "inactive":
      return v;
    default:
      return "all";
  }
}

function syncFilterButtons(selector, datasetKey, activeValue) {
  document.querySelectorAll(`${selector} button`).forEach((b) => {
    const pressed = b.dataset[datasetKey] === activeValue;
    b.classList.toggle("f-active", pressed);
    b.setAttribute("aria-pressed", pressed ? "true" : "false");
  });
}

// Column sort: null key keeps the default failing-first order; clicking a header
// sorts by it (ascending), and clicking the same header again flips direction.
let svcSort = { key: "", dir: 1 };
const svcSortKeys = {
  name: (s) => displayName(s).toLowerCase(),
  category: (s) => categoryOf(s, "service").toLowerCase(),
  state: (s) => stateRank(serviceDisplayState(s)),
  uptime: (s) => numericSortValue(s && s.uptime_seconds),
  cpu: (s) => (s && s.cpu_ready) ? numericSortValue(s.cpu) : 0,
  memory: (s) => numericSortValue(s && s.rss),
  fds: (s) => numericSortValue(s && s.fds),
  io: (s) => numericSortValue(s && s.io_read) + numericSortValue(s && s.io_write),
  last: lastEventTime,
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

function updateSortIndicatorsFor(attr, sort, headerSelector, headerKey) {
  document.querySelectorAll(`.sort-ind[data-${attr}]`).forEach((el) => {
    el.textContent = el.dataset[attr] === sort.key ? (sort.dir > 0 ? " ▲" : " ▼") : "";
  });
  if (!headerSelector || !headerKey) return;
  document.querySelectorAll(headerSelector).forEach((th) => {
    th.setAttribute("aria-sort", sortAriaValue(sort, th.dataset[headerKey] || ""));
  });
}

function updateSortIndicators() {
  updateSortIndicatorsFor("si", svcSort, ".services-table th.sortable[data-sort]", "sort");
}

// renderFilterCounts annotates each status-filter button with how many services
// match it, for at-a-glance triage.
function renderFilterCounts() {
  const s = allServices || [];
  const c = {
    all: s.length,
    disabled: s.filter((x) => serviceDisplayState(x) === "disabled").length,
    stopped: s.filter((x) => serviceDisplayState(x) === "stopped").length,
    started: s.filter((x) => serviceDisplayState(x) === "started").length,
    starting: s.filter((x) => serviceDisplayState(x) === "starting").length,
    collecting: s.filter((x) => serviceDisplayState(x) === "collecting").length,
    monitored: s.filter((x) => serviceDisplayState(x) === "monitored").length,
    failed: s.filter((x) => serviceDisplayState(x) === "failed").length,
  };
  renderFilterButtonCounts("#svc-filters", c);
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
  const categories = sortedCategories(list, "service");
  const allCollapsed = categories.length > 0 && categories.every((category) => svcCollapsedGroups.has(category));
  if (allCollapsed) {
    categories.forEach((category) => svcCollapsedGroups.delete(category));
  } else {
    categories.forEach((category) => svcCollapsedGroups.add(category));
  }
  renderServices();
  saveUIState();
}

function serviceActionDisabled(s, action, busy) {
  const st = (s.status || "unknown").toLowerCase();
  const paused = st === "paused";
  const stopped = st === "inactive" || st === "failed";
  switch (action) {
    case "start":
      return !!(busy || st === "active" || paused);
    case "stop": return !!(busy || stopped);
    case "restart": return !!busy;
    case "resume": return !!(busy || !paused);
    case "reload": return !!(busy || st !== "active" || !s.can_reload);
    case "monitor":
    case "unmonitor": return !!busy;
    default: return false;
  }
}

function serviceActionDisabledReason(s, action, busy) {
  const st = (s.status || "unknown").toLowerCase();
  if (busy) return "operation in progress";
  const paused = st === "paused";
  const stopped = st === "inactive" || st === "failed";
  switch (action) {
    case "start":
      if (paused) return "service is paused";
      if (st === "active") return "service is already running";
      return "";
    case "stop": return stopped ? "service is already stopped" : "";
    case "resume": return !paused ? "service is not paused" : "";
    case "reload":
      if (!s.can_reload) return "service does not support reload";
      return st !== "active" ? "service is not running" : "";
    default: return "";
  }
}

function svcActionHintId(s, action) {
  return `svc-${s.name}-${action}-hint`;
}

function svcActionHint(s, action, busy) {
  const disabled = serviceActionDisabled(s, action, busy);
  const reason = serviceActionDisabledReason(s, action, busy);
  if (!disabled || !reason) return nothing;
  return tpl`<span id="${svcActionHintId(s, action)}" class="visually-hidden">${reason}</span>`;
}

function svcActionDescribedBy(s, action, busy) {
  const disabled = serviceActionDisabled(s, action, busy);
  const reason = serviceActionDisabledReason(s, action, busy);
  return disabled && reason ? svcActionHintId(s, action) : nothing;
}

function servicePowerAction(s) {
  const st = (s.status || "unknown").toLowerCase();
  return st === "active" || st === "paused" ? "stop" : "start";
}

function expandToggleAriaLabel(name, open, subject) {
  return `${open ? "Collapse" : "Expand"} ${subject} for ${name}`;
}

function groupToggleAriaLabel(category, count, collapsed) {
  return `${collapsed ? "Expand" : "Collapse"} ${category} group (${count} items)`;
}

function svcActionAriaLabel(s, action) {
  const name = displayName(s) || s.name || "";
  switch (action) {
    case "start": return `Start service ${name}`;
    case "stop": return `Stop service ${name}`;
    case "restart": return `Restart service ${name}`;
    case "resume": return `Resume service ${name}`;
    case "reload": return `Reload service ${name}`;
    case "monitor": return `Monitor service ${name}`;
    case "unmonitor": return `Unmonitor service ${name}`;
    default: return `${action} service ${name}`;
  }
}

// serviceRowParts builds one service's main and optional expansion <tr> HTML.
// Shared by the full tbody rebuild and the large-fleet in-place patch path.
function serviceRowParts(s) {
  const st = (s.status || "unknown").toLowerCase();
  const state = serviceState(s);
  const category = categoryOf(s, "service");
  const op = liveOps.get(s.name);
  const busy = serviceBusy(s.name);
  const busyText = op
    ? tpl`<div id="svc-${s.name}-busy" class="svc-busy ${op.finished ? (op.ok ? 'ok' : 'bad') : 'inactive'}" role="status" aria-live="polite">${op.action} ${opStateText(op)} · ${opElapsed(op)}s${op.message ? tpl` <span class="muted">${op.message}</span>` : nothing}</div>`
    : nothing;
  let actions;
  if (!s.enabled) {
    actions = tpl`<span class="muted">disabled in config</span>`;
  } else {
    const powerAction = servicePowerAction(s);
    const alsoApply = (s.also_apply || []).length;
    actions = me.can_act ? tpl`
        ${svcActionHint(s, powerAction, busy)}
        <button ?disabled=${serviceActionDisabled(s, powerAction, busy)} data-service="${s.name}" data-service-action="${powerAction}" title="${alsoApply ? `also applies to: ${s.also_apply.join(", ")}` : nothing}" aria-label="${svcActionAriaLabel(s, powerAction)}" aria-describedby="${svcActionDescribedBy(s, powerAction, busy)}">${powerAction}</button>
        ${svcActionHint(s, "restart", busy)}
        <button ?disabled=${serviceActionDisabled(s, "restart", busy)} data-service="${s.name}" data-service-action="restart" aria-label="${svcActionAriaLabel(s, "restart")}" aria-describedby="${svcActionDescribedBy(s, "restart", busy)}">restart</button>
        ${svcActionHint(s, "resume", busy)}
        <button ?disabled=${serviceActionDisabled(s, "resume", busy)} data-service="${s.name}" data-service-action="resume" aria-label="${svcActionAriaLabel(s, "resume")}" aria-describedby="${svcActionDescribedBy(s, "resume", busy)}">resume</button>
        ${svcActionHint(s, "reload", busy)}
        <button ?disabled=${serviceActionDisabled(s, "reload", busy)} data-service="${s.name}" data-service-action="reload" aria-label="${svcActionAriaLabel(s, "reload")}" aria-describedby="${svcActionDescribedBy(s, "reload", busy)}">reload</button>
        ${s.monitored
          ? tpl`${svcActionHint(s, "unmonitor", busy)}<button ?disabled=${serviceActionDisabled(s, "unmonitor", busy)} data-service="${s.name}" data-service-action="unmonitor" aria-label="${svcActionAriaLabel(s, "unmonitor")}" aria-describedby="${svcActionDescribedBy(s, "unmonitor", busy)}">unmonitor</button>`
          : tpl`${svcActionHint(s, "monitor", busy)}<button ?disabled=${serviceActionDisabled(s, "monitor", busy)} data-service="${s.name}" data-service-action="monitor" aria-label="${svcActionAriaLabel(s, "monitor")}" aria-describedby="${svcActionDescribedBy(s, "monitor", busy)}">monitor</button>`}`
      : tpl`<span class="muted">read-only</span>`;
  }
  const label = displayName(s);
  const key = "svc:" + s.name;
  const open = expanded.has(key);
  const chev = tpl`<span class="exp" aria-hidden="true">${open ? '▾' : '▸'}</span>`;
  const name = tpl`<button type="button" class="name row-toggle" data-service-expand="${s.name}" aria-expanded="${open}" aria-controls="${open ? "exp-" + key : nothing}" aria-label="${expandToggleAriaLabel(label, open, "service details")}">${label}</button>`;
  const rowClass = state === "failed" ? "row-failing" : (state === "warning" ? "row-warning" : "");
  const main = tpl`<tr id="svc-row-${s.name}" class="clickable ${rowClass}" data-exp-key="${key}">
    <td><div class="svc-main">${chev}${name}</div>${busyText}</td>
    <td>${categoryBadge(category)}</td>
    <td>${serviceStateCell(s)}</td>
    <td>${serviceUptimeCell(s)}</td>
    <td>${serviceCpuCell(s)}</td>
    <td>${serviceMemCell(s)}</td>
    <td>${serviceFDsCell(s)}</td>
    <td>${serviceIoCell(s)}</td>
    <td>${lastEventCell(s)}</td>
    <td class="actions">${actions}</td>
  </tr>`;
  const exp = open
    ? tpl`<tr class="exp-row" id="exp-${key}" data-exp="${key}"><td colspan="10"></td></tr>`
    : null;
  return { main, exp };
}

function serviceRowHTML(s) {
  const parts = serviceRowParts(s);
  return parts.exp ? [parts.main, parts.exp] : [parts.main];
}

function finishSvcRender() {
  renderAttention();
  refreshExpandedServices();
}

// render receives fresh data on each refresh; cache it, then render through the
// active filter. Calls with no argument re-render the cache (filter changes).
function render(services) {
  if (services) allServices = services;
  renderServices();
  applyHash();
}

function renderServices() {
  const total = (allServices || []).length;
  const headCount = $("#services-count");
  if (headCount) headCount.textContent = total ? `(${total})` : "";
  svcCategory = syncCategorySelect("#svc-category", allServices || [], "service", svcCategory);
  renderFilterCounts();
  const list = (allServices || []).filter(serviceMatches);
  if (svcSort.key && svcSortKeys[svcSort.key]) {
    sortedBy(list, svcSort, svcSortKeys, "name");
  } else {
    // Default: failing services first (stable sort keeps backend order in groups).
    list.sort((a, b) => (isFailing(b) ? 1 : 0) - (isFailing(a) ? 1 : 0));
  }
  updateSortIndicators();
  const visibleCategories = sortedCategories(list, "service");
  svcCollapsedGroups.forEach((category) => { if (!visibleCategories.includes(category)) svcCollapsedGroups.delete(category); });
  updateGroupButtons("svc", svcGrouped, visibleCategories, svcCollapsedGroups, "services");
  const cnt = $("#svc-count");
  if (cnt) cnt.textContent = (svcQuery || svcStatus !== "all" || svcCategory !== "all") ? `showing ${list.length} of ${total}` : "";
  let content;
  if (!list.length) {
    content = (allServices || []).length
      ? tpl`<tr><td colspan="10" class="muted">No services match the filter.</td></tr>`
      : tpl`<tr><td colspan="10" class="muted">No services.</td></tr>`;
  } else {
    content = svcGrouped
      ? renderGroupedRows(list, svcCollapsedGroups, "svc", "service", 10, serviceRowHTML, svcSort.key === "category" ? svcSort.dir : 1)
      : list.flatMap(serviceRowHTML);
  }
  litRender(content, $("#rows"));
  finishSvcRender();
  updateSectionNav();
}

// toggleExpand / loadExpansionFor drive inline expansion, shared by services and
// host watches. Keys are "svc:<name>" (full inline service detail) or
// "wat:<name>" (watch config + recent activity).
function toggleExpand(key) {
  if (expanded.has(key)) {
    expanded.delete(key);
    delete expCache[key];
    delete expDetailCache[key];
    if (location.hash === "#" + key) history.replaceState(null, "", location.pathname + location.search);
  } else {
    expanded.add(key);
    if (key.startsWith("svc:") || key.startsWith("wat:") || key.startsWith("app:")) {
      history.replaceState(null, "", "#" + key); // shareable deep-link
    }
  }
  renderServices();
  renderWatches();
  renderApps();
  saveUIState();
}

function openServiceExpansion(name, scroll) {
  if (!name) return;
  const key = "svc:" + name;
  if (!expanded.has(key)) expanded.add(key);
  history.replaceState(null, "", "#" + key);
  renderServices();
  if (scroll) {
    const el = document.getElementById("svc-row-" + name);
    if (el) el.scrollIntoView({ block: "center" });
  }
}

function toggleServiceExpansion(name) {
  if (!name) return;
  toggleExpand("svc:" + name);
}

function refreshExpandedServiceDetails() {
  refreshExpandedServices({ metricsOnly: true });
}

// refreshExpandedServices reloads open expansions on each dashboard refresh.
// Service expansions skip the full HTML rebuild on most cycles (charts and
// events are refreshed via hydrateServiceDetail only); a full renderServiceDetail
// pass runs every SVC_EXPAND_FULL_EVERY cycles or when the cache is empty.
// Skipped while the tab is hidden unless opts.force is set.
function refreshExpandedServices(opts = {}) {
  if (document.hidden && !opts.force) return;
  if (opts.metricsOnly) {
    expanded.forEach((k) => {
      if (!k.startsWith("svc:")) return;
      const detail = expDetailCache[k];
      if (detail) hydrateServiceDetail(detail);
    });
    return;
  }
  const forceFull = !!opts.forceFull;
  svcExpandRefreshTick++;
  const periodicFull = forceFull || (svcExpandRefreshTick % SVC_EXPAND_FULL_EVERY === 0);
  expanded.forEach((k) => {
    if (k.startsWith("wat:")) {
      loadExpansionFor(k);
      return;
    }
    if (!k.startsWith("svc:")) return;
    if (!expCache[k] || periodicFull) {
      loadExpansionFor(k);
      return;
    }
    refreshServiceExpansionLight(k);
  });
}

async function refreshServiceExpansionLight(key) {
  const name = key.slice(4);
  const tr = [...document.querySelectorAll("tr.exp-row")].find((r) => r.dataset.exp === key);
  if (!tr) return;
  // A structural re-render of #rows can recreate this row and blank its detail
  // cell; re-assert the cached markup (a cheap no-op when already present) so the
  // expansion survives expanding/collapsing other rows.
  if (expCache[key]) litRender(expCache[key], tr.querySelector("td"));
  try {
    const res = await fetch(`api/services/${encodeURIComponent(name)}`);
    if (!res.ok) return;
    const detailData = await res.json();
    expDetailCache[key] = detailData;
    hydrateServiceDetail(detailData);
  } catch (_) { /* keep charts/events on a transient error */ }
}

// applyHash opens/scrolls to the target named in a #svc:|#wat:|#app: URL fragment
// or a section id such as #services-section.
// Runs after each render and on hashchange.
let hashScrolled = false;
function watchSectionFor(w) {
  if (isStorageWatch(w)) return "#storage-section";
  if (isNetworkWatch(w)) return "#network-section";
  return "#watches-section";
}
function applyHash() {
  const h = decodeURIComponent(location.hash.slice(1));
  if (!h) return;
  const section = document.getElementById(h);
  if (section && (section.tagName === "DETAILS" || section.tagName === "SECTION")) {
    if (section.classList.contains("panel-hidden")) return;
    if (section.tagName === "DETAILS") section.open = true;
    if (!hashScrolled) {
      section.scrollIntoView({ block: "start" });
      hashScrolled = true;
    }
    return;
  }
  if (h.startsWith("svc:")) {
    const name = h.slice(4);
    if (!(allServices || []).some((s) => s.name === name)) return;
    if (!expanded.has(h)) { expanded.add(h); renderServices(); }
    if (!hashScrolled) {
      const el = document.getElementById("svc-row-" + name);
      if (el) el.scrollIntoView({ block: "center" });
      hashScrolled = true;
    }
    return;
  }
  if (h.startsWith("wat:")) {
    const name = h.slice(4);
    const w = (allWatches || []).find((item) => item && item.name === name);
    if (!w) return;
    const sec = $(watchSectionFor(w));
    if (sec) { setPanelVisible(sec, true); sec.open = true; }
    if (!expanded.has(h)) { expanded.add(h); renderWatches(); }
    if (!hashScrolled) {
      const el = document.getElementById("wat-row-" + name);
      if (el) el.scrollIntoView({ block: "center" });
      hashScrolled = true;
    }
    return;
  }
  if (h.startsWith("app:")) {
    const name = h.slice(4);
    if (!(allApps || []).some((a) => a.name === name)) return;
    const sec = $("#apps-section");
    if (sec) { setPanelVisible(sec, true); sec.open = true; }
    if (!expanded.has(h)) { expanded.add(h); renderApps(); }
    if (!hashScrolled) {
      const el = document.getElementById("app-row-" + name);
      if (el) el.scrollIntoView({ block: "center" });
      hashScrolled = true;
    }
  }
}
window.addEventListener("hashchange", () => { hashScrolled = false; applyHash(); });

// rowClick expands a row from a click anywhere on it, except on interactive
// elements (action buttons and links) which keep their own behaviour.
function rowClick(event, key) {
  if (closestFrom(event, "button, a, input, select")) return;
  toggleExpand(key);
}

// loadExpansionFor is the sole renderer of an expansion's detail cell: the row
// template leaves the <td> empty (no binding) and we litRender into it here, so
// the outer #rows/watch render and this loader never fight over the same cell.
function expansionCell(key) {
  const tr = [...document.querySelectorAll("tr.exp-row")].find((r) => r.dataset.exp === key);
  return tr ? tr.querySelector("td") : null;
}

async function loadExpansionFor(key) {
  const cell = expansionCell(key);
  if (cell && !expCache[key]) litRender(tpl`<span class="muted">loading…</span>`, cell);
  try {
    let html;
    let detailData = null;
    if (key.startsWith("svc:")) {
      const name = key.slice(4);
      const res = await fetch(`api/services/${encodeURIComponent(name)}`);
      if (!res.ok) return;
      detailData = await res.json();
      expDetailCache[key] = detailData;
      html = renderServiceDetail(detailData);
    } else if (key.startsWith("wat:")) {
      const name = key.slice(4);
      const res = await fetch("api/events?limit=200");
      const events = res.ok ? await res.json() : [];
      html = renderWatchExpansion((allWatches || []).find((x) => x.name === name),
        (events || []).filter((e) => e.watch === name));
    } else {
      return;
    }
    expCache[key] = html;
    const target = expansionCell(key);
    if (target) litRender(html, target);
    if (detailData) hydrateServiceDetail(detailData);
  } catch (_) { /* keep the last content on a transient error */ }
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

// procCpuCells and procIoFdThreadCells render the per-process metric columns
// shared by the expansion and detail process tables, keeping the two identical
// (only the memory column differs: the expansion shows a host-RAM bar).
function procCpuCells(p) {
  if (!p.has_cpu) return tpl`<td>—</td><td>—</td>`;
  const cpu = Number(p.cpu) || 0;
  return tpl`<td>${fmtNum(cpu, 2)}%</td><td>${cpuBarMini(cpu)}</td>`;
}
function procIoFdThreadCells(p) {
  const io = (p.io_read || p.io_write) ? `${fmtBytes(p.io_read || 0)} / ${fmtBytes(p.io_write || 0)}` : '—';
  return tpl`<td>${io}</td><td class="muted">${p.fds || '—'}</td><td class="muted">${p.threads || '—'}</td>`;
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

function procTreeLabel(row) {
  const depth = Number(row.depth || 0);
  const p = row.p || {};
  const branch = depth > 0
    ? tpl`<span class="proc-branch" title="child process of PID ${p.ppid || ""}" aria-label="child process of PID ${p.ppid || ""}"></span>`
    : nothing;
  return tpl`<span class="proc-tree${depth > 0 ? " proc-tree-child" : ""}" style="--proc-depth:${depth}">${branch}${procLabel(p)}</span>`;
}

function serviceUptimeCell(s) {
  const up = s ? fmtUptime(s.uptime_seconds) : "";
  if (!up) return tpl`<span class="muted">—</span>`;
  const title = s.started_at ? `started ${fmtTime(s.started_at)}` : nothing;
  return tpl`<span title="${title}">${up}</span>`;
}

function cpuInline(cpu, ready, numCPU) {
  if (!ready) return numCPU ? tpl`<span class="muted">measuring…</span>` : tpl`<span class="muted">—</span>`;
  const v = Number(cpu) || 0;
  // Same shape as every other CPU bar (cpuBarMini): the percentage lives inside
  // the bar and the precise value in the tooltip — no separate label prefix.
  return usageBarMini(pctClamp(v), fmtPct(v), `${fmtNum(v, 2)}% of ${numCPU || "?"} host CPUs`);
}

function serviceHasNoResidentProcess(s) {
  return !!(s && s.no_resident_process);
}

function serviceCpuCell(s) {
  if (serviceHasNoResidentProcess(s)) return tpl`<span class="muted">—</span>`;
  return cpuInline(s && s.cpu, !!(s && s.cpu_ready), s && s.num_cpu);
}

function memoryInline(rss) {
  rss = Number(rss) || 0;
  if (!rss) return tpl`<span class="muted">—</span>`;
  const hostMem = hostMemTotalBytes();
  if (hostMem > 0) return usageBarMini(pctClamp(rss / hostMem * 100), fmtBytes(rss), `${fmtBytes(rss)} resident memory`);
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

function slaWindowLabel(window) {
  switch (window) {
    case "hour": return "1h";
    case "day": return "1d";
    case "week": return "7d";
    case "month": return "30d";
    case "year": return "1y";
    default: return window || "?";
  }
}

function slaColor(pct) {
  if (pct == null) return "color-mix(in srgb, var(--text-2) 40%, transparent)";
  if (pct >= 99) return themeHealthColor("ok");
  if (pct >= 95) return themeHealthColor("warning");
  return themeHealthColor("critical");
}

function renderSLAWindows(wins, compact) {
  wins = wins || [];
  if (!wins.length) return tpl`<span class="muted">No SLA data yet.</span>`;
  const rows = wins.map((w) => {
    const pct = w.ratio == null ? null : Number(w.ratio) * 100;
    const label = slaWindowLabel(w.window);
    const pctText = pct == null ? "—" : fmtNum(pct, 2) + "%";
    const count = `${Number(w.up || 0)}/${Number(w.total || 0)}`;
    const title = `${label} · ${pctText} · ${count}`;
    const track = Array.isArray(w.segments) && w.segments.length
      ? renderSLATimeline(w.segments, w.window)
      : renderSLAFill(pct);
    return tpl`<div class="sla-window" title="${title}">
      <span class="sla-label">${label}</span>
      ${track}
      <span class="sla-pct">${pctText}</span>
      <span class="sla-count">${count}</span>
    </div>`;
  });
  return tpl`<div class="sla-windows${compact ? " sla-compact" : ""}">${rows}</div>`;
}

// renderSLAFill is the single-fill bar used when a window has no segment data.
function renderSLAFill(pct) {
  const width = pct == null ? 0 : pctClamp(pct);
  const empty = pct == null ? " sla-empty" : "";
  const label = pct == null ? "No SLA data" : `${fmtNum(pct, 2)}% available`;
  return tpl`<span class="sla-bar" aria-label="${label}"><span class="sla-fill${empty}" style="--sla-pct:${width.toFixed(2)}%; --sla-color:${slaColor(pct)}"></span></span>`;
}

function slaTimelineDataRows(segments, window) {
  const n = segments.length;
  if (!n) return nothing;
  const spanMs = slaWindowSpanMs(window);
  const endMs = Date.now();
  const startIdx = Math.max(0, n - chartDataTableMaxRows);
  return segments.slice(startIdx).map((ratio, i) => {
    const idx = startIdx + i;
    const segStart = endMs - spanMs + (idx / n) * spanMs;
    const segEnd = endMs - spanMs + ((idx + 1) / n) * spanMs;
    const when = `${fmtTime(new Date(segStart).toISOString())} – ${fmtTime(new Date(segEnd).toISOString())}`;
    const pctText = ratio == null ? "no data" : fmtNum(Number(ratio) * 100, 2) + "%";
    return tpl`<tr><td>${when}</td><td>${pctText}</td></tr>`;
  });
}

// renderSLATimeline draws a contiguous status-page style availability band: one
// colored cell per sub-span (oldest left), hatched where no data was observed.
function renderSLATimeline(segments, window) {
  const n = segments.length;
  const spanMs = slaWindowSpanMs(window);
  const endMs = Date.now();
  const cells = segments.map((ratio, i) => {
    const pct = ratio == null ? null : Number(ratio) * 100;
    const segStart = endMs - spanMs + (i / n) * spanMs;
    const segEnd = endMs - spanMs + ((i + 1) / n) * spanMs;
    const when = `${fmtTime(new Date(segStart).toISOString())} – ${fmtTime(new Date(segEnd).toISOString())}`;
    if (pct == null) return tpl`<span class="sla-seg sla-gap" title="${when + " · no data"}" aria-label="${when}: no data"></span>`;
    const pctText = fmtNum(pct, 2) + "%";
    return tpl`<span class="sla-seg" style="--sla-color:${slaColor(pct)}" title="${when + " · " + pctText}" aria-label="${when}: ${pctText} available"></span>`;
  });
  const dataRows = slaTimelineDataRows(segments, window);
  return tpl`<table class="chart-data visually-hidden"><caption>SLA timeline data</caption><thead><tr><th scope="col">Period</th><th scope="col">Availability</th></tr></thead><tbody>${dataRows}</tbody></table><span class="sla-timeline" role="img" aria-label="SLA availability timeline">${cells}</span>`;
}

function slaWindowSpanMs(window) {
  switch (window) {
    case "hour": return 36e5;
    case "day": return 864e5;
    case "week": return 6048e5;
    case "month": return 2592e6;
    case "year": return 3.1536e10;
    default: return 864e5;
  }
}

function slaPointPct(p) {
  const total = Number(p && p.total || 0);
  if (total <= 0) return null;
  return pctClamp(Number(p.up || 0) / total * 100);
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
  return new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function slaTimelineSummary(points) {
  let up = 0, total = 0;
  (points || []).forEach((p) => {
    up += Number(p.up || 0);
    total += Number(p.total || 0);
  });
  if (total <= 0) return '<span class="muted">No data in this window.</span>';
  const pct = up / total * 100;
  const incidentCount = (points || []).filter((p) => Number(p.total || 0) > Number(p.up || 0)).length;
  const head = incidentCount
    ? `<span class="bad">${incidentCount} incident${incidentCount === 1 ? "" : "s"}</span>`
    : '<span class="ok">No incidents</span>';
  return `${head} &middot; ${fmtNum(pct, 2)}%`;
}

function renderSLAIncidentList(incidents) {
  if (!incidents.length) return '<div class="sla-incident-list"><span class="ok">No incidents in this window.</span></div>';
  const shown = incidents.slice(-10);
  const hidden = incidents.length - shown.length;
  const chips = shown.map((o) => {
    const tip = `Incident ${fmtTime(new Date(o.t).toISOString())} · ${fmtNum(o.pct, 2)}% · ${Number(o.p.up || 0)}/${Number(o.p.total || 0)}`;
    return `<span class="sla-incident" title="${esc(tip)}">${esc(slaIncidentTime(o.t))}</span>`;
  }).join("");
  const more = hidden > 0 ? `<span class="muted">+${hidden} earlier</span>` : "";
  return `<div class="sla-incident-list"><span class="muted">Incidents</span>${chips}${more}</div>`;
}

async function loadServiceSLA(name) {
  const summary = document.getElementById(detailDomId(name, "sla-summary"));
  const chart = document.getElementById(detailDomId(name, "sla-chart"));
  if (!summary || !chart) return;
  try {
    const res = await fetch(`api/services/${encodeURIComponent(name)}/sla?since=${metricWindow}`);
    if (!res.ok) throw new Error("HTTP " + res.status);
    const body = await res.json();
    const points = body.points || [];
    summary.innerHTML = slaTimelineSummary(points);
    chart.innerHTML = drawSLAChart(points, metricWindow);
  } catch (e) {
    summary.innerHTML = `<span class="bad">Failed to load SLA: ${esc(e.message)}</span>`;
    chart.innerHTML = "";
  }
}

function drawSLAChart(points, win) {
  const W = 640, H = 160, padL = 42, padR = 16, padT = 14, padB = 30, cols = 120;
  const span = windowMs[win || metricWindow] || 864e5;
  const endMs = Date.now();
  const startMs = endMs - span;
  const plotW = W - padL - padR;
  const plotH = H - padT - padB;
  const baseY = padT + plotH;
  const observed = (points || []).map((p) => ({ p, t: slaPointTime(p), pct: slaPointPct(p) }))
    .filter((o) => o.t != null && o.t >= startMs && o.t <= endMs && o.pct != null)
    .sort((a, b) => a.t - b.t);
  if (!observed.length) return '<span class="muted">No SLA data yet for this window.</span>';

  // Zoom the vertical scale to the data. SLA lives near 100%, so a fixed 0–100%
  // axis squashes the line against the top and crowds the 95/100 labels into one
  // another. Pick a "nice" floor just below the worst observed value: healthy
  // data gets a tight 99–100 / 95–100 view, real downtime widens it as needed.
  const lo = Math.min.apply(null, observed.map((o) => o.pct));
  const yMin = lo >= 99.5 ? 99 : lo >= 99 ? 98 : lo >= 95 ? 90 : lo >= 90 ? 80 : lo >= 70 ? 60 : lo >= 40 ? 30 : 0;
  const x = (t) => padL + ((t - startMs) / span) * plotW;
  const y = (pct) => padT + (100 - Math.max(yMin, Math.min(100, pct))) / (100 - yMin) * plotH;

  const breakMs = Math.max(span / cols * 2.5, 6 * 60 * 1000);
  const segments = [];
  let seg = [];
  observed.forEach((o) => {
    if (seg.length && o.t - seg[seg.length - 1].t > breakMs) {
      segments.push(seg);
      seg = [];
    }
    seg.push(o);
  });
  if (seg.length) segments.push(seg);

  // Reference threshold bands (the slaColor breakpoints), drawn only when inside
  // the current range.
  const refs = [99, 95].filter((v) => v > yMin && v < 100).map((v) =>
    `<line x1="${padL}" y1="${y(v).toFixed(1)}" x2="${W - padR}" y2="${y(v).toFixed(1)}" stroke="#8883" stroke-dasharray="3 4"></line>`).join("");
  // Y labels: candidates coarsest→finest, placed greedily top-down and skipped
  // when they would land within 11px of an already-placed one, so they never
  // overlap no matter how tight or wide the zoomed range is.
  const placed = [];
  const yLabels = [100, 99, 95, 90, 75, 50, 25, yMin]
    .filter((v, i, a) => v >= yMin && v <= 100 && a.indexOf(v) === i)
    .sort((a, b) => b - a)
    .map((v) => {
      const yy = y(v);
      if (placed.some((py) => Math.abs(py - yy) < 11)) return "";
      placed.push(yy);
      return `<text x="${padL - 6}" y="${yy.toFixed(1)}" font-size="10" fill="#888" text-anchor="end" dominant-baseline="middle">${v}%</text>`;
    }).join("");
  const axis = `
    <line x1="${padL}" y1="${padT}" x2="${padL}" y2="${baseY}" stroke="#8886"></line>
    <line x1="${padL}" y1="${baseY}" x2="${W - padR}" y2="${baseY}" stroke="#8886"></line>
    ${refs}${yLabels}
    <text x="${padL}" y="${H - 6}" font-size="10" fill="#888">${esc(new Date(startMs).toLocaleString())}</text>
    <text x="${W - padR}" y="${H - 6}" font-size="10" fill="#888" text-anchor="end">now</text>`;
  // Soft area under each segment gives the line body and makes the trend readable
  // at a glance (SVG fills keep the literal palette so they read on both schemes).
  const areas = segments.filter((s) => s.length > 1).map((s) => {
    const top = s.map((o) => `${x(o.t).toFixed(1)},${y(o.pct).toFixed(1)}`).join(" ");
    return `<polygon points="${x(s[0].t).toFixed(1)},${baseY.toFixed(1)} ${top} ${x(s[s.length - 1].t).toFixed(1)},${baseY.toFixed(1)}" fill="#1a7f3718" stroke="none"></polygon>`;
  }).join("");
  const lines = segments.map((s) => {
    if (s.length === 1) {
      const o = s[0];
      return `<circle cx="${x(o.t).toFixed(1)}" cy="${y(o.pct).toFixed(1)}" r="2.6" fill="${slaColor(o.pct)}"><title>${esc(fmtTime(new Date(o.t).toISOString()) + " · " + fmtNum(o.pct, 2) + "%")}</title></circle>`;
    }
    const pts = s.map((o) => `${x(o.t).toFixed(1)},${y(o.pct).toFixed(1)}`).join(" ");
    return `<polyline points="${pts}" fill="none" stroke="#1a7f37" stroke-width="1.8" stroke-linejoin="round" stroke-linecap="round"></polyline>`;
  }).join("");
  const incidents = slaIncidentPoints(points, startMs, endMs);
  const markers = incidents.map((o) => {
    const tx = x(o.t);
    const ty = y(o.pct);
    const tip = `Incident ${fmtTime(new Date(o.t).toISOString())} · ${fmtNum(o.pct, 2)}% · ${Number(o.p.up || 0)}/${Number(o.p.total || 0)}`;
    return `<g>
      <title>${esc(tip)}</title>
      <circle cx="${tx.toFixed(1)}" cy="${ty.toFixed(1)}" r="3.4" fill="#cf222e"></circle>
    </g>`;
  }).join("");
  const hover = observed.map((o) => {
    const tx = x(o.t);
    const tip = `${fmtTime(new Date(o.t).toISOString())} · SLA ${fmtNum(o.pct, 2)}% · ${Number(o.p.up || 0)}/${Number(o.p.total || 0)}`;
    return `<circle cx="${tx.toFixed(1)}" cy="${y(o.pct).toFixed(1)}" r="5" fill="transparent"><title>${esc(tip)}</title></circle>`;
  }).join("");
  const latestPct = observed.length ? observed[observed.length - 1].pct : null;
  const slaAria = latestPct != null
    ? `SLA timeline: latest ${fmtNum(latestPct, 2)}%, ${incidents.length} incident${incidents.length === 1 ? "" : "s"}`
    : "SLA timeline";
  const dataTable = slaChartDataTable(observed);
  return `${dataTable}<svg viewBox="0 0 ${W} ${H}" width="100%" role="img" aria-label="${esc(slaAria)}">${axis}${areas}${lines}${hover}${markers}</svg>${renderSLAIncidentList(incidents)}`;
}

function totalsCpuCell(pt) {
  return cpuInline(pt && pt.cpu, !!(pt && pt.has_cpu), pt && pt.num_cpu);
}

function detailDomKey(name) {
  return Array.from(String(name || "")).map((ch) => ch.charCodeAt(0).toString(16).padStart(4, "0")).join("");
}

function detailDomId(name, suffix) {
  return "svc-detail-" + detailDomKey(name) + "-" + suffix;
}

function serviceMeasuredChecks(d) {
  return (d.checks || []).filter((c) => metricTypes.includes(c.type));
}

function selectedMetricCheck(measured) {
  if (metricCheck && measured.some((c) => c.name === metricCheck)) return metricCheck;
  return measured.length ? measured[0].name : "";
}

function renderServiceDetail(d) {
  const procs = d.processes || [];
  const procWarnings = d.process_warnings || [];
  const noResidentProcess = !!d.no_resident_process;
  const checkRows = (d.checks || []).map((c) => {
    const age = c.at ? tpl` <span class="muted">· ${fmtAge(c.at)}</span>` : nothing;
    const state = !c.ran
      ? (c.at ? tpl`<span class="muted">cached</span>${age}` : tpl`<span class="muted">not run yet</span>`)
      : c.skipped ? tpl`<span class="muted">skipped</span>${age}`
      : c.ok ? tpl`<span class="ok">ok</span>${age}` : tpl`<span class="bad">fail</span>${age}`;
    const readings = (c.readings && c.readings.length) ? renderWatchReadings(c.readings) : nothing;
    const msg = c.message
      ? tpl`<span class="truncate check-message" title="${c.message || ""}">${c.message || ""}</span>`
      : nothing;
    const hasReadings = !!(c.readings && c.readings.length);
    const detailCell = (hasReadings || c.message) ? tpl`${readings}${msg}` : "—";
    return tpl`<tr><td>${c.name}</td><td class="muted">${c.type || ""}</td>
      <td>${state}${c.optional ? tpl` <span class="muted">(optional)</span>` : nothing}</td>
      <td class="sla-cell">${renderSLAWindows(c.sla, true)}</td>
      <td class="muted">${detailCell}</td></tr>`;
  });
  const checks = checkRows.length ? checkRows : tpl`<tr><td colspan="5" class="muted">No checks.</td></tr>`;

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
  const lockRows = lockRowsArr.length ? lockRowsArr : tpl`<tr><td colspan="8" class="muted">No named runtime locks.</td></tr>`;
  const lockWarns = (d.lock_warnings || []).map((w) =>
    tpl`<div class="inactive detail-warn">warning: ${w}</div>`
  );

  const pt = d.process_totals || (noResidentProcess ? null : {
    rss: procs.reduce((a, p) => a + (p.rss || 0), 0),
    io_read: procs.reduce((a, p) => a + (p.io_read || 0), 0),
    io_write: procs.reduce((a, p) => a + (p.io_write || 0), 0),
    fds: procs.reduce((a, p) => a + (p.fds || 0), 0),
    threads: procs.reduce((a, p) => a + (p.threads || 0), 0),
    count: procs.length,
  });
  // When the host RAM total is known, show each process's resident memory as a
  // share of host RAM (a compact bar), plus a bar on the whole-tree total.
  const hostMem = hostMemTotalBytes();
  const memPct = (rss) => hostMem > 0 ? pctClamp((Number(rss) || 0) / hostMem * 100) : 0;
  const totalBar = pt && hostMem > 0
    ? tpl` ${usageBarMini(memPct(pt.rss || 0), fmtPct(memPct(pt.rss || 0)))}`
    : nothing;
  const totals = pt
    ? tpl`<p class="muted detail-totals">Service totals (including child processes): memory <b>${fmtBytes(pt.rss || 0)}</b>${totalBar}${cpuTotalsLine(pt)} · IO r/w <b>${fmtBytes(pt.io_read || 0)} / ${fmtBytes(pt.io_write || 0)}</b> · fds <b>${pt.fds || 0}</b> · threads <b>${pt.threads || 0}</b> · ${pt.count} process${pt.count === 1 ? "" : "es"}</p>`
    : nothing;
  const procWarns = procWarnings.map((w) => tpl`<div class="bad detail-warn">discovery warning: ${w}</div>`);
  const procSummary = tpl`<p class="muted detail-summary">${procs.length} discovered${procWarnings.length ? ` · ${procWarnings.length} discovery warning${procWarnings.length === 1 ? "" : "s"}` : ""}</p>`;
  const procRows = processRows(procs);
  const procTable = procs.length
    ? tpl`<table class="detail-compact-table">
        <caption class="visually-hidden">Service processes</caption>
        <thead><tr><th scope="col">PID</th><th scope="col">CMD</th><th scope="col">User</th><th scope="col">Role</th><th scope="col">CPU</th><th scope="col" title="CPU used by this process, normalized to one core">Core peak</th><th scope="col">Mem</th><th scope="col">IO r/w</th><th scope="col">FDs</th><th scope="col">Threads</th></tr></thead>
        <tbody>${procRows.map((row) => { const p = row.p; return tpl`<tr>
          <td>${p.pid}</td>
          <td>${procTreeLabel(row)}</td>
          <td class="muted">${p.user || ""}</td>
          <td class="muted">${p.role || ""}</td>
          ${procCpuCells(p)}
          <td>${p.rss ? (hostMem > 0 ? usageBarMini(memPct(p.rss), fmtBytes(p.rss)) : fmtBytes(p.rss)) : '—'}</td>
          ${procIoFdThreadCells(p)}
        </tr>`; })}</tbody></table>`
    : tpl`<div class="${procWarnings.length ? "bad" : "muted"}">${procWarnings.length ? "No processes discovered; check discovery warnings." : "No processes found."}</div>`;

  const measured = serviceMeasuredChecks(d);
  const activeMetricCheck = selectedMetricCheck(measured);
  const checkBtns = measured.length
    ? metricCheckButtons(d.name, measured, activeMetricCheck)
    : tpl`<span class="muted">No latency checks</span>`;
  const latencyPanel = measured.length
    ? tpl`<div id="${detailDomId(d.name, "lat-summary")}" class="muted">loading…</div>
      <div id="${detailDomId(d.name, "lat-chart")}" class="muted chart-box"></div>`
    : tpl`<div class="muted">No latency checks configured for this service.</div>`;
  const runtimeGraphPanels = noResidentProcess
    ? nothing
    : tpl`<div class="metric-panel">
        <div class="metric-title">Latency <span class="muted">${checkBtns}</span></div>
        ${latencyPanel}
      </div>
      <div class="metric-panel">
        <div class="metric-title">CPU</div>
        <div id="${detailDomId(d.name, "runtime-cpu-summary")}" class="muted">loading…</div>
        <div id="${detailDomId(d.name, "runtime-cpu-chart")}" class="muted chart-box"></div>
      </div>
      <div class="metric-panel">
        <div class="metric-title">Memory</div>
        <div id="${detailDomId(d.name, "runtime-memory-summary")}" class="muted">loading…</div>
        <div id="${detailDomId(d.name, "runtime-memory-chart")}" class="muted chart-box"></div>
      </div>
      <div class="metric-panel">
        <div class="metric-title">IO</div>
        <div id="${detailDomId(d.name, "runtime-io-summary")}" class="muted">loading…</div>
        <div id="${detailDomId(d.name, "runtime-io-chart")}" class="muted chart-box"></div>
      </div>`;
  const graphs = tpl`<h2>Graphs <span class="muted">${winButtons(metricWins, metricWindow, "setMetricWin", "Graph time window")}</span></h2>
    <div class="metric-grid">
      <div class="metric-panel metric-panel-wide">
        <div class="sla-chart-head">
          <span class="metric-title">SLA timeline</span>
          <span id="${detailDomId(d.name, "sla-summary")}" class="muted">loading...</span>
        </div>
        <div class="sla-panel">
          <div class="sla-chart-panel">
            <div id="${detailDomId(d.name, "sla-chart")}" class="muted chart-box-wide"></div>
          </div>
        </div>
      </div>
      ${runtimeGraphPanels}
    </div>`;

  const disabledNote = !d.enabled
    ? tpl`<p class="muted bad">This service is disabled in configuration (enabled: false). Edit its YAML file and reload the daemon to activate it.</p>`
    : nothing;
  const processGeneral = noResidentProcess
    ? nothing
    : tpl`<div><span class="muted">Processes</span><br>${pt ? `${pt.count} process${pt.count === 1 ? "" : "es"}` : tpl`<span class="muted">—</span>`}</div>
      <div><span class="muted">CPU total</span><br>${totalsCpuCell(pt)}</div>
      <div><span class="muted">Memory</span><br>${memoryInline(pt && pt.rss)}</div>
      <div><span class="muted">IO R/W</span><br>${ioRWInline(pt && pt.io_read, pt && pt.io_write)}</div>
      <div><span class="muted">FDs / Threads</span><br>${pt ? `${pt.fds || 0} / ${pt.threads || 0}` : tpl`<span class="muted">—</span>`}</div>`;
  const general = tpl`<h2>General data</h2>
    <div class="runtime-grid">
      <div><span class="muted">State</span><br>${serviceStateCell(d)}</div>
      <div><span class="muted">Category</span><br>${categoryBadge(categoryOf(d, "service"))}</div>
      <div><span class="muted">Unit</span><br>${unitCell(d)}</div>
      <div><span class="muted">Backend</span><br>${d.backend || "—"}</div>
      <div><span class="muted">Uptime</span><br>${serviceUptimeCell(d)}</div>
      <div><span class="muted">Interval</span><br>${d.interval ? d.interval : tpl`<span class="muted">—</span>`}</div>
      <div><span class="muted">Dry run</span><br><b>${d.dry_run ? "yes" : "no"}</b></div>
      <div><span class="muted">Policy</span><br>${policyCell(d)}</div>
      <div><span class="muted">Locks</span><br>${locksCell(d)}</div>
      <div><span class="muted">Last event</span><br>${lastEventCell(d)}</div>
      <div><span class="muted">Next remediation</span><br>${nextRemediationCell(d)}</div>
      <div><span class="muted">Remediation</span><br>${renderRemediation(d.remediation)}</div>
      ${processGeneral}
    </div>`;
  const processSection = noResidentProcess
    ? nothing
    : tpl`<h2>Processes</h2>
      ${procSummary}${totals}${procWarns}${procTable}`;
  return tpl`<div class="service-detail" data-service-detail="${d.name}">
    <h2>${displayName(d)} <span class="muted">${d.unit || ""}</span></h2>
    ${disabledNote}
    ${general}
    ${graphs}
    ${processSection}
    <h2>Checks</h2>
    <table>
      <caption class="visually-hidden">Service checks</caption>
      <thead><tr><th scope="col">Check</th><th scope="col">Type</th><th scope="col">State</th><th scope="col">SLA</th><th scope="col">Message</th></tr></thead>
      <tbody>${checks}</tbody></table>
    <h2>Named locks</h2>
    <table>
      <caption class="visually-hidden">Service named locks</caption>
      <thead><tr><th scope="col">Name</th><th scope="col">State</th><th scope="col">TTL</th><th scope="col">Owner</th><th scope="col">Created</th><th scope="col">Blocks</th><th scope="col">Reason</th><th scope="col"><span class="visually-hidden">Actions</span></th></tr></thead>
      <tbody>${lockRows}</tbody></table>${lockWarns}
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

function hydrateServiceDetail(d) {
  const measured = serviceMeasuredChecks(d);
  syncWindowButtons("setMetricWin", metricWindow);
  loadServiceSLA(d.name);
  if (!d.no_resident_process) {
    if (measured.length) loadMetrics(d.name, measured);
    loadServiceRuntimeMetrics(d.name);
  }
  loadServiceEvents(d.name);
}

// fmtNum renders a number with at most `max` decimals (default 2), dropping any
// trailing zeros so 5.00 -> "5", 5.10 -> "5.1" and 5.125 -> "5.13". Non-finite
// values render as `fallback`. This is the single canonical numeric formatter;
// route every user-facing reading through it instead of bare toFixed (geometry —
// SVG coordinates, CSS bar widths — keeps its own fixed precision).
function fmtNum(n, max = 2, fallback = "—") {
  n = Number(n);
  if (!Number.isFinite(n)) return fallback;
  return n.toFixed(max).replace(/(\.\d*?)0+$/, "$1").replace(/\.$/, "");
}

// fmtUptime renders a duration given in whole seconds as "111d 22h 33m 44s",
// dropping the leading units that are zero (95 -> "1m 35s", 44 -> "44s") while
// always keeping every unit below the largest non-zero one down to seconds.
// This is the single uptime format used everywhere in the UI. Returns "" for
// missing/negative input so callers can fall back to "—".
function fmtUptime(sec) {
  sec = Math.floor(Number(sec));
  if (!Number.isFinite(sec) || sec < 0) return "";
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = sec % 60;
  const parts = [];
  if (d) parts.push(d + "d");
  if (d || h) parts.push(h + "h");
  if (d || h || m) parts.push(m + "m");
  parts.push(s + "s");
  return parts.join(" ");
}

function fmtBytes(n) {
  n = Number(n);
  // Guard non-finite/negative inputs (e.g. an inconsistent backend counter):
  // dividing a negative repeatedly would otherwise render nonsense like "-1 KB".
  if (!Number.isFinite(n) || n < 0) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  // Route every unit through fmtNum (including raw bytes): integer byte counts
  // still render clean (512 -> "512 B") while fractional rates lose the long tail
  // (234.5678 B/s -> "234.57 B/s") instead of leaking full float precision.
  return fmtNum(n, 2, "0") + " " + u[i];
}

function fmtPct(n) {
  n = Number(n);
  return Number.isFinite(n) ? fmtNum(n, 2) + "%" : "—";
}

function pctClamp(n) {
  n = Number(n);
  if (!Number.isFinite(n)) return 0;
  return Math.max(0, Math.min(100, n));
}

function usageLevel(pct) {
  pct = pctClamp(pct);
  if (pct <= 0) return "usage-empty";
  if (pct >= 95) return "usage-crit";
  if (pct >= 90) return "usage-high";
  if (pct >= 75) return "usage-warn";
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
  const freeLabel = fmtPct(100 - p);
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
  const m = (latestHostMetrics || []).find((x) => x.name === "total_memory");
  return m && m.total > 0 ? Number(m.total) : 0;
}

// cpuBarMini renders a single-core-normalized CPU% as a compact bar (100% = one
// full core). A multithreaded process can exceed 100%; the bar caps at full but
// the label keeps the true value.
function cpuBarMini(pct) {
  const v = Number(pct) || 0;
  return usageBarMini(pctClamp(v), fmtPct(v), `${fmtNum(v, 2)}% of one core used by this process`);
}

// cpuTotalsLine renders the whole-tree CPU summary (whole-machine %) for a
// process_totals object, or a "measuring" hint until the first rate is
// available. "" when CPU was never sampled (no live registry).
function cpuTotalsLine(pt) {
  if (!pt) return nothing;
  if (!pt.has_cpu) return pt.num_cpu ? tpl` · cpu <span class="muted">measuring…</span>` : nothing;
  const machine = Number(pt.cpu) || 0;
  const machineBar = usageBarMini(pctClamp(machine), fmtPct(machine), `${fmtNum(machine, 2)}% of ${pt.num_cpu || "?"} cores`);
  return tpl` · cpu <b>${fmtPct(machine)}</b> ${machineBar}`;
}

// storageUsedPct returns the used percentage 0..100, or null when the volume
// reports no usable figures — so callers render "—" instead of a misleading
// 0% (empty/healthy-looking) bar for missing data.
function storageUsedPct(d) {
  if (!d) return null;
  const used = Number(d.used_bytes);
  const total = Number(d.total_bytes);
  if (Number.isFinite(used) && Number.isFinite(total) && total > 0) return pctClamp((used / total) * 100);
  const free = Number(d.free_bytes);
  if (Number.isFinite(free) && Number.isFinite(total) && total > 0) return pctClamp(((total - free) / total) * 100);
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
    case "memory":
      return [`${fmtBytes(m.total_bytes)} total`,
        `${fmtBytes(m.used_bytes)} used · ${fmtBytes(m.free_bytes)} free`];
    case "load":
      return [`${m.num_cpu || 0} CPU${(m.num_cpu === 1) ? "" : "s"}`,
        `load1 ${fmtNum(m.load || 0, 2)} · ${fmtPct(m.used_pct)} of capacity`];
    case "fds":
      return [`${(m.max || 0).toLocaleString()} file descriptors max`,
        `${(m.count || 0).toLocaleString()} allocated · ${((m.max || 0) - (m.count || 0)).toLocaleString()} free`];
    case "pids":
      return [`${(m.max || 0).toLocaleString()} max`,
        `${(m.count || 0).toLocaleString()} in use · ${((m.max || 0) - (m.count || 0)).toLocaleString()} free`];
    case "conntrack":
      return [`${(m.max || 0).toLocaleString()} max`,
        `${(m.count || 0).toLocaleString()} entries · ${((m.max || 0) - (m.count || 0)).toLocaleString()} free`];
    default:
      return null;
  }
}

function meterSummaryCell(m) {
  const parts = meterParts(m);
  if (!parts) return nothing;
  return tpl`<div>${parts[0]}</div>
    <div>${usageBar(pctClamp(m.used_pct || 0))} <span class="muted">· ${parts[1]}</span></div>`;
}

function watchReadings(w) {
  return (w && Array.isArray(w.readings)) ? w.readings.filter(Boolean) : [];
}

function readingText(r) {
  if (!r) return "";
  const label = r.label || r.field || "sample";
  return r.error ? `${label}: ${r.error}` : `${label} ${r.value || ""}`.trim();
}

function readingsSummaryCell(w) {
  const list = watchReadings(w);
  if (!list.length) return nothing;
  const errors = list.filter((r) => r.error);
  if (errors.length) {
    return errors.map((r, i) => i ? [tpl`<br>`, tpl`<span class="bad">${readingText(r)}</span>`] : tpl`<span class="bad">${readingText(r)}</span>`);
  }
  const detail = w.summary ? "" : list.slice(0, 3).map(readingText).filter(Boolean).join(" · ");
  return tpl`<div>${w.summary || w.check_type || "watch"}</div>${
    detail ? tpl`<div class="muted">· ${detail}</div>` : nothing}`;
}

function watchSummaryCell(w) {
  if (!w) return "—";
  const sw = w.swap;
  if (sw) {
    // Volume-style rendering for a swap watch: bar + used/free, like storage.
    return tpl`<div><span class="muted">${fmtBytes(sw.total_bytes)} total</span></div>
      <div>${usageBar(pctClamp(sw.used_pct || 0))} <span class="muted">· ${fmtBytes(sw.used_bytes)} used · ${fmtBytes(sw.free_bytes)} free</span></div>`;
  }
  const meterCell = meterSummaryCell(w.meter);
  if (meterCell !== nothing) return meterCell;
  const readingCell = readingsSummaryCell(w);
  if (readingCell !== nothing) return readingCell;
  const d = w.storage;
  if (d) {
    if (d.sample_error) {
      return tpl`<span class="bad">${d.path || ""}: ${d.sample_error}</span>`;
    }
    const fs = d.filesystem ? ` · ${d.filesystem}` : "";
    const mount = d.mount_point && d.mount_point !== d.path ? ` · ${d.mount_point}` : "";
    const usedPct = storageUsedPct(d);
    const bar = usedPct == null ? tpl`<span class="muted">—</span>` : usageBar(usedPct);
    const openFiles = d.open_files ? tpl` · <span title="open files on this filesystem">${Number(d.open_files).toLocaleString()} open files</span>` : nothing;
    return tpl`<div>${d.path || ""}<span class="muted">${fs}${mount}</span></div>
      <div>${bar} <span class="muted">· ${fmtBytes(d.used_bytes)} used · ${fmtBytes(d.free_bytes)} free${openFiles}</span></div>`;
  }
  return w.summary ? w.summary : "—";
}

function watchMonitorHint(w) {
  const bits = [];
  if (w.monitor_source) bits.push(fmtMonitorSource(w.monitor_source));
  if (w.monitor_changed_at) bits.push(fmtAge(w.monitor_changed_at));
  return bits.length ? tpl` <span class="muted">${bits.join(" · ")}</span>` : nothing;
}

function watchMonitorMode(w) {
  return w && w.monitor ? w.monitor : "enabled";
}

function watchHasNotify(w) {
  return notifierNames(w).length > 0 || (w && Number(w.notifier_count || 0) > 0);
}

function watchHasExpand(w) {
  return !!(w && w.expand && Number(w.expand.by_bytes) > 0);
}

// watchStateText reads the server-computed health state (app.WatchState:
// disabled, starting, failed or ok). Monitor state remains available to actions
// and search, but the State column renders one state badge.
function watchStateText(w) {
  return (w && w.state) || "unknown";
}

function watchStateRank(w) {
  return stateRank(watchStateText(w));
}

function watchStateCell(w) {
  return stateBadge(watchStateText(w));
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
  const category = categoryOf(w, "watch");
  return [
    displayName(w),
    w.name,
    category,
    w.check_type,
    watchSummaryText(w),
    w.interval,
    w.fire_on_fail ? "on fail" : "on threshold",
    w.has_hook ? "hook" : "",
    (w.hook_command || []).join(" "),
    notifierNames(w).join(" "),
    watchHasNotify(w) ? "notify notifiers" : "",
    watchHasExpand(w) ? "expand" : "",
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
  return (panel.typeOf ? panel.typeOf(w) : w.check_type) || "";
}

function watchMatches(w, panelKey) {
  const panel = getWatchPanel(panelKey);
  if (panel.query && !watchSearchText(w).includes(panel.query)) return false;
  if (panel.type !== "all" && watchTypeValue(panel, w) !== panel.type) return false;
  switch (panel.status) {
    case "disabled":      return watchStateText(w) === "disabled";
    case "ok":            return watchStateText(w) === "ok";
    case "starting":      return watchStateText(w) === "starting";
    case "failed":        return watchStateText(w) === "failed";
    default:           return true;
  }
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
    watchPanels[key].status = v || "all";
    syncWatchFilterActive(key);
  });
  renderWatches();
  saveUIState();
}

function setWatchType(panelKey, v) {
  const panel = getWatchPanel(panelKey);
  panel.type = v || "all";
  renderWatches();
  saveUIState();
}

// syncWatchTypeSelect repopulates one watch panel's type dropdown from the
// distinct check types currently present in that panel (with per-type counts),
// mirroring the apps category select. Returns the reconciled selection ("all" if
// the chosen type no longer exists).
function syncWatchTypeSelect(panelKey, watches) {
  const panel = getWatchPanel(panelKey);
  const select = $(panel.typeSelect);
  if (!select) return "all";
  const counts = new Map();
  (watches || []).forEach((w) => {
    const t = watchTypeValue(panel, w);
    if (t) counts.set(t, (counts.get(t) || 0) + 1);
  });
  const types = [...counts.keys()].sort((a, b) =>
    a.localeCompare(b, undefined, { numeric: true, sensitivity: "base" }));
  // Some panels (certificates) only expose the type filter once enough distinct
  // types exist to be worth choosing between. Below that threshold, hide the
  // dropdown and force "all" so no stale selection keeps filtering.
  if (panel.typeFilterMin && types.length < panel.typeFilterMin) {
    select.hidden = true;
    select.innerHTML = `<option value="all">${esc(panel.allTypesLabel)}</option>`;
    select.value = "all";
    return "all";
  }
  select.hidden = false;
  const next = panel.type !== "all" && counts.has(panel.type) ? panel.type : "all";
  select.innerHTML = `<option value="all">${esc(panel.allTypesLabel)}</option>` + types.map((t) =>
    `<option value="${esc(t)}">${esc(t)} (${counts.get(t)})</option>`).join("");
  select.value = next;
  return next;
}

function renderWatchFilterCounts(panelKey, watches) {
  const w = watches || allWatches || [];
  renderFilterButtonCounts(getWatchPanel(panelKey).filters, {
    all: w.length,
    disabled: w.filter((x) => watchStateText(x) === "disabled").length,
    ok: w.filter((x) => watchStateText(x) === "ok").length,
    starting: w.filter((x) => watchStateText(x) === "starting").length,
    failed: w.filter((x) => watchStateText(x) === "failed").length,
  });
}

function watchPanelFilterActive(panel) {
  return !!(panel.query || panel.status !== "all" || panel.type !== "all");
}

function parseDurationSeconds(raw) {
  const s = String(raw || "").trim();
  if (!s) return 0;
  let total = 0;
  let matched = false;
  const re = /(\d+(?:\.\d+)?)(ms|s|m|h)/g;
  let m;
  while ((m = re.exec(s)) !== null) {
    matched = true;
    const n = parseFloat(m[1]);
    switch (m[2]) {
      case "ms": total += n / 1000; break;
      case "s": total += n; break;
      case "m": total += n * 60; break;
      case "h": total += n * 3600; break;
    }
  }
  if (matched) return total;
  const n = parseFloat(s);
  return Number.isFinite(n) ? n : 0;
}

const watchSortKeys = {
  name: (w) => displayName(w).toLowerCase(),
  type: (w) => (w.check_type || "").toLowerCase(),
  summary: (w) => watchSummaryText(w).toLowerCase(),
  interval: (w) => parseDurationSeconds(w.interval),
  polarity: (w) => w.fire_on_fail ? "fail" : "threshold",
  hook: (w) => w.has_hook ? 1 : 0,
  notifiers: (w) => notifierNames(w).join(" ").toLowerCase() || Number(w.notifier_count || 0),
  last: (w) => w.last_activity || "",
  state: watchStateRank,
  // Type-specific columns for the Certificate and Disk I/O panels, read from
  // the watch's live readings (see readingRaw / checkreadings.go field names).
  expires: (w) => readingRaw(w, "not_after"),
  days_left: (w) => { const n = parseFloat(readingRaw(w, "days_left")); return Number.isFinite(n) ? n : Infinity; },
  issuer: (w) => readingRaw(w, "issuer").toLowerCase(),
  keytype: (w) => readingRaw(w, "public_key_algorithm").toLowerCase(),
  device: (w) => readingRaw(w, "device").toLowerCase(),
  util: (w) => parseFloat(readingRaw(w, "util_pct")) || 0,
  readwrite: (w) => parseFloat(readingRaw(w, "read_bytes")) || 0,
  await: (w) => parseFloat(readingRaw(w, "await_ms")) || 0,
  // Storage panel columns.
  usage: (w) => { const p = w.storage ? storageUsedPct(w.storage) : null; return p == null ? -1 : p; },
  filesystem: (w) => ((w.storage && w.storage.filesystem) || "").toLowerCase(),
  mount: (w) => ((w.storage && w.storage.mount_point) || "").toLowerCase(),
};

function setWatchSort(panelKey, key) { toggleSort(getWatchPanel(panelKey).sort, key, renderWatches); }

function updateWatchSortIndicators(panelKey) {
  const panel = getWatchPanel(panelKey);
  document.querySelectorAll(`${panel.section} .sort-ind[data-wi]`).forEach((el) => {
    el.textContent = el.dataset.wi === panel.sort.key ? (panel.sort.dir > 0 ? " ▲" : " ▼") : "";
  });
  document.querySelectorAll(`${panel.section} th.sortable[data-watch-sort]`).forEach((th) => {
    th.setAttribute("aria-sort", sortAriaValue(panel.sort, th.dataset.watchSort || ""));
  });
}

function watchPanelKeyForElement(el) {
  const id = (el && el.closest("details") && el.closest("details").id) || "";
  if (id === "storage-section") return "storage";
  if (id === "network-section") return "network";
  return "host";
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
    ? `${(Number(d.inodes_total) - Number(d.inodes_free || 0)).toLocaleString()} used / ${Number(d.inodes_total).toLocaleString()} total (${fmtPct(d.inodes_used_pct)} used)`
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
  if (m.kind === "memory") {
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
      tpl`<div><span class="muted">${label}</span><br><b>${(m.count || 0).toLocaleString()}</b></div>`,
      tpl`<div><span class="muted">Max</span><br><b>${(m.max || 0).toLocaleString()}</b></div>`,
      tpl`<div><span class="muted">Used</span><br>${usageBar(usedPct)} <b>${fmtPct(m.used_pct)}</b></div>`,
      tpl`<div><span class="muted">Free</span><br><b>${((m.max || 0) - (m.count || 0)).toLocaleString()}</b></div>`);
  }
  return tpl`<div class="watch-grid">${cells}</div>`;
}

function renderWatchReadings(readings) {
  const list = (readings || []).filter(Boolean);
  if (!list.length) return nothing;
  const cells = list.map((r) => {
    const label = r.label || r.field || "Sample";
    const longValue = ["issuer", "dns_names"].includes(r.field || "");
    const value = r.error
      ? tpl`<span class="watch-reading-value bad">${r.error}</span>`
      : tpl`<b class="watch-reading-value">${r.value || "—"}</b>`;
    return tpl`<div class="watch-reading${longValue ? " watch-reading-long" : ""}"><span class="muted">${label}</span><br>${value}</div>`;
  });
  return tpl`<div class="watch-grid">${cells}</div>`;
}

// isStorageWatch reports whether a watch is a storage/volume check. Storage
// watches get their own panel above Services; every other type stays in the
// Host watches table below. Matches the backend isStorageCheckType.
function isStorageWatch(w) {
  const t = ((w && w.check_type) || "").toLowerCase();
  return t === "storage";
}

// isNetworkWatch reports whether a watch is a network/connectivity check. These
// get their own panel right after Services; every other (non-storage) type stays
// in the Host watches table below.
function isNetworkWatch(w) {
  const t = ((w && w.check_type) || "").toLowerCase();
  return t === "net" || t === "icmp";
}

// isCertWatch / isDiskioWatch report whether a watch is a certificate or disk
// I/O check. Each gets its own panel (with type-specific columns); every other
// non-storage/non-network type stays in the Host watches table below.
function isCertWatch(w) {
  return ((w && w.check_type) || "").toLowerCase() === "cert";
}

function isDiskioWatch(w) {
  return ((w && w.check_type) || "").toLowerCase() === "diskio";
}

function watchActionDisabled(w, action) {
  if (!w || !w.enabled) return true;
  if (watchStateText(w) === "starting") return true;
  switch (action) {
    case "monitor": return !!w.monitored;
    case "unmonitor": return !w.monitored;
    case "expand": return !watchHasExpand(w);
    default: return false;
  }
}

function watchActionDisabledReason(w, action) {
  if (watchStateText(w) === "starting") return "watch is starting";
  switch (action) {
    case "monitor":
      if (w.monitored) return "watch is already monitored";
      return "";
    case "unmonitor":
      if (!w.monitored) return "watch is paused";
      return "";
    case "expand":
      if (!watchHasExpand(w)) return "expand is not configured";
      return "";
    default: return "";
  }
}

function watchActionHintId(w, action) {
  return `wat-${w.name}-${action}-hint`;
}

function watchActionHint(w, action) {
  const disabled = watchActionDisabled(w, action);
  const reason = watchActionDisabledReason(w, action);
  if (!disabled || !reason) return nothing;
  return tpl`<span id="${watchActionHintId(w, action)}" class="visually-hidden">${reason}</span>`;
}

function watchActionDescribedBy(w, action) {
  const disabled = watchActionDisabled(w, action);
  const reason = watchActionDisabledReason(w, action);
  return disabled && reason ? watchActionHintId(w, action) : nothing;
}

function watchActionAriaLabel(w, action) {
  const name = displayName(w) || w.name || "";
  switch (action) {
    case "expand": return `Expand storage for watch ${name}`;
    case "monitor": return `Monitor watch ${name}`;
    case "unmonitor": return `Unmonitor watch ${name}`;
    default: return `${action} watch ${name}`;
  }
}

// readingValue returns the formatted value of a named live reading (as shipped
// in /api/watches) for display in a type-specific column, or "—" when absent.
// Used by the Certificate and Disk I/O panels. Field names match checkreadings.go.
function readingValue(w, field) {
  const r = ((w && w.readings) || []).find((x) => x && x.field === field);
  if (!r) return "—";
  if (r.error) return tpl`<span class="bad">${r.error}</span>`;
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

// watchNameCell renders the shared expandable name cell (chevron + toggle).
function watchNameCell(w, key, open) {
  const chev = tpl`<span class="exp" aria-hidden="true">${open ? '▾' : '▸'}</span>`;
  return tpl`<td>${chev}<button type="button" class="row-toggle" data-exp-toggle="${key}" aria-expanded="${open}" aria-controls="${open ? "exp-" + key : nothing}" aria-label="${expandToggleAriaLabel(displayName(w), open, "watch details")}">${displayName(w)}</button></td>`;
}

// watchActionsCell renders the shared actions cell (expand / monitor / unmonitor).
function watchActionsCell(w) {
  const expandBtn = (w.expand && Number(w.expand.by_bytes) > 0 && me.can_act && w.enabled)
    ? tpl`${watchActionHint(w, "expand")}<button ?disabled=${watchActionDisabled(w, "expand")} data-watch="${w.name}" data-watch-action="expand" aria-label="${watchActionAriaLabel(w, "expand")}" aria-describedby="${watchActionDescribedBy(w, "expand")}">expand ${fmtBytes(w.expand.by_bytes)}</button>`
    : nothing;
  const monitorBtn = !w.enabled
    ? tpl`<span class="muted">disabled in config</span>`
    : (me.can_act
      ? (w.monitored
        ? tpl`${watchActionHint(w, "unmonitor")}<button ?disabled=${watchActionDisabled(w, "unmonitor")} data-watch="${w.name}" data-watch-action="unmonitor" aria-label="${watchActionAriaLabel(w, "unmonitor")}" aria-describedby="${watchActionDescribedBy(w, "unmonitor")}">unmonitor</button>`
        : tpl`${watchActionHint(w, "monitor")}<button ?disabled=${watchActionDisabled(w, "monitor")} data-watch="${w.name}" data-watch-action="monitor" aria-label="${watchActionAriaLabel(w, "monitor")}" aria-describedby="${watchActionDescribedBy(w, "monitor")}">monitor</button>`)
      : tpl`<span class="muted">read-only</span>`);
  const actions = !w.enabled
    ? tpl`<span class="muted">disabled in config</span>`
    : tpl`${expandBtn} ${monitorBtn}`;
  return tpl`<td class="actions">${actions}</td>`;
}

// watchRowClass mirrors the service/app row highlight: a firing watch (state
// "failed") paints the row red, a warning amber, matching serviceRowParts so
// certificate and every other host-watch panel follow the same visual line.
function watchRowClass(state) {
  return state === "failed" ? "row-failing" : (state === "warning" ? "row-warning" : "");
}

// watchExpansionRow returns the inline expansion row when open. Its colspan must
// match the number of columns in the panel's table — 9 for most, but the
// Certificate panel passes 10 for its extra Key type column.
function watchExpansionRow(key, open, cols = 9) {
  return open
    ? tpl`<tr class="exp-row" id="exp-${key}" data-exp="${key}"><td colspan="${cols}"></td></tr>`
    : null;
}

// watchRowHTML builds the table row(s) for one watch — the main row plus its
// expansion row when open. Shared by the Storage, Network and Host watches
// panels so they render identically (including the expand action).
function watchRowHTML(w) {
  const state = watchStateText(w);
  const key = "wat:" + w.name;
  const open = expanded.has(key);
  const row = tpl`<tr id="wat-row-${w.name}" class="clickable ${watchRowClass(state)}" data-exp-key="${key}">
    ${watchNameCell(w, key, open)}
    <td>${w.check_type || ""}</td>
    <td class="watch-summary">${watchSummaryCell(w)}</td>
    <td>${watchLastCell(w)}</td>
    <td>${watchStateCell(w)}</td>
    ${watchActionsCell(w)}
  </tr>`;
  const expRow = watchExpansionRow(key, open, 6);
  return expRow ? [row, expRow] : [row];
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
function storageRowHTML(w) {
  const state = watchStateText(w);
  const d = w.storage || {};
  const fs = d.filesystem ? tpl`<code>${d.filesystem}</code>` : tpl`<span class="muted">—</span>`;
  const mount = d.mount_point
    ? (d.mounted === false ? tpl`<span class="bad">${d.mount_point} (not mounted)</span>` : tpl`<code>${d.mount_point}</code>`)
    : (w.storage && w.storage.mounted === false ? tpl`<span class="bad">not found</span>` : tpl`<span class="muted">—</span>`);
  const key = "wat:" + w.name;
  const open = expanded.has(key);
  const row = tpl`<tr id="wat-row-${w.name}" class="clickable ${watchRowClass(state)}" data-exp-key="${key}">
    ${watchNameCell(w, key, open)}
    <td class="watch-summary">${storageUsageCell(w)}</td>
    <td>${fs}</td>
    <td>${mount}</td>
    <td>${watchLastCell(w)}</td>
    <td>${watchStateCell(w)}</td>
    ${watchActionsCell(w)}
  </tr>`;
  const expRow = watchExpansionRow(key, open, 7);
  return expRow ? [row, expRow] : [row];
}

// certRowHTML renders a Certificate-panel row, surfacing the most relevant cert
// readings (expiry date, days left, issuer) in place of the generic type/summary.
function certRowHTML(w) {
  const state = watchStateText(w);
  const key = "wat:" + w.name;
  const open = expanded.has(key);
  const algo = readingRaw(w, "public_key_algorithm");
  const bits = readingRaw(w, "key_bits");
  const keyType = algo
    ? tpl`<code>${bits ? `${algo} ${bits}` : algo}</code>`
    : tpl`<span class="muted">—</span>`;
  const row = tpl`<tr id="wat-row-${w.name}" class="clickable ${watchRowClass(state)}" data-exp-key="${key}">
    ${watchNameCell(w, key, open)}
    <td class="cert-expires">${readingValue(w, "not_after")}</td>
    <td>${readingValue(w, "days_left")}</td>
    <td>${readingValue(w, "issuer")}</td>
    <td>${keyType}</td>
    <td>${watchLastCell(w)}</td>
    <td>${watchStateCell(w)}</td>
    ${watchActionsCell(w)}
  </tr>`;
  const expRow = watchExpansionRow(key, open, 8);
  return expRow ? [row, expRow] : [row];
}

// diskioRowHTML renders a Disk I/O-panel row, surfacing the most relevant diskio
// readings (device, utilization, read/write throughput, await latency).
function diskioRowHTML(w) {
  const state = watchStateText(w);
  const key = "wat:" + w.name;
  const open = expanded.has(key);
  const row = tpl`<tr id="wat-row-${w.name}" class="clickable ${watchRowClass(state)}" data-exp-key="${key}">
    ${watchNameCell(w, key, open)}
    <td>${readingValue(w, "device")}</td>
    <td>${readingValue(w, "util_pct")}</td>
    <td>${readingValue(w, "read_bytes")} / ${readingValue(w, "write_bytes")}</td>
    <td>${readingValue(w, "await_ms")}</td>
    <td>${watchLastCell(w)}</td>
    <td>${watchStateCell(w)}</td>
    ${watchActionsCell(w)}
  </tr>`;
  const expRow = watchExpansionRow(key, open, 8);
  return expRow ? [row, expRow] : [row];
}

function renderWatches(watches) {
  if (watches) allWatches = watches;
  const all = allWatches || [];
  renderWatchPanel("storage", all.filter(isStorageWatch));
  renderWatchPanel("network", all.filter(isNetworkWatch));
  renderWatchPanel("cert", all.filter(isCertWatch));
  renderWatchPanel("diskio", all.filter(isDiskioWatch));
  renderWatchPanel("host", all.filter((w) => !isStorageWatch(w) && !isNetworkWatch(w) && !isCertWatch(w) && !isDiskioWatch(w)));
  expanded.forEach((k) => { if (k.startsWith("wat:")) loadExpansionFor(k); });
  applyHash();
  updateSectionNav();
}

// renderWatchPanel fills one watch table (Storage, Network, or Host watches)
// from its already-classified subset, using the same search/type/status filters,
// visible count and column sorting for every panel.
function renderWatchPanel(panelKey, watches) {
  const panel = getWatchPanel(panelKey);
  const section = $(panel.section);
  const tbody = $(panel.rows);
  const cnt = $(panel.count);
  const filterCount = $(panel.filterCount);
  if (!section || !tbody) return;
  const total = (watches || []).length;
  if (total === 0) {
    setPanelVisible(section, false);
    if (cnt) cnt.textContent = "";
    if (filterCount) filterCount.textContent = "";
    litRender(nothing, tbody);
    return;
  }
  setPanelVisible(section, true);
  if (cnt) cnt.textContent = `(${total})`;
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
  const filtered = watchPanelFilterActive(panel);
  if (filterCount) filterCount.textContent = filtered ? `showing ${list.length} of ${total}` : "";
  const content = list.length
    ? list.flatMap(panel.rowHTML || watchRowHTML)
    : tpl`<tr><td colspan="${panel.cols || 9}" class="muted">${filtered ? panel.emptyFiltered : panel.empty}</td></tr>`;
  litRender(content, tbody);
}

// ---- Installed applications ----------------------------------------------
let allApps = [];
let appQuery = "";
let appCategory = "all";
let appStatus = "all";
let appSort = { key: "", dir: 1 };
const appSortKeys = {
  name: (a) => displayName(a).toLowerCase(),
  category: (a) => categoryOf(a, "app").toLowerCase(),
  state: appStateRank,
  version: (a) => (a.version_short || a.version || "").toLowerCase(),
  last: lastEventTime,
};
function setAppSort(key) { toggleSort(appSort, key, renderApps); }
function setAppQuery(q) { appQuery = q || ""; renderApps(); saveUIState(); }
function setAppCategory(v) { appCategory = v || "all"; renderApps(); saveUIState(); }
function setAppStatus(v) {
  appStatus = v || "all";
  syncFilterButtons("#app-filters", "af", appStatus);
  renderApps();
  saveUIState();
}
function renderAppFilterCounts() {
  const a = allApps || [];
  renderFilterButtonCounts("#app-filters", {
    all: a.length,
    ok: a.filter((x) => appStateText(x) === "ok").length,
    starting: a.filter((x) => appStateText(x) === "starting").length,
    warning: a.filter((x) => appStateText(x) === "warning").length,
    failed: a.filter((x) => appStateText(x) === "failed").length,
  });
}
function updateAppSortIndicators() {
  updateSortIndicatorsFor("ai", appSort, ".apps-table th.sortable[data-app-sort]", "appSort");
}
function appStateText(a) {
  if (a && a.state === "starting") return "starting";
  const status = String((a && a.status) || "").trim().toLowerCase();
  if (!status || status === "ok") return "ok";
  if (status.startsWith("error:") || status === "not installed" || status === "no binary configured") return "failed";
  return "warning";
}
function appStateRank(a) {
  switch (appStateText(a)) {
    case "ok": return 0;
    case "starting": return 1;
    case "warning": return 2;
    case "failed": return 3;
    default: return 4;
  }
}
function appStatusLabel(a) {
  switch (appStateText(a)) {
    case "ok": return "Ok";
    case "starting": return "Starting";
    case "warning": return "Warning";
    case "failed": return "Failed";
    default: return "Unknown";
  }
}
function appStatusCell(a) {
  const state = appStateText(a);
  const detail = (a && a.status && a.status !== "ok") ? a.status : appStatusLabel(a);
  return tpl`<td class="status-cell status-${state}" title="${detail}">${stateBadgeLabel(state, appStatusLabel(a))}</td>`;
}
function appMatches(a) {
  const category = categoryOf(a, "app");
  if (appCategory !== "all" && category !== appCategory) return false;
  switch (appStatus) {
    case "ok":
    case "starting":
    case "warning":
    case "failed":
      if (appStateText(a) !== appStatus) return false;
      break;
    default:
      break;
  }
  if (!appQuery) return true;
  const q = appQuery.toLowerCase();
  return displayName(a).toLowerCase().includes(q)
    || (a.name || "").toLowerCase().includes(q)
    || (a.display_name || "").toLowerCase().includes(q)
    || category.toLowerCase().includes(q)
    || appStateText(a).includes(q)
    || (a.status || "").toLowerCase().includes(q)
    || (a.version || "").toLowerCase().includes(q)
    || (a.user || "").toLowerCase().includes(q)
    || (a.group || "").toLowerCase().includes(q);
}

function setAppGrouped(v) {
  appGrouped = !!v;
  renderApps();
  saveUIState();
}

function toggleAllAppGroups() {
  const list = (allApps || []).filter(appMatches);
  const categories = sortedCategories(list, "app");
  const allCollapsed = categories.length > 0 && categories.every((category) => appCollapsedGroups.has(category));
  if (allCollapsed) {
    categories.forEach((category) => appCollapsedGroups.delete(category));
  } else {
    categories.forEach((category) => appCollapsedGroups.add(category));
  }
  renderApps();
  saveUIState();
}

function toggleCategoryGroup(panel, category) {
  if (!category) return;
  if (panel === "svc") {
    if (svcCollapsedGroups.has(category)) svcCollapsedGroups.delete(category);
    else svcCollapsedGroups.add(category);
    renderServices();
    saveUIState();
    return;
  }
  if (panel === "app") {
    if (appCollapsedGroups.has(category)) appCollapsedGroups.delete(category);
    else appCollapsedGroups.add(category);
    renderApps();
    saveUIState();
  }
}

// renderApps lists the installed applications below the services table. The
// version column shows the short version; expanding a row reveals the full
// version string, binary location, permissions, user and group (all already in
// hand, so no extra request is needed).
function renderApps(apps) {
  if (apps) allApps = apps;
  const section = $("#apps-section");
  const tbody = $("#app-rows");
  const cnt = $("#apps-count");
  const filterCount = $("#app-count");
  if (!section || !tbody) return;
  const total = (allApps || []).length;
  if (total === 0) {
    setPanelVisible(section, false);
    if (cnt) cnt.textContent = "";
    if (filterCount) filterCount.textContent = "";
    updateSectionNav();
    return;
  }
  setPanelVisible(section, true);
  if (cnt) cnt.textContent = `(${total})`;
  appCategory = syncCategorySelect("#app-category", allApps || [], "app", appCategory);
  renderAppFilterCounts();
  const list = (allApps || []).filter(appMatches);
  if (appSort.key && appSortKeys[appSort.key]) {
    sortedBy(list, appSort, appSortKeys, "name");
  }
  updateAppSortIndicators();
  const visibleCategories = sortedCategories(list, "app");
  appCollapsedGroups.forEach((category) => { if (!visibleCategories.includes(category)) appCollapsedGroups.delete(category); });
  updateGroupButtons("app", appGrouped, visibleCategories, appCollapsedGroups, "applications");
  if (filterCount) filterCount.textContent = (appQuery || appCategory !== "all" || appStatus !== "all") ? `showing ${list.length} of ${total}` : "";
  const appRow = (a) => {
    const category = categoryOf(a, "app");
    const state = appStateText(a);
    const rowClass = state === "failed" ? "row-failing" : (state === "warning" ? "row-warning" : "");
    const label = displayName(a);
    const key = "app:" + a.name;
    const open = expanded.has(key);
    const chev = tpl`<span class="exp" aria-hidden="true">${open ? '▾' : '▸'}</span>`;
    const ver = a.version_short || a.version || "—";
    const row = tpl`<tr id="app-row-${a.name}" class="clickable ${rowClass}" data-exp-key="${key}">
      <td>${chev}<button type="button" class="row-toggle" data-exp-toggle="${key}" aria-expanded="${open}" aria-controls="${open ? "exp-" + key : nothing}" aria-label="${expandToggleAriaLabel(label, open, "application details")}">${label}</button></td>
      <td>${categoryBadge(category)}</td>
      ${appStatusCell(a)}
      <td>${ver}</td>
      <td>${lastEventCell(a)}</td>
    </tr>`;
    const expRow = open
      ? tpl`<tr class="exp-row" id="exp-${key}" data-exp="${key}"><td colspan="5">${renderAppExpansion(a)}</td></tr>`
      : null;
    return expRow ? [row, expRow] : [row];
  };
  const content = list.length
    ? (appGrouped
      ? renderGroupedRows(list, appCollapsedGroups, "app", "app", 5, appRow, appSort.key === "category" ? appSort.dir : 1)
      : list.flatMap(appRow))
    : tpl`<tr><td colspan="5" class="muted">No applications match the filter.</td></tr>`;
  litRender(content, tbody);
  // Fill the recent-events table of each expanded app (async), mirroring how
  // expanded services load their events.
  (allApps || []).forEach((a) => { if (expanded.has("app:" + a.name)) loadAppEvents(a.name); });
  applyHash();
  updateSectionNav();
}

// renderAppExpansion shows one application's full version, binary location and
// permissions, reusing the watch-grid layout.
function renderAppExpansion(a) {
  const bin = a.binary ? tpl`<code>${a.binary}</code>` : tpl`<span class="muted">unknown</span>`;
  const perm = a.permissions ? tpl`<code>${a.permissions}</code>` : tpl`<span class="muted">—</span>`;
  const usr = a.user ? a.user : tpl`<span class="muted">—</span>`;
  const grp = a.group ? a.group : tpl`<span class="muted">—</span>`;
  const source = a.version_source
    ? tpl`<code>${a.version_source}</code>`
    : (a.version ? tpl`<span class="muted">local</span>` : tpl`<span class="muted">—</span>`);
  const category = categoryOf(a, "app");
  const sla = renderSLAWindows(a.sla, true);
  const st = appStateText(a);
  const statusCls = st === "failed" ? "lvl-error" : (st === "warning" ? "lvl-warning" : "");
  const statusHTML = a.status
    ? tpl`<span class="${statusCls}">${a.status}</span>`
    : "—";
  const eventsId = detailDomId(a.name, "app-events");
  return tpl`<div class="watch-grid">
    <div><span class="muted">Version</span><br>${a.version || "—"}</div>
    <div><span class="muted">Version source</span><br>${source}</div>
    <div><span class="muted">Category</span><br>${category}</div>
    <div><span class="muted">Location</span><br>${bin}</div>
    <div><span class="muted">Permissions</span><br>${perm}</div>
    <div><span class="muted">User</span><br>${usr}</div>
    <div><span class="muted">Group</span><br>${grp}</div>
    <div><span class="muted">Status</span><br>${statusHTML}</div>
    <div class="app-sla"><span class="muted">SLA</span><br>${sla}</div>
  </div>
  <h3 class="expansion-heading">Recent events</h3>
  <table class="events">
    <caption class="visually-hidden">Recent application events</caption>
    <thead><tr><th scope="col">Time</th><th scope="col">Kind</th><th scope="col">Message</th></tr></thead>
    <tbody id="${eventsId}"></tbody></table>`;
}

// loadAppEvents fills an expanded application's "Recent events" table with its
// monitoring history (errors/recoveries), mirroring loadServiceEvents.
async function loadAppEvents(name) {
  const target = document.getElementById(detailDomId(name, "app-events"));
  if (!target) return;
  renderEventsLoading(target);
  try {
    const res = await fetch(`api/applications/${encodeURIComponent(name)}/events?limit=50`);
    if (!res.ok) throw new Error("HTTP " + res.status);
    litRender(eventRows(await res.json(), false), target);
  } catch (e) {
    litRender(tpl`<tr><td colspan="3" class="muted">Failed to load events: ${e.message}</td></tr>`, target);
  }
}

// renderWatchExpansion shows a host watch's config summary and its recent
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
  const category = categoryOf(w, "watch");
  const cfg = tpl`<div class="watch-grid">
    <div><span class="muted">Type</span><br><b>${w.check_type || ""}</b></div>
    <div><span class="muted">Category</span><br>${categoryBadge(category)}</div>
    <div><span class="muted">Interval</span><br><b>${w.interval || ""}</b></div>
    <div><span class="muted">Fires</span><br><b>${polarity}</b></div>
    <div><span class="muted">State</span><br>${watchStateCell(w)}</div>
    <div><span class="muted">Monitor flag</span><br><code>${mode}</code></div>
    <div><span class="muted">Hook</span><br>${hook}</div>
    <div><span class="muted">Notifies</span><br>${notifiers}</div>
    <div><span class="muted">Dry run</span><br><b>${w.dry_run ? "yes" : "no"}</b></div>
  </div>`;
  const live = tpl`${renderStorageWatch(w.storage)}${renderMeterWatch(w.meter)}${renderWatchReadings(w.readings)}`;
  const conditions = renderConditionRows(w.conditions || []);
  if (!events || !events.length) return tpl`${cfg}${live}${conditions}<div class="muted">No recent activity.</div>`;
  const rows = events.slice(0, 50).map((e) => {
    const detail = [e.action, e.status].filter(Boolean).join(" ");
    return tpl`<tr>
      <td class="t">${fmtTime(e.time)}</td>
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
  if (state === "error") return "state-failed";
  if (mounted || state === "active") return "state-running";
  return "state-stopped";
}

function mountStateText(m) {
  const state = String((m && m.state) || "").toLowerCase();
  if (state === "error") return "error";
  if ((m && m.mounted) || state === "active") return "active";
  return "inactive";
}

function mountStateRank(m) {
  switch (mountStateText(m)) {
    case "active": return 0;
    case "inactive": return 1;
    case "error": return 2;
    default: return 3;
  }
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

const mountSortKeys = {
  name: (m) => displayName(m).toLowerCase(),
  category: (m) => categoryOf(m, "storage").toLowerCase(),
  path: (m) => (m.path || "").toLowerCase(),
  mounted: (m) => mountStateRank(m),
  refcount: (m) => numericSortValue(m && m.refcount),
  processes: (m) => mountBlockers(m).length,
  users: (m) => mountUserNames(m).length,
  state: (m) => mountStateRank(m),
};

function setMountSort(key) { toggleSort(mountSort, key, renderMounts); }
function setMountQuery(v) { mountQuery = (v || "").trim().toLowerCase(); renderMounts(); saveUIState(); }
function setMountCategory(v) { mountCategory = v || "all"; renderMounts(); saveUIState(); }
function setMountStatus(v) {
  mountStatus = normalizeMountStatusFilter(v);
  syncFilterButtons("#mount-filters", "mf", mountStatus);
  renderMounts();
  saveUIState();
}

function mountMatches(m) {
  const category = categoryOf(m, "storage");
  if (mountCategory !== "all" && category !== mountCategory) return false;
  if (mountStatus !== "all" && mountStateText(m) !== mountStatus) return false;
  if (!mountQuery) return true;
  const hay = [
    displayName(m), m.name || "", m.display_name || "", category, m.path || "",
    mountStateText(m), ...mountBlockers(m).map(mountProcessLabel), ...mountUserNames(m),
  ].join(" ").toLowerCase();
  return hay.includes(mountQuery);
}

function renderMountFilterCounts() {
  const mounts = allMounts || [];
  renderFilterButtonCounts("#mount-filters", {
    all: mounts.length,
    active: mounts.filter((m) => mountStateText(m) === "active").length,
    inactive: mounts.filter((m) => mountStateText(m) === "inactive").length,
  });
}

function syncMountCategorySelect() {
  const select = $("#mount-category");
  if (!select) return mountCategory || "all";
  const categories = sortedCategories(allMounts || [], "storage");
  const counts = categoryCounts(allMounts || [], "storage");
  const visible = categories.length > 1;
  select.classList.toggle("panel-hidden", !visible);
  select.disabled = !visible;
  const next = visible && mountCategory !== "all" && categories.includes(mountCategory) ? mountCategory : "all";
  select.innerHTML = `<option value="all">all groups</option>` + categories.map((category) =>
    `<option value="${esc(category)}">${esc(category)} (${counts.get(category) || 0})</option>`
  ).join("");
  select.value = next;
  return next;
}

function updateMountSortIndicators() {
  updateSortIndicatorsFor("mi", mountSort, ".mount-table th.sortable[data-mount-sort]", "mountSort");
}

function renderMounts(mounts) {
  if (mounts) allMounts = mounts;
  const section = $("#mounts-section");
  const tbody = $("#mount-rows");
  const cnt = $("#mounts-count");
  const filterCount = $("#mount-filter-count");
  if (!section || !tbody) return;
  const total = (allMounts || []).length;
  if (total === 0) {
    setPanelVisible(section, false);
    if (cnt) cnt.textContent = "";
    if (filterCount) filterCount.textContent = "";
    updateSectionNav();
    return;
  }
  setPanelVisible(section, true);
  if (cnt) cnt.textContent = `(${total})`;
  mountCategory = syncMountCategorySelect();
  renderMountFilterCounts();
  const list = (allMounts || []).filter(mountMatches);
  if (mountSort.key && mountSortKeys[mountSort.key]) {
    sortedBy(list, mountSort, mountSortKeys, "name");
  }
  updateMountSortIndicators();
  if (filterCount) filterCount.textContent = (mountQuery || mountStatus !== "all" || mountCategory !== "all") ? `showing ${list.length} of ${total}` : "";
  const rows = list.map((m) => {
    const label = esc(m.display_name || m.name);
    const category = categoryOf(m, "storage");
    const mounted = !!m.mounted;
    const state = m.state || (mounted ? "active" : "inactive");
    const detail = m.message ? ` title="${esc(m.message)}"` : "";
    const refcount = m.refcounted === false ? '<span class="muted">off</span>' : String(Number(m.refcount || 0));
    const name = esc(m.name || "");
    const actions = mountActionButtons(m, mounted);
    return `<tr${detail}>
      <td>${label}</td>
      <td>${mountCategoryCell(category)}</td>
      <td><code>${esc(m.path || "")}</code></td>
      <td>${mounted ? '<span class="ok">yes</span>' : '<span class="muted">no</span>'}</td>
      <td>${refcount}</td>
      <td class="mount-processes">${mountProcessesCell(m)}</td>
      <td class="mount-users">${mountUsersCell(m)}</td>
      <td><span class="target-state ${mountStateClass(state, mounted)}">${esc(state)}</span></td>
      <td class="actions" data-mount-row="${name}">${actions}</td>
    </tr>`;
  });
  tbody.innerHTML = rows.join("") || `<tr><td colspan="9" class="muted">No mount units match the filter.</td></tr>`;
  updateSectionNav();
}

function mountActionButtons(m, mounted) {
  if (!me.can_act) return '<span class="muted">read-only</span>';
  const name = esc(m.name || "");
  const label = esc(m.display_name || m.name || m.path || "mount");
  if (!mounted) {
    return `<button data-mount="${name}" data-mount-action="mount" aria-label="Mount ${label}">mount</button>`;
  }
  return `<button data-mount="${name}" data-mount-action="umount" aria-label="Unmount ${label}">umount</button>` +
    `<button data-mount="${name}" data-mount-action="alert" aria-label="Alert users blocking ${label}">alert</button>` +
    `<button class="danger-btn" data-mount="${name}" data-mount-action="kill-umount" aria-label="Kill blockers and unmount ${label}">kill+umount</button>`;
}

function renderNotifiers(notifiers) {
  const section = $("#notifiers-section");
  const tbody = $("#notifier-rows");
  const cnt = $("#notifiers-count");
  if (!section || !tbody) return;
  if (!notifiers || notifiers.length === 0) {
    setPanelVisible(section, false);
    if (cnt) cnt.textContent = "";
    updateSectionNav();
    return;
  }
  setPanelVisible(section, true);
  if (cnt) cnt.textContent = `(${notifiers.length})`;
  const rows = notifiers.map((n) => {
    const enabled = n.enabled !== false;
    const state = enabled ? "enabled" : "disabled";
    const cls = enabled ? "state-monitored" : "state-disabled";
    const dest = n.summary ? esc(n.summary) : '<span class="muted">—</span>';
    const used = Number(n.used_by || 0);
    const watches = used ? String(used) : '<span class="muted">—</span>';
    return `<tr><td>${esc(n.name)}</td><td>${esc(n.type)}</td><td class="muted">${dest}</td><td>${watches}</td><td class="${cls}">${state}</td></tr>`;
  });
  tbody.innerHTML = rows.join("") || `<tr><td colspan="5" class="muted">No notifiers.</td></tr>`;
  updateSectionNav();
}

function renderDaemon(info) {
  if (!info) return;
  const set = (id, val) => {
    const el = $(id);
    if (el) el.textContent = val || "—";
  };
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
  set("#engine-max-ops", info.max_parallel_operations);
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

// hostMetricVal finds a single host metric by name and formats its value
// (percent or absolute+unit), or returns null when absent. Used to fold the
// live host readings into the system-status line.
function hostMetricVal(metrics, name) {
  const m = (metrics || []).find((x) => x.name === name);
  if (!m) return null;
  let val;
  if (m.percent != null) val = fmtNum(m.percent, 2) + "%";
  else if (m.absolute != null) { val = fmtNum(m.absolute, 2) + (m.unit ? " " + m.unit : ""); }
  else return null;
  if (!m.ready) val += " (stale)";
  return val;
}

// pctVal formats a percent-type host metric (cpu/mem/swap). A value of exactly
// 0% is dropped from the JSON by omitempty, so a metric that is present but has
// no percent is shown as 0.0% rather than hidden. Returns null only when the
// metric is absent entirely (e.g. no swap device).
function pctVal(metrics, name) {
  const m = (metrics || []).find((x) => x.name === name);
  if (!m) return null;
  const v = fmtNum(m.percent != null ? m.percent : 0, 2) + "%";
  return m.ready === false ? v + " (stale)" : v;
}

// shortDur renders a second count on the shared s/m/h/d ladder ("37s", "5m",
// "3h", "2d"); every age/remaining formatter builds on it.
function shortDur(sec) {
  sec = Math.max(0, Math.floor(Number(sec) || 0));
  if (sec < 60) return sec + "s";
  if (sec < 3600) return Math.floor(sec / 60) + "m";
  if (sec < 86400) return Math.floor(sec / 3600) + "h";
  return Math.floor(sec / 86400) + "d";
}

function fmtSeconds(n) {
  return shortDur(n);
}

function lockName(l) {
  return l.name || "(default)";
}

function lockStateHTML(l) {
  const cls = l.state === "active" ? "bad" : (l.state === "stale" ? "inactive" : "muted");
  return tpl`<span class="${cls}">${l.state || ""}</span>`;
}

function lockTTL(l) {
  if (!l.expires_at) return tpl`<span class="muted">—</span>`;
  if (l.ttl_remaining_seconds > 0) return tpl`<span title="${fmtTime(l.expires_at)}">${fmtSeconds(l.ttl_remaining_seconds)}</span>`;
  return tpl`<span class="muted" title="${fmtTime(l.expires_at)}">expired</span>`;
}

function lockOwner(l) {
  if (!l.owner_pid) return tpl`<span class="muted">none</span>`;
  const cls = l.owner_status === "live" ? "ok" : (l.owner_status === "stale" ? "inactive" : "muted");
  const reason = l.stale_reason ? ` · ${l.stale_reason}` : "";
  return tpl`<span class="${cls}">${l.owner_pid}</span> <span class="muted">${(l.owner_status || "") + reason}</span>`;
}

function lockCreated(l) {
  if (l.created_age_seconds > 0) return tpl`<span title="${fmtTime(l.created_at)}">${fmtSeconds(l.created_age_seconds)} ago</span>`;
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
  if (l.state === "active") return "lock is still active";
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
  const qs = name ? `?name=${encodeURIComponent(name)}` : "";
  try {
    const res = await fetch(`api/locks/${encodeURIComponent(service)}/release${qs}`, {
      method: "POST",
      headers: { "X-Sermo-CSRF": "1" },
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok || body.ok === false) throw new Error(body.message || ("HTTP " + res.status));
    setStatus(`released lock ${label}`, "ok");
    await load();
  } catch (e) {
    setStatus(`release ${label}: ${e.message}`, "err");
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
  const set = (id, v) => { const el = $(id); if (el) el.textContent = v; };

  set("#act-service", sum.service_actions || 0);
  set("#act-hooks", sum.watch_hooks || 0);
  set("#act-notify", sum.watch_notifies || 0);
  set("#act-errors", sum.errors || 0);

  const lastEl = $("#act-last");
  if (lastEl) {
    if (sum.last_event_time) {
      let who = "";
      if (sum.last_event_service) who = `service ${esc(sum.last_event_service)}`;
      if (sum.last_event_watch) who = `watch ${esc(sum.last_event_watch)}`;
      lastEl.innerHTML = `Last: <b>${esc(sum.last_event_kind || "")}</b> ${who} <span class="muted">(${esc(sum.last_event_time)})</span>`;
    } else {
      lastEl.textContent = "No recent events";
    }
  }
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
    case "failed-services": return "services panel, failed filter";
    case "starting-services": return "services panel, starting filter";
    case "collecting-services": return "services panel, collecting filter";
    case "monitored-services": return "services panel, monitored filter";
    case "failed-watches": return "watches panel, failed filter";
    case "starting-watches": return "watches panel, starting filter";
    case "failed-apps": return "applications panel, failed filter";
    case "starting-apps": return "applications panel, starting filter";
    case "locks-section": return "runtime locks panel";
    case "daemon-section": return "daemon panel";
    case "activity-section": return "recent activity panel";
    case "watches-section": return "host watches panel";
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
// passes the same burst snapshot into renderStatus — no extra requests here.
function renderOverview(ctx) {
  const band = $("#overview");
  if (!band) return;
  const { ready, live, mon, ops, locks, hostMetrics } = ctx;
  const svcs = allServices || [];
  const enabled = svcs.filter((s) => s.enabled);
  const failedSvcs = svcs.filter((s) => serviceDisplayState(s) === "failed");
  const startingSvcs = svcs.filter((s) => serviceDisplayState(s) === "starting");
  const collectingSvcs = svcs.filter((s) => serviceDisplayState(s) === "collecting");
  const activeSvcs = enabled.filter((s) => ["started", "collecting", "monitored"].includes(serviceDisplayState(s)));
  const monitoredSvcs = enabled.filter((s) => serviceDisplayState(s) === "monitored");
  const watches = allWatches || [];
  const enabledWatches = watches.filter((w) => w && w.enabled);
  const failedWatches = watches.filter((w) => watchStateText(w) === "failed");
  const startingWatches = watches.filter((w) => watchStateText(w) === "starting");
  const startingApps = (allApps || []).filter((a) => appStateText(a) === "starting");
  const daemonStarting = ready && ready.status === "starting" && ready.ready === false;
  const activeLocks = (locks || []).filter((l) => l.state === "active");
  const failedApps = (allApps || []).filter((a) => appStateText(a) === "failed");
  const alerts = failedSvcs.length + failedWatches.length + failedApps.length + activeLocks.length;
  const settling = daemonStarting || startingSvcs.length > 0 || startingWatches.length > 0 || startingApps.length > 0;
  const servicesSettlingSub = () => {
    if (startingSvcs.length) return `${startingSvcs.length} starting`;
    const parts = [];
    if (daemonStarting) parts.push("daemon starting");
    if (startingWatches.length) parts.push(`${startingWatches.length} watch starting`);
    if (startingApps.length) parts.push(`${startingApps.length} app starting`);
    return parts.length ? parts.join(" · ") : "";
  };
  const watchesSettlingSub = () => {
    if (startingWatches.length) return `${startingWatches.length} starting`;
    const parts = [];
    if (daemonStarting) parts.push("daemon starting");
    if (startingSvcs.length) parts.push(`${startingSvcs.length} svc starting`);
    if (startingApps.length) parts.push(`${startingApps.length} app starting`);
    return parts.length ? parts.join(" · ") : "";
  };
  const watchesSettling = settling && !failedWatches.length;
  const servicesTarget = failedSvcs.length ? "failed-services"
    : (startingSvcs.length || daemonStarting ? "starting-services"
      : (collectingSvcs.length ? "collecting-services"
        : (startingWatches.length ? "starting-watches"
          : (startingApps.length ? "starting-apps" : "services-section"))));
  const watchesTarget = failedWatches.length ? "failed-watches"
    : (startingWatches.length ? "starting-watches"
      : (startingApps.length && !startingSvcs.length && !daemonStarting ? "starting-apps"
        : (settling ? "starting-services" : "watches-section")));

  const tile = (opts) => tpl`
    <button class="tile ${opts.cls || ""}" data-panel-target="${opts.target || "services-section"}" aria-label="${opts.ariaLabel || opts.label}" aria-describedby="${opts.describedBy || nothing}">
      <span class="t-label">${opts.label}</span>
      <div class="t-value">${opts.value}</div>
      <div class="t-sub">${opts.sub || ""}</div>
      ${opts.extra || nothing}
    </button>`;

  const tiles = [];
  const servicesSub = failedSvcs.length
    ? `${failedSvcs.length} failed`
    : (servicesSettlingSub() || (collectingSvcs.length ? `${collectingSvcs.length} collecting` : (enabled.length === 0 ? "none enabled" : "all active")));
  tiles.push(tile({
    label: "Services active",
    value: tpl`${activeSvcs.length}<small> / ${enabled.length}</small>`,
    cls: failedSvcs.length ? "t-crit" : (collectingSvcs.length ? "t-warn" : (settling ? "" : (enabled.length ? "t-ok" : ""))),
    sub: servicesSub,
    target: servicesTarget,
    ariaLabel: tileAriaLabel("Services active", `${activeSvcs.length} of ${enabled.length}`, servicesSub, servicesTarget),
  }));
  if (watches.length) {
    const watchesSub = failedWatches.length
      ? `${failedWatches.length} firing`
      : (watchesSettlingSub() || "quiet");
    const watchesUp = enabledWatches.length - failedWatches.length;
    tiles.push(tile({
      label: "Watches",
      value: tpl`${watchesUp}<small> / ${enabledWatches.length}</small>`,
      cls: failedWatches.length ? "t-crit" : (watchesSettling ? "" : "t-ok"),
      sub: watchesSub,
      target: watchesTarget,
      ariaLabel: tileAriaLabel("Watches", `${watchesUp} of ${enabledWatches.length}`, watchesSub, watchesTarget),
    }));
  }
  const alertsTarget = alerts
    ? (failedSvcs.length ? "failed-services"
      : (failedWatches.length ? "failed-watches"
        : (failedApps.length ? "failed-apps"
          : (activeLocks.length ? "locks-section" : "services-section"))))
    : "services-section";
  const alertsSub = alerts
    ? [failedSvcs.length && `${failedSvcs.length} svc`, failedWatches.length && `${failedWatches.length} watch`, failedApps.length && `${failedApps.length} app`, activeLocks.length && `${activeLocks.length} lock`].filter(Boolean).join(" · ")
    : "nothing on fire";
  tiles.push(tile({
    label: "Alerts",
    value: String(alerts),
    cls: alerts ? "t-crit" : "t-ok",
    sub: alertsSub,
    target: alertsTarget,
    ariaLabel: tileAriaLabel("Alerts", String(alerts), alertsSub, alertsTarget),
  }));
  const monitoredTarget = collectingSvcs.length && !failedSvcs.length
    ? "collecting-services"
    : (settling && !failedSvcs.length
    ? servicesTarget
    : (monitoredSvcs.length ? "monitored-services" : "services-section"));
  const monitoredSub = collectingSvcs.length
    ? `${collectingSvcs.length} collecting`
    : (settling && !failedSvcs.length ? (servicesSettlingSub() || "settling") : "");
  if (enabled.length || (mon && mon.total != null)) {
    tiles.push(tile({
      label: "Monitored",
      value: tpl`${monitoredSvcs.length}<small> / ${enabled.length}</small>`,
      cls: collectingSvcs.length ? "t-warn" : (settling && !failedSvcs.length ? "" : (enabled.length && monitoredSvcs.length === enabled.length ? "t-ok" : "")),
      sub: monitoredSub,
      target: monitoredTarget,
      ariaLabel: tileAriaLabel("Monitored", `${monitoredSvcs.length} of ${enabled.length}`, monitoredSub, monitoredTarget),
    }));
  }
  if (ops && ops.total) {
    const saturated = (ops.in_use || 0) >= ops.total;
    const opSub = saturated ? "saturated" : "";
    tiles.push(tile({
      label: "Op slots",
      value: tpl`${ops.in_use || 0}<small> / ${ops.total}</small>`,
      cls: saturated ? "t-warn" : "",
      sub: opSub,
      target: "services-section",
      ariaLabel: tileAriaLabel("Op slots", `${ops.in_use || 0} of ${ops.total}`, opSub, "services-section"),
    }));
  }
  const cpu = (hostMetrics || []).find((m) => m.name === "total_cpu");
  const mem = (hostMetrics || []).find((m) => m.name === "total_memory");
  const swap = (hostMetrics || []).find((m) => m.name === "total_swap");
  const load = (hostMetrics || []).find((m) => m.name === "load1");
  // usedFreeSub renders the volume-style "X used · Y free" line for a usage
  // metric carrying its capacity (total bytes).
  const usedFreeSub = (m) => m.total
    ? `${fmtBytes(m.absolute || 0)} used · ${fmtBytes(Math.max(m.total - (m.absolute || 0), 0))} free`
    : "";
  if (cpu) {
    const p = pctClamp(cpu.percent || 0);
    const gaugeId = tileGaugeId("cpu");
    tiles.push(tile({
      label: "Host CPU", value: tpl`${fmtNum(p, 2)}<small>%</small>`, sub: "", extra: usageBar(p, fmtPct(p), gaugeId), target: "daemon-section",
      ariaLabel: tileAriaLabel("Host CPU", fmtPct(p), "", "daemon-section"),
      describedBy: gaugeId,
    }));
  }
  if (mem) {
    const p = pctClamp(mem.percent || 0);
    const memSub = usedFreeSub(mem);
    const gaugeId = tileGaugeId("mem");
    tiles.push(tile({
      label: "Host memory", value: tpl`${fmtNum(p, 2)}<small>%</small>`, sub: memSub, extra: usageBar(p, fmtPct(p), gaugeId), target: "daemon-section",
      ariaLabel: tileAriaLabel("Host memory", fmtPct(p), memSub, "daemon-section"),
      describedBy: gaugeId,
    }));
  }
  if (swap && swap.total) {
    const p = pctClamp(swap.percent || 0);
    const swapSub = usedFreeSub(swap);
    const gaugeId = tileGaugeId("swap");
    tiles.push(tile({
      label: "Host swap", value: tpl`${fmtNum(p, 2)}<small>%</small>`, sub: swapSub, cls: p >= 90 ? "t-crit" : (p >= 70 ? "t-warn" : ""), extra: usageBar(p, fmtPct(p), gaugeId), target: "daemon-section",
      ariaLabel: tileAriaLabel("Host swap", fmtPct(p), swapSub, "daemon-section"),
      describedBy: gaugeId,
    }));
  }
  if (load && load.absolute != null) {
    // load.total carries the logical CPU count and load.percent the saturation
    // (load1/CPUs), so the tile gets the same bar as cpu/mem/swap. >100% means
    // the run queue exceeds the cores.
    const hasCap = load.total > 0;
    const p = hasCap ? pctClamp(load.percent || 0) : 0;
    const loadSub = hasCap ? `${fmtNum(load.total, 0)} CPUs · ${fmtPct(load.percent)}` : (live && fmtUptime(live.uptime_seconds) ? `up ${fmtUptime(live.uptime_seconds)}` : "");
    const gaugeId = hasCap ? tileGaugeId("load") : nothing;
    tiles.push(tile({
      label: "Load 1m",
      value: fmtNum(load.absolute, 2),
      sub: loadSub,
      cls: hasCap ? (p >= 100 ? "t-crit" : (p >= 80 ? "t-warn" : "")) : "",
      extra: hasCap ? usageBar(p, fmtPct(p), gaugeId) : nothing,
      target: "daemon-section",
      ariaLabel: tileAriaLabel("Load 1m", fmtNum(load.absolute, 2), loadSub, "daemon-section"),
      describedBy: gaugeId,
    }));
  }
  litRender(tiles, band);
}

// getJSON fetches and parses one endpoint, returning dflt on any failure (network
// error, non-2xx, bad JSON). It never throws, so one failing endpoint can't take
// down the whole status line — each just degrades to its default for that cycle.
async function getJSON(url, dflt) {
  try {
    const r = await fetch(url);
    return r.ok ? await r.json() : dflt;
  } catch (_) {
    return dflt;
  }
}

// fetchReadyReport loads GET /readyz?verbose. Unlike getJSON, it parses the JSON
// body even when the probe returns 503 (starting / shutting_down), so the header
// status line keeps showing the daemon lifecycle state.
async function fetchReadyReport() {
  try {
    const r = await fetch("readyz?verbose");
    const data = await r.json();
    return (data && typeof data === "object") ? data : {};
  } catch (_) {
    return {};
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
}

function renderStatus(ctx) {
  const bar = $("#statusbar");
  if (!bar) return;
  try {
    const { ready, live, mon, ops, locks, daemon, hostMetrics } = ctx || {};
    latestReady = ready || {};
    liveOpsSlots = ops || liveOpsSlots;
    latestLocks = locks || [];
    latestHostMetrics = hostMetrics || [];

    // System-status line (line 2): host identity + detected backend + OS + live readings.
    const sys = $("#system-status");
    if (sys) {
      const sp = [];
      if (daemon.hostname) sp.push(`host: <b>${esc(daemon.hostname)}</b>`);
      const hostType = hostTypeDisplay(daemon.host_type);
      if (hostType.label) {
        const title = hostType.title ? ` title="${esc(hostType.title)}"` : "";
        sp.push(`type: <b${title}>${esc(hostType.label)}</b>`);
      }
      if (ready.backend) sp.push(`backend: <b>${esc(ready.backend)}</b>`);
      if (daemon.os) sp.push(`OS: <b>${esc(daemon.os)}</b>`);
      // cpu/mem/swap are percent-type: show 0.0% when present-but-zero instead
      // of hiding them (omitempty drops an exact 0 from the JSON). load is an
      // absolute reading, so it keeps the generic formatter.
      const cpu = pctVal(hostMetrics, "total_cpu");
      const mem = pctVal(hostMetrics, "total_memory");
      const swap = pctVal(hostMetrics, "total_swap");
      const load = hostMetricVal(hostMetrics, "load1");
      if (cpu != null) sp.push(`cpu: <b>${esc(cpu)}</b>`);
      if (mem != null) sp.push(`mem: <b>${esc(mem)}</b>`);
      if (swap != null) sp.push(`swap: <b>${esc(swap)}</b>`);
      if (load != null) sp.push(`load: <b>${esc(load)}</b>`);
      setHTMLIfChanged(sys, sp.join(" &middot; "));
    }

    const parts = [];
    parts.push(`services: <b>${ready.services || 0}</b>`);
    parts.push(`watches: <b>${ready.watches || 0}</b>`);
    if (mon.total != null) {
      let monStr = `monitoring: <b>${mon.monitored || 0}/${mon.total || 0}</b>`;
      if (mon.paused > 0) monStr += ` <span class="muted">(${mon.paused} paused)</span>`;
      parts.push(monStr);
    }
    if (ops.total != null) {
      let opsStr = `ops: <b>${ops.in_use || 0}/${ops.total || 0}</b>`;
      if ((ops.in_use || 0) > 0) opsStr += ` <span class="muted">(in use)</span>`;
      parts.push(opsStr);
    }
    if (ops.active_users != null) {
      parts.push(`users: <b>${ops.active_users || 0}</b>`);
    }
    const activeLocks = (locks || []).filter(l => l.state === "active").length;
    if (activeLocks > 0 || (locks || []).length > 0) {
      let lockStr = `locks: <b>${activeLocks}</b>`;
      if (activeLocks < (locks || []).length) lockStr += `/${(locks || []).length}`;
      if (activeLocks > 0) lockStr += ` <span class="muted">(active)</span>`;
      parts.push(lockStr);
    }
    // Host uptime and daemon lifecycle status are always the last two readings,
    // paired so status stays immediately after uptime.
    const hostUp = fmtUptime(daemon.host_uptime_seconds);
    const statusText = ready.status || (ready.ready ? "ok" : "");
    const statusCls = ready.panic ? "status-panic" : (statusText === "starting" ? "status-starting" : (ready.ready ? "ok" : "inactive"));
    const statusLabel = statusText === "starting" && ready.message
      ? `${esc(statusText)} <span class="muted">(${esc(ready.message)})</span>`
      : esc(statusText || "—");
    const tail = [
      `uptime: <b>${esc(hostUp || "—")}</b>`,
      `status: <span class="${statusCls}">${statusLabel}</span>`,
    ];
    parts.push(`<span class="status-tail">${tail.join(" &middot; ")}</span>`);
    setHTMLIfChanged(bar, parts.join(" &middot; "));
    updatePanicView(ready.panic);
    renderOverview({ ready, live, mon, ops, locks, hostMetrics });
    updateSectionNav();

    // Also populate the runtime part of the Daemon info panel
    const set = (id, val) => { const el = $(id); if (el) el.textContent = val || "—"; };
    if (live.started_at) set("#daemon-started", live.started_at);
    set("#daemon-uptime", fmtUptime(live.uptime_seconds));
    if (live.go) set("#daemon-go", live.go);
    if (ready.status) {
      const cls = ready.panic ? "status-panic" : (ready.status === "starting" ? "status-starting" : (ready.ready ? "ok" : "inactive"));
      const el = $("#daemon-ready");
      if (el) {
        el.textContent = ready.status;
        el.className = cls;
      }
    }
    renderAttention();
  } catch (e) {
    bar.textContent = "status unavailable";
  }
}

async function act(name, action) {
  let noCascade = false;
  if ((action === "start" || action === "stop" || action === "restart") && !(await confirmAction(name, action))) return;
  if (action === "start" || action === "stop" || action === "restart") {
    noCascade = confirmNoCascade;
    confirmNoCascade = false;
  }
  setStatus("");
  const tracked = isTrackedOperation(action);
  if (tracked) beginOperation(name, action);
  try {
    const q = noCascade ? "?no_cascade=1" : "";
    const res = await fetch(`api/services/${encodeURIComponent(name)}/${action}${q}`, {
      method: "POST",
      headers: { "X-Sermo-CSRF": "1" },
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok || body.ok === false) {
      throw new Error(body.message || ("HTTP " + res.status));
    }
    if (tracked) finishOperation(name, true, body.message || body.status || "operation completed");
  } catch (e) {
    if (tracked) finishOperation(name, false, e.message);
    setStatus(`${action} ${name}: ${e.message}`, "err");
  }
  load();
}

async function actWatch(name, action) {
  if (action === "expand" && !(await confirmWatchExpand(name))) return;
  setStatus("");
  try {
    const res = await fetch(`api/watches/${encodeURIComponent(name)}/${action}`, {
      method: "POST",
      headers: { "X-Sermo-CSRF": "1" },
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok || body.ok === false) {
      throw new Error(body.message || ("HTTP " + res.status));
    }
  } catch (e) {
    setStatus(`${action} watch ${name}: ${e.message}`, "err");
  }
  load();
}

async function fetchMountBlockers(name) {
  const res = await fetch(`api/mounts/${encodeURIComponent(name)}/blockers`, {
    method: "POST",
    headers: { "X-Sermo-CSRF": "1" },
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok || body.ok === false) {
    throw new Error(body.message || ("HTTP " + res.status));
  }
  return body;
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

async function confirmMountUnmount(name) {
  const info = await fetchMountBlockers(name);
  const blockers = info.blockers || [];
  const message = blockers.length
    ? `Mount "${name}" is currently used by:\n${mountBlockerSummary(blockers)}\n\nThis will try a normal unmount only. Use alert first to message users, or kill+umount for policy-gated signal escalation.`
    : `Unmount "${name}"?`;
  return promptConfirm({
    title: `Unmount ${name}?`,
    message,
    okLabel: "umount",
    danger: true,
  });
}

async function confirmMountKillUnmount(name) {
  const info = await fetchMountBlockers(name);
  const blockers = info.blockers || [];
  if (!blockers.length) {
    return promptConfirm({
      title: `Unmount ${name}?`,
      message: `No blocking processes are using "${name}" right now. Unmount normally?`,
      okLabel: "umount",
      danger: true,
    });
  }
  if (!info.can_kill) {
    setStatus(`kill+umount ${name}: no current blocker is killable by this mount policy`, "warn");
    return false;
  }
  return promptConfirm({
    title: `Kill blockers and unmount ${name}?`,
    message: `Sermo will send TERM/KILL only to blockers allowed by stop_policy.kill_only_if, then retry umount.\n\n${mountBlockerSummary(blockers)}`,
    okLabel: "kill+umount",
    danger: true,
  });
}

async function confirmMountAlert(name) {
  const info = await fetchMountBlockers(name);
  const blockers = info.blockers || [];
  if (!blockers.length) {
    setStatus(`alert ${name}: no blocking processes found`, "warn");
    return false;
  }
  if (!info.can_alert) {
    setStatus(`alert ${name}: blockers have no resolved login user`, "warn");
    return false;
  }
  return promptConfirm({
    title: `Alert users for ${name}?`,
    message: `Send a console message to users currently blocking "${name}"?\n\n${mountBlockerSummary(blockers)}`,
    okLabel: "alert",
    danger: false,
  });
}

async function actMount(name, action) {
  if (!name) return;
  let postAction = action;
  let query = "";
  try {
    if (action === "umount" && !(await confirmMountUnmount(name))) return;
    if (action === "kill-umount") {
      if (!(await confirmMountKillUnmount(name))) return;
      postAction = "umount";
      query = "?kill=1";
    }
    if (action === "alert" && !(await confirmMountAlert(name))) return;
  } catch (e) {
    setStatus(`${action} ${name}: ${e.message}`, "err");
    return;
  }

  setStatus("");
  try {
    const res = await fetch(`api/mounts/${encodeURIComponent(name)}/${postAction}${query}`, {
      method: "POST",
      headers: { "X-Sermo-CSRF": "1" },
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok || body.ok === false) {
      const blockers = body.blockers && body.blockers.length ? `; blockers: ${mountBlockerSummary(body.blockers)}` : "";
      throw new Error((body.message || ("HTTP " + res.status)) + blockers);
    }
    setStatus(`${action} ${name}: ${body.message || "ok"}`, "ok");
  } catch (e) {
    setStatus(`${action} ${name}: ${e.message}`, "err");
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
    okLabel: "expand",
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
  if (!["start", "stop", "restart"].includes(action)) return "preflight not available for this action";
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
      danger: action === "stop" || action === "restart",
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
    const [detailRes, eventRes] = await Promise.all([
      fetch(`api/services/${encodeURIComponent(name)}`),
      fetch(`api/services/${encodeURIComponent(name)}/events?limit=1`),
    ]);
    if (!detailRes.ok) throw new Error("HTTP " + detailRes.status);
    confirmCtx.detail = await detailRes.json();
    if (eventRes.ok) {
      const events = await eventRes.json();
      confirmCtx.lastEvent = (events || [])[0] || null;
    }
    syncConfirmPreflightButton(action);
    const alsoApply = (confirmCtx.detail?.also_apply || []);
    const showCascade = alsoApply.length > 0 && (action === "start" || action === "stop" || action === "restart");
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
  if (dlg) dlg.addEventListener("close", () => { if (confirmResolve) closeActionConfirm(false); });
})();

function renderActionConfirm() {
  const ctx = confirmCtx || {};
  const d = ctx.detail || {};
  const activeLocks = (d.locks || []).filter((l) => l.state === "active");
  const failingChecks = (d.checks || []).filter((c) => c.ran && !c.ok && !c.optional);
  const procWarnings = d.process_warnings || [];
  const noResidentProcess = !!d.no_resident_process;
  const ev = ctx.lastEvent;
  const pre = ctx.preflight;
  const preState = ["start", "stop", "restart"].includes(ctx.action)
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
    ? tpl`${fmtTime(ev.time)} · <span class="kind kind-${ev.kind}">${ev.kind || ""}</span> ${[ev.action, ev.status].filter(Boolean).join(" ")} <span class="muted">${ev.message || ""}</span>`
    : tpl`<span class="muted">none recorded</span>`;
  const preRows = pre ? tpl`<div class="confirm-preflight-block">${preflightRows(pre.checks || [])}</div>` : nothing;
  const warning = ctx.action === "restart"
    ? "A safe restart stops the unit, verifies residual processes, then starts only if the stop phase is clean."
    : ctx.action === "start"
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
  if (!confirmCtx) return;
  syncConfirmPreflightButton(confirmCtx.action, { running: true });
  $("#confirm-preflight-btn").textContent = "running…";
  try {
    const res = await fetch(`api/services/${encodeURIComponent(confirmCtx.name)}/preflight`, {
      method: "POST",
      headers: { "X-Sermo-CSRF": "1" },
    });
    if (!res.ok) throw new Error("HTTP " + res.status);
    confirmCtx.preflight = await res.json();
    renderActionConfirm();
  } catch (e) {
    confirmCtx.preflight = { ok: false, checks: [{ name: "preflight", ok: false, message: e.message }] };
    renderActionConfirm();
  } finally {
    $("#confirm-preflight-btn").textContent = "run preflight";
    syncConfirmPreflightButton(confirmCtx.action);
  }
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
    const res = await fetch(`api/services/${encodeURIComponent(name)}/preflight`, {
      method: "POST",
      headers: { "X-Sermo-CSRF": "1" },
    });
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

async function loadServiceEvents(name) {
  const target = document.getElementById(detailDomId(name, "events"));
  if (!target) return;
  renderEventsLoading(target);
  try {
    const res = await fetch(`api/services/${encodeURIComponent(name)}/events?limit=50`);
    if (!res.ok) throw new Error("HTTP " + res.status);
    litRender(eventRows(await res.json(), false), target);
  } catch (e) {
    litRender(tpl`<tr><td colspan="3" class="muted">Failed to load events: ${e.message}</td></tr>`, target);
  }
}

const windowMs = { "1h": 36e5, "24h": 864e5, "168h": 6048e5, "720h": 2592e6, "8760h": 3.1536e10 };

// Latency graph state: the selected measured check and its window.
let metricCheck = "";
const metricTypes = ["tcp", "http", "ports", "service"];
const metricWins = [["1h", "1h"], ["24h", "24h"], ["7d", "168h"], ["30d", "720h"], ["1y", "8760h"]];

function setMetricCheck(name, service) {
  metricCheck = name;
  if (service) syncMetricCheckButtons(service, name);
  const key = service ? "svc:" + service : "";
  const detail = key ? expDetailCache[key] : null;
  if (detail) loadMetrics(service, serviceMeasuredChecks(detail));
  else if (service) loadExpansionFor(key);
  else refreshExpandedServiceDetails();
}
function setMetricWin(win) {
  metricWindow = win;
  saveUIState();
  syncWindowButtons("setMetricWin", metricWindow);
  refreshExpandedServiceDetails();
}
function setDaemonMetricWin(win) {
  daemonMetricWindow = win;
  saveUIState();
  syncWindowButtons("setDaemonMetricWin", daemonMetricWindow);
  loadDaemonMetrics();
}

function metricCheckButtons(serviceName, measured, selected) {
  const btns = measured.map((c) =>
    tpl`<button data-metric-service="${serviceName}" data-metric-check="${c.name}" aria-pressed=${c.name === selected ? "true" : "false"} class="${c.name === selected ? "win-btn-active" : nothing}">${c.name}</button> `);
  return tpl`<span role="group" aria-label="Latency check">${btns}</span>`;
}

function syncMetricCheckButtons(serviceName, selected) {
  document.querySelectorAll("[data-metric-check][data-metric-service]").forEach((btn) => {
    if (btn.dataset.metricService !== serviceName) return;
    const active = btn.dataset.metricCheck === selected;
    btn.classList.toggle("win-btn-active", active);
    btn.setAttribute("aria-pressed", active ? "true" : "false");
  });
}

function winButtons(list, selected, fn, groupLabel) {
  const btns = list.map(([label, val]) =>
    tpl`<button data-window-kind="${fn}" data-window-value="${val}" aria-pressed=${val === selected ? "true" : "false"} class="${val === selected ? "win-btn-active" : nothing}">${label}</button> `);
  return tpl`<span role="group" aria-label="${groupLabel || "Time window"}">${btns}</span>`;
}

function syncWindowButtons(kind, selected) {
  document.querySelectorAll("[data-window-kind][data-window-value]").forEach((btn) => {
    if (btn.dataset.windowKind !== kind) return;
    const active = btn.dataset.windowValue === selected;
    btn.classList.toggle("win-btn-active", active);
    btn.setAttribute("aria-pressed", active ? "true" : "false");
  });
}

async function loadMetrics(name, measured) {
  const check = selectedMetricCheck(measured || []);
  if (!check) return;
  const summary = document.getElementById(detailDomId(name, "lat-summary"));
  const chart = document.getElementById(detailDomId(name, "lat-chart"));
  if (!summary || !chart) return;
  try {
    const res = await fetch(`api/services/${encodeURIComponent(name)}/metrics?check=${encodeURIComponent(check)}&since=${metricWindow}`);
    if (!res.ok) throw new Error("HTTP " + res.status);
    const body = await res.json();
    const s = body.summary || {};
    summary.innerHTML = s.count
      ? `avg <b>${fmtNum(s.avg, 2)}</b> ms &middot; min ${fmtNum(s.min, 2)} &middot; max ${fmtNum(s.max, 2)}`
      : '<span class="muted">No latency data yet for this window.</span>';
    chart.innerHTML = drawMetricChart(body.points || [], body.unit || "ms", metricWindow, "Service latency metric chart");
  } catch (e) {
    chart.textContent = "Failed to load latency: " + e.message;
  }
}

async function loadServiceRuntimeMetrics(name) {
  const ids = ["cpu", "memory", "io"];
  const setAll = (msg) => ids.forEach((id) => {
    const summary = document.getElementById(detailDomId(name, `runtime-${id}-summary`));
    const chart = document.getElementById(detailDomId(name, `runtime-${id}-chart`));
    if (summary) summary.innerHTML = `<span class="muted">${esc(msg)}</span>`;
    if (chart) chart.innerHTML = "";
  });
  try {
    const res = await fetch(`api/services/${encodeURIComponent(name)}/runtime?since=${metricWindow}`);
    if (!res.ok) throw new Error("HTTP " + res.status);
    const body = await res.json();
    renderServiceRuntimeMetric(name, "cpu", body.cpu, "CPU", "%");
    renderServiceRuntimeMetric(name, "memory", body.memory, "memory", "bytes");
    renderServiceRuntimeMetric(name, "io", body.io, "IO", "B/s");
  } catch (e) {
    setAll("Failed to load runtime metrics: " + e.message);
  }
}

function renderServiceRuntimeMetric(name, suffix, series, label, fallbackUnit) {
  const summary = document.getElementById(detailDomId(name, `runtime-${suffix}-summary`));
  const chart = document.getElementById(detailDomId(name, `runtime-${suffix}-chart`));
  const unit = (series && series.unit) || fallbackUnit || "";
  if (summary) summary.innerHTML = daemonMetricSummary(series, label);
  if (chart) chart.innerHTML = drawMetricChart((series || {}).points || [], unit, metricWindow, `${label} runtime metric chart`);
}

async function loadDaemonMetrics() {
  try {
    const body = await getJSON(`api/daemon/metrics?since=${daemonMetricWindow}`, null);
    if (body) renderDaemonMetrics(body);
  } catch (_) { /* getJSON already degrades */ }
}

function renderDaemonMetrics(body) {
  const c = (body && body.current) || {};
  const setText = (id, val) => {
    const el = $(id);
    if (el) el.textContent = (val === 0 || val) ? String(val) : "—";
  };
  setText("#daemon-pid", c.pid);
  setText("#daemon-fds", c.fds);
  setText("#daemon-threads", c.threads);
  setText("#daemon-cpu-live", c.cpu_ready ? `${fmtNum(c.cpu || 0, 2)}%` : "measuring");
  const mem = c.rss ? fmtBytes(c.rss) : "";
  const memPct = (c.memory_percent === 0 || c.memory_percent) ? ` (${fmtNum(c.memory_percent, 2)}%)` : "";
  setText("#daemon-memory-live", mem ? mem + memPct : "");
  setText("#daemon-io-live", c.io_ready ? `${fmtBytes(c.io || 0)}/s` : "measuring");

  const win = $("#daemon-metric-windows");
  if (win) litRender(winButtons(metricWins, daemonMetricWindow, "setDaemonMetricWin", "Daemon metrics time window"), win);
  const summary = $("#daemon-metric-summary");
  if (summary) {
    summary.innerHTML = [
      daemonMetricSummary(body.cpu, "CPU"),
      daemonMetricSummary(body.memory, "memory"),
      daemonMetricSummary(body.io, "IO"),
    ].join(" &middot; ");
  }
  const cpu = $("#daemon-cpu-chart");
  if (cpu) cpu.innerHTML = drawMetricChart((body.cpu || {}).points || [], (body.cpu || {}).unit || "%", daemonMetricWindow, "Daemon CPU metric chart");
  const memory = $("#daemon-memory-chart");
  if (memory) memory.innerHTML = drawMetricChart((body.memory || {}).points || [], (body.memory || {}).unit || "bytes", daemonMetricWindow, "Daemon memory metric chart");
  const io = $("#daemon-io-chart");
  if (io) io.innerHTML = drawMetricChart((body.io || {}).points || [], (body.io || {}).unit || "B/s", daemonMetricWindow, "Daemon IO metric chart");
}

function daemonMetricSummary(series, label) {
  const s = (series && series.summary) || {};
  const unit = (series && series.unit) || "";
  if (!s.count) return `${esc(label)} <span class="muted">no data</span>`;
  return `${esc(label)} avg <b>${esc(fmtMetricValue(s.avg, unit))}</b>`;
}

const chartDataTableMaxRows = 30;

function metricChartDataTable(pts, unit, startMs, span, cols) {
  if (!pts.length) return "";
  const shown = pts.slice(-chartDataTableMaxRows);
  const rows = shown.map((o) => {
    const t = new Date(startMs + (o.i + 0.5) * (span / cols));
    const b = o.b;
    return `<tr><td>${esc(t.toLocaleString())}</td><td>${esc(fmtMetricValue(b.sum / b.n, unit))}</td><td>${esc(fmtMetricValue(b.min, unit))}</td><td>${esc(fmtMetricValue(b.max, unit))}</td></tr>`;
  }).join("");
  return `<table class="chart-data visually-hidden"><caption>Chart data</caption><thead><tr><th scope="col">Time</th><th scope="col">Avg</th><th scope="col">Min</th><th scope="col">Max</th></tr></thead><tbody>${rows}</tbody></table>`;
}

function slaChartDataTable(observed) {
  if (!observed.length) return "";
  const shown = observed.slice(-chartDataTableMaxRows);
  const rows = shown.map((o) => {
    const up = Number(o.p.up || 0);
    const total = Number(o.p.total || 0);
    return `<tr><td>${esc(fmtTime(new Date(o.t).toISOString()))}</td><td>${esc(fmtNum(o.pct, 2))}%</td><td>${up}/${total}</td></tr>`;
  }).join("");
  return `<table class="chart-data visually-hidden"><caption>SLA chart data</caption><thead><tr><th scope="col">Time</th><th scope="col">SLA</th><th scope="col">Up/Total</th></tr></thead><tbody>${rows}</tbody></table>`;
}

function drawMetricChart(points, unit, win, label) {
  unit = unit || "ms";
  const W = 640, H = 160, pad = 34, cols = 120;
  const span = windowMs[win || metricWindow] || 864e5;
  const { buckets, startMs } = bucketize(points, span, cols,
    () => ({ n: 0, sum: 0, min: Infinity, max: -Infinity }),
    (b, p) => {
      b.n += p.n; b.sum += p.avg * p.n;
      b.min = Math.min(b.min, p.min); b.max = Math.max(b.max, p.max);
    });
  let maxV = 0;
  buckets.forEach((b) => { if (b.n) maxV = Math.max(maxV, b.max); });
  if (maxV <= 0) return '<span class="muted">No data yet for this window.</span>';
  const x = (i) => pad + (i + 0.5) * ((W - 2 * pad) / cols);
  const y = (v) => H - pad - (v / maxV) * (H - 2 * pad);
  const pts = buckets.map((b, i) => ({ i, b })).filter((o) => o.b.n > 0);
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
    <text x="${pad}" y="${H - 6}" font-size="10" fill="#888">${new Date(startMs).toLocaleString()}</text>
    <text x="${W - pad}" y="${H - 6}" font-size="10" fill="#888" text-anchor="end">now</text>`;
  // Transparent per-bucket strips carry a native <title> tooltip (value + time).
  const bw = (W - 2 * pad) / cols;
  const hover = pts.map((o) => {
    const t = new Date(startMs + (o.i + 0.5) * (span / cols));
    const tip = `${t.toLocaleString()}\navg ${fmtMetricValue(o.b.sum / o.b.n, unit)} · min ${fmtMetricValue(o.b.min, unit)} · max ${fmtMetricValue(o.b.max, unit)}`;
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

function fmtMetricValue(v, unit) {
  const n = Number(v || 0);
  switch (unit) {
    case "bytes":
      return fmtBytes(n);
    case "B/s":
      return fmtBytes(n) + "/s";
    case "%":
      return fmtNum(n, 2) + "%";
    case "ms":
      return fmtNum(n, 2) + "ms";
    default:
      return fmtNum(n, 2) + (unit || "");
  }
}

function fmtTime(t) {
  const d = new Date(t);
  return isNaN(d) ? (t || "") : d.toLocaleString();
}

function fmtRemain(until) {
  const d = new Date(until);
  if (isNaN(d)) return "";
  const sec = Math.floor((d - Date.now()) / 1000);
  if (sec <= 0) return "elapsed";
  if (sec < 3600) return shortDur(sec) + " remaining";
  return Math.floor(sec / 3600) + "h remaining · until " + fmtTime(until);
}

function fmtUntilShort(until) {
  const d = new Date(until);
  if (isNaN(d)) return "";
  const sec = Math.floor((d - Date.now()) / 1000);
  if (sec <= 0) return "now";
  if (sec < 86400) return "in " + shortDur(sec);
  return d.toLocaleDateString();
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
    if (r.reason === "cooldown") {
      const rem = r.cooldown_until ? fmtRemain(r.cooldown_until) : "";
      parts.push(tpl`<span class="inactive">cooldown</span>${rem ? " · " + rem : nothing}`);
      if (r.effective_cooldown) parts.push(tpl`<span class="muted">effective ${r.effective_cooldown}</span>`);
    } else if (r.reason === "rate limit") {
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

function fmtAge(t) {
  const d = new Date(t);
  if (isNaN(d)) return "";
  const sec = Math.floor((Date.now() - d) / 1000);
  if (sec < 0) return "just now";
  if (sec < 86400) return shortDur(sec) + " ago";
  return fmtTime(t);
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

function syncCategorySelect(id, items, fallback, selected) {
  const select = $(id);
  if (!select) return selected || "all";
  const categories = sortedCategories(items, fallback);
  const next = selected !== "all" && categories.includes(selected) ? selected : "all";
  const counts = categoryCounts(items, fallback);
  select.innerHTML = `<option value="all">all categories</option>` + categories.map((category) =>
    `<option value="${esc(category)}">${esc(category)} (${counts.get(category) || 0})</option>`
  ).join("");
  select.value = next;
  return next;
}

function groupedPanelId(panel, items) {
  const first = items && items[0];
  if (!first || !first.name) return nothing;
  return `${panel}-row-${first.name}`;
}

function renderGroupedRows(list, collapsedGroups, panel, fallback, colspan, renderRow, groupDir) {
  const groups = new Map();
  list.forEach((item) => {
    const category = categoryOf(item, fallback);
    if (!groups.has(category)) groups.set(category, []);
    groups.get(category).push(item);
  });
  const dir = groupDir === -1 ? -1 : 1;
  return Array.from(groups.entries()).sort((a, b) =>
    a[0].localeCompare(b[0], undefined, { numeric: true, sensitivity: "base" }) * dir
  ).map(([category, items]) => {
    const collapsed = collapsedGroups.has(category);
    const panelId = groupedPanelId(panel, items);
    const header = tpl`<tr class="group-row">
      <td colspan="${colspan}"><button type="button" class="row-toggle group-toggle" data-group-panel="${panel}" data-group-name="${category}" aria-expanded="${collapsed ? "false" : "true"}" aria-controls="${panelId}" aria-label="${groupToggleAriaLabel(category, items.length, collapsed)}"><span class="exp" aria-hidden="true">${collapsed ? "▸" : "▾"}</span>${category} <span class="muted">${items.length}</span></button></td>
    </tr>`;
    return [header, collapsed ? nothing : items.map(renderRow)];
  });
}

function updateGroupButtons(prefix, grouped, categories, collapsedGroups, label) {
  const group = $("#" + prefix + "-group-toggle");
  if (group) {
    group.setAttribute("aria-pressed", grouped ? "true" : "false");
    group.title = grouped ? `Ungroup ${label}` : `Group ${label} by category`;
    group.setAttribute("aria-label", group.title);
  }
  const all = $("#" + prefix + "-groups-toggle");
  if (!all) return;
  const any = categories.length > 0;
  const allCollapsed = any && categories.every((category) => collapsedGroups.has(category));
  all.disabled = !grouped || !any;
  all.innerHTML = allCollapsed ? "▾" : "▴";
  all.title = allCollapsed ? `Expand all ${label} groups` : `Collapse all ${label} groups`;
  all.setAttribute("aria-label", all.title);
  all.setAttribute("aria-pressed", grouped && any && !allCollapsed ? "true" : "false");
}

function closestFrom(event, selector) {
  let target = event.target;
  if (target && target.nodeType !== 1) target = target.parentElement;
  return target && target.closest ? target.closest(selector) : null;
}

function bindSortHeader(th, action) {
  th.tabIndex = 0;
  th.setAttribute("aria-sort", "none");
  th.addEventListener("click", action);
  th.addEventListener("keydown", (e) => {
    if (e.key !== "Enter" && e.key !== " ") return;
    e.preventDefault();
    action();
  });
}

function initStaticHandlers() {
  const refreshSelect = $("#refresh-select");
  if (refreshSelect) refreshSelect.addEventListener("change", () => setRefresh(refreshSelect.value));

  const refreshButton = $("#refresh-now");
  if (refreshButton) refreshButton.addEventListener("click", refreshNow);

  const shortcutToggle = $("#shortcut-toggle");
  if (shortcutToggle) {
    shortcutToggle.checked = keyboardShortcutsEnabled();
    shortcutToggle.addEventListener("change", () => setKeyboardShortcutsEnabled(shortcutToggle.checked));
  }

  const svcSearch = $("#svc-search");
  if (svcSearch) {
    svcSearch.addEventListener("input", () => setSvcQuery(svcSearch.value));
    svcSearch.addEventListener("keydown", (e) => {
      if (e.key === "Escape") {
        svcSearch.value = "";
        setSvcQuery("");
      }
    });
  }

  const svcFilters = $("#svc-filters");
  if (svcFilters) {
    svcFilters.addEventListener("click", (e) => {
      const btn = closestFrom(e, "button[data-f]");
      if (btn) setSvcStatus(btn.dataset.f || "all");
    });
  }

  const svcCategorySelect = $("#svc-category");
  if (svcCategorySelect) svcCategorySelect.addEventListener("change", () => setSvcCategory(svcCategorySelect.value));

  const svcGroupToggle = $("#svc-group-toggle");
  if (svcGroupToggle) {
    svcGroupToggle.addEventListener("click", (e) => {
      e.preventDefault();
      e.stopPropagation();
      setSvcGrouped(!svcGrouped);
    });
  }
  const svcGroupsToggle = $("#svc-groups-toggle");
  if (svcGroupsToggle) {
    svcGroupsToggle.addEventListener("click", (e) => {
      e.preventDefault();
      e.stopPropagation();
      toggleAllSvcGroups();
    });
  }

  document.querySelectorAll(".services-table th.sortable[data-sort]").forEach((th) => {
    bindSortHeader(th, () => setSvcSort(th.dataset.sort || ""));
  });

  document.querySelectorAll(".events th.sortable[data-ev-sort]").forEach((th) => {
    bindSortHeader(th, () => setEvSort(th.dataset.evSort || ""));
  });

  applyUIStateToControls();

  function bindWatchPanelControls(panelKey) {
    const panel = getWatchPanel(panelKey);
    const search = $(panel.search);
    if (search) {
      search.addEventListener("input", () => setWatchQuery(panelKey, search.value));
      search.addEventListener("keydown", (e) => {
        if (e.key === "Escape") {
          search.value = "";
          setWatchQuery(panelKey, "");
        }
      });
    }

    const typeSelect = $(panel.typeSelect);
    if (typeSelect) typeSelect.addEventListener("change", () => setWatchType(panelKey, typeSelect.value));

    const filters = $(panel.filters);
    if (filters) {
      filters.addEventListener("click", (e) => {
        const btn = closestFrom(e, "button[data-wf]");
        if (btn) setWatchStatus(panelKey, btn.dataset.wf || "all");
      });
    }
  }
  ["storage", "network", "cert", "diskio", "host"].forEach(bindWatchPanelControls);

  document.querySelectorAll(".watch-table th.sortable[data-watch-sort]").forEach((th) => {
    bindSortHeader(th, () => setWatchSort(watchPanelKeyForElement(th), th.dataset.watchSort || ""));
  });

  const mountSearch = $("#mount-search");
  if (mountSearch) {
    mountSearch.addEventListener("input", () => setMountQuery(mountSearch.value));
    mountSearch.addEventListener("keydown", (e) => {
      if (e.key === "Escape") {
        mountSearch.value = "";
        setMountQuery("");
      }
    });
  }

  const mountCategorySelect = $("#mount-category");
  if (mountCategorySelect) mountCategorySelect.addEventListener("change", () => setMountCategory(mountCategorySelect.value));

  const mountFilters = $("#mount-filters");
  if (mountFilters) {
    mountFilters.addEventListener("click", (e) => {
      const btn = closestFrom(e, "button[data-mf]");
      if (btn) setMountStatus(btn.dataset.mf || "all");
    });
  }

  document.querySelectorAll(".mount-table th.sortable[data-mount-sort]").forEach((th) => {
    bindSortHeader(th, () => setMountSort(th.dataset.mountSort || ""));
  });

  const appSearch = $("#app-search");
  if (appSearch) {
    appSearch.addEventListener("input", () => setAppQuery(appSearch.value));
    appSearch.addEventListener("keydown", (e) => {
      if (e.key === "Escape") {
        appSearch.value = "";
        setAppQuery("");
      }
    });
  }

  const appCategorySelect = $("#app-category");
  if (appCategorySelect) appCategorySelect.addEventListener("change", () => setAppCategory(appCategorySelect.value));
  const appFilters = $("#app-filters");
  if (appFilters) {
    appFilters.addEventListener("click", (e) => {
      const btn = closestFrom(e, "button[data-af]");
      if (btn) setAppStatus(btn.dataset.af || "all");
    });
  }

  const appGroupToggle = $("#app-group-toggle");
  if (appGroupToggle) {
    appGroupToggle.addEventListener("click", (e) => {
      e.preventDefault();
      e.stopPropagation();
      setAppGrouped(!appGrouped);
    });
  }
  const appGroupsToggle = $("#app-groups-toggle");
  if (appGroupsToggle) {
    appGroupsToggle.addEventListener("click", (e) => {
      e.preventDefault();
      e.stopPropagation();
      toggleAllAppGroups();
    });
  }

  document.querySelectorAll(".apps-table th.sortable[data-app-sort]").forEach((th) => {
    bindSortHeader(th, () => setAppSort(th.dataset.appSort || ""));
  });

  ["event-service", "event-watch", "event-kind", "event-status"].forEach((id) => {
    const el = $("#" + id);
    if (!el) return;
    el.addEventListener("input", scheduleLoadEvents);
    el.addEventListener("keydown", eventFilterKey);
  });
  const onlyErrors = $("#event-errors");
  if (onlyErrors) onlyErrors.addEventListener("change", flushLoadEvents);
  const groupEvents = $("#event-group");
  if (groupEvents) groupEvents.addEventListener("change", () => { saveUIState(); renderGlobalEvents(); });
  const eventResetFilters = $("#event-reset-filters");
  if (eventResetFilters) eventResetFilters.addEventListener("click", clearEventFilters);
  const eventClear = $("#event-clear");
  if (eventClear) {
    eventClear.addEventListener("click", (e) => {
      e.stopPropagation();
      clearEventLog();
    });
  }

  document.querySelectorAll("[data-confirm-result]").forEach((btn) => {
    btn.addEventListener("click", () => closeActionConfirm(btn.dataset.confirmResult === "true"));
  });
  const confirmPreflight = $("#confirm-preflight-btn");
  if (confirmPreflight) confirmPreflight.addEventListener("click", runConfirmPreflight);

  const reloadBtn = $("#reload-btn");
  if (reloadBtn) {
    reloadBtn.addEventListener("click", (e) => {
      e.stopPropagation();
      reloadConfig();
    });
  }
  const activityClear = $("#activity-clear");
  if (activityClear) {
    activityClear.addEventListener("click", (e) => {
      e.stopPropagation();
      clearEventLog($("#event-before")?.value || "");
    });
  }
  const stateCompactBtn = $("#state-compact-btn");
  if (stateCompactBtn) {
    stateCompactBtn.addEventListener("click", (e) => {
      e.stopPropagation();
      compactState();
    });
  }
  const panicBtn = $("#panic-btn");
  if (panicBtn) {
    panicBtn.addEventListener("click", (e) => {
      e.stopPropagation();
      requestPanic(!panicOn);
    });
  }
  const panicDlg = $("#panic-confirm");
  if (panicDlg) {
    panicDlg.addEventListener("click", (e) => {
      const b = e.target.closest("[data-panic-result]");
      if (b) closePanicConfirm(b.dataset.panicResult === "true");
    });
    panicDlg.addEventListener("close", () => { if (panicResolve) closePanicConfirm(false); });
  }

  const simpleDlg = $("#simple-confirm");
  if (simpleDlg) {
    simpleDlg.addEventListener("click", (e) => {
      const b = closestFrom(e, "[data-simple-result]");
      if (b) closePromptConfirm(b.dataset.simpleResult === "true");
    });
    simpleDlg.addEventListener("close", () => { if (promptConfirmResolve) closePromptConfirm(false); });
  }
}

function initDelegatedHandlers() {
  document.addEventListener("click", (e) => {
    const eventToggle = closestFrom(e, "[data-event-toggle]");
    if (eventToggle) {
      toggleEventMsg(eventToggle.dataset.eventToggle || "");
      return;
    }

    const panelTarget = closestFrom(e, "[data-panel-target]");
    if (panelTarget) {
      openPanelTarget(panelTarget.dataset.panelTarget || "");
      return;
    }

    const serviceAction = closestFrom(e, "[data-service-action][data-service]");
    if (serviceAction) {
      act(serviceAction.dataset.service || "", serviceAction.dataset.serviceAction || "");
      return;
    }

    const watchAction = closestFrom(e, "[data-watch-action][data-watch]");
    if (watchAction) {
      actWatch(watchAction.dataset.watch || "", watchAction.dataset.watchAction || "");
      return;
    }

    const mountAction = closestFrom(e, "[data-mount-action][data-mount]");
    if (mountAction) {
      actMount(mountAction.dataset.mount || "", mountAction.dataset.mountAction || "");
      return;
    }

    const serviceExpand = closestFrom(e, "[data-service-expand]");
    if (serviceExpand) {
      toggleServiceExpansion(serviceExpand.dataset.serviceExpand || "");
      return;
    }

    const serviceOpen = closestFrom(e, "[data-service-open]");
    if (serviceOpen) {
      openServiceExpansion(serviceOpen.dataset.serviceOpen || "", true);
      return;
    }

    const release = closestFrom(e, "[data-lock-release]");
    if (release) {
      releaseLock(release.dataset.lockService || "", release.dataset.lockName || "");
      return;
    }

    const preflight = closestFrom(e, "[data-preflight-service]");
    if (preflight) {
      runPreflight(preflight.dataset.preflightService || "");
      return;
    }

    const metricCheckBtn = closestFrom(e, "[data-metric-check]");
    if (metricCheckBtn) {
      setMetricCheck(metricCheckBtn.dataset.metricCheck || "", metricCheckBtn.dataset.metricService || "");
      return;
    }

    const windowBtn = closestFrom(e, "[data-window-kind][data-window-value]");
    if (windowBtn) {
      const val = windowBtn.dataset.windowValue || "";
      switch (windowBtn.dataset.windowKind) {
        case "setMetricWin":
          setMetricWin(val);
          break;
        case "setDaemonMetricWin":
          setDaemonMetricWin(val);
          break;
      }
      return;
    }

    const group = closestFrom(e, "[data-group-panel][data-group-name]");
    if (group) {
      toggleCategoryGroup(group.dataset.groupPanel || "", group.dataset.groupName || "");
      return;
    }

    const expToggle = closestFrom(e, "[data-exp-toggle]");
    if (expToggle) {
      toggleExpand(expToggle.dataset.expToggle || "");
      return;
    }

    const row = closestFrom(e, "[data-exp-key]");
    if (row) rowClick(e, row.dataset.expKey || "");
  });
}

initStaticHandlers();
initDelegatedHandlers();
loadMe().then(() => { load(); });

// Manual refresh + a once-per-second "updated Xs ago" readout. The readout is
// independent of the auto-refresh interval, so it keeps counting up even when
// auto-refresh is set to a long interval or stopped.
let lastRefresh = 0;
function refreshNow() { load(); }
function fmtSince(ms) {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return s + "s";
  const m = Math.floor(s / 60), r = s % 60;
  return r ? `${m}m ${r}s` : `${m}m`;
}
function tickRefreshAge() {
  if (!connOK) { showDisconnected(); return; } // keep the banner's age fresh
  const el = $("#last-refresh");
  if (!el) return;
  const text = lastRefresh ? `updated ${fmtSince(Date.now() - lastRefresh)} ago` : "";
  if (el.textContent === text) return;
  el.textContent = text;
}
setInterval(tickRefreshAge, 1000);

let refreshTimer = null;
function applyRefresh(ms) {
  if (refreshTimer) clearInterval(refreshTimer);
  // Skip polling while the tab is hidden (no one is looking); a visibilitychange
  // handler refreshes immediately when it becomes visible again.
  refreshTimer = ms > 0 ? setInterval(() => { if (document.hidden) return; load(); }, ms) : null;
}
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) load();
});
function setRefresh(v) {
  const ms = parseInt(v, 10) || 0;
  applyRefresh(ms);
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
  try { return localStorage.getItem(KEYBOARD_SHORTCUTS_KEY) !== "0"; } catch (_) { return true; }
}

function setKeyboardShortcutsEnabled(enabled) {
  try { localStorage.setItem(KEYBOARD_SHORTCUTS_KEY, enabled ? "1" : "0"); } catch (_) {}
}

// activeSearchBox returns the search input for the topmost open data panel.
function activeSearchBox() {
  const panels = [
    ["#services-section", "#svc-search"],
    ["#storage-section", "#storage-search"],
    ["#network-section", "#network-search"],
    ["#watches-section", "#watch-search"],
    ["#apps-section", "#app-search"],
  ];
  for (const [sectionSel, searchSel] of panels) {
    const section = $(sectionSel);
    if (!section || !panelVisible(section) || !section.open) continue;
    const box = $(searchSel);
    if (box) return { section, box };
  }
  const fallback = $("#svc-search");
  return fallback ? { section: $("#services-section"), box: fallback } : null;
}

// "/" focuses the visible panel search (unless already typing in a field).
document.addEventListener("keydown", (e) => {
  if (e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey) return;
  if (!keyboardShortcutsEnabled()) return;
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
      if (saved !== null) el.open = saved === "1";
    } catch (_) {}
    el.addEventListener("toggle", () => {
      try { localStorage.setItem(key, el.open ? "1" : "0"); } catch (_) {}
    });
  });
})();
