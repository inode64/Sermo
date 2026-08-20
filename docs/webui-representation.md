# Web UI representation

This file is an editable map of the Web UI. Use it to describe layout changes in
plain Markdown; the implementation lives in `internal/web/index.html`.

Keep changes concrete:

- panel title
- controls
- columns
- row expansion
- actions
- empty states
- ordering / grouping

`make web-e2e` validates this representation in desktop and mobile Chromium,
including global search, compact row actions, per-service graph state, viewport
overflow and axe WCAG 2.2 AA rules against deterministic API fixtures.

## Contents

- [Global rules](#global-rules)
- [Data sources](#data-sources)
- [SLA timeline strip](#sla-timeline-strip)
- [Action Endpoints](#action-endpoints)
- [Top bar](#top-bar)
- [Overview tiles](#overview-tiles)
- [Attention required](#attention-required)
- [Live operations](#live-operations)
- [Services panel](#services-panel)
- [Containers and virtual machines panels](#containers-and-virtual-machines-panels)
- [Service row expansion](#service-row-expansion)
- [Installed applications panel](#installed-applications-panel)
- [Installed libraries panel](#installed-libraries-panel)
- [Mount units panel](#mount-units-panel)
- [Watch panel](#watch-panel)
- [Events panel](#events-panel)
- [Notifiers panel](#notifiers-panel)
- [Daemon / Engine settings panel](#daemon--engine-settings-panel)
  - [Panic mode](#panic-mode)
- [Runtime locks panel](#runtime-locks-panel)
- [Action confirmation dialog](#action-confirmation-dialog)
- [Change template](#change-template)

## Global rules

- The Web UI is one embedded document: `internal/web/index.html`.
- Data panels are `<details>` cards. The page scrolls as a whole; do not add
  panel-local scrollbars.
- Every data panel carries `class="panel"` (shared styling such as the
  disconnected dimming targets that class, not an id list). Watch panel
  `<details>` also carry `data-panel="<key>"` naming their entry in the
  `watchPanels` registry; rendering, deep-link routing, attention navigation
  and the `/` search shortcut iterate that registry. Static IDs, columns,
  controls and copy come from `internal/web/src/watch-panels.json`, shared by
  the Go shell builder and the runtime registry.
- Services, containers, virtual machines, applications, libraries and mount
  units group by `category`; watch panels group by their panel-specific type.
- A top-level YAML `category` field is the category source. If it is absent,
  services fall back to `service`, applications to `app`, storage watches to
  `storage` and other watches to `watch`.
- State-changing buttons use the same safe backend path as `sermoctl`.
- Timestamps render in UTC, the daemon's canonical convention shared with
  events and notifications; the visible timestamps in the event and activity
  views carry the viewer's local time as their hover title.

## Data sources

| Area | Endpoint | Notes |
| --- | --- | --- |
| Current user | `GET /api/whoami` | role and action permissions; action controls stay hidden until this request succeeds |
| Dashboard snapshot | `GET /api/dashboard?since=WINDOW` | aggregate of the frequently refreshed service/runtime panels from one active daemon configuration generation; it carries `generation`, and dashboard data responses carry `X-Sermo-Generation` so the browser discards a mixed reload view before rendering it; the browser falls back to the individual endpoints if unavailable |
| Change stream | `GET /api/stream` | Server-Sent Events channel that pushes a payload-free `change` signal on every daemon event; the dashboard refetches immediately. It only adds refreshes: the scheduled poll always keeps the cadence chosen in the top bar, because nothing is pushed when a metric sample changes and host/service/watch readings depend on that poll |
| Readiness | `GET /readyz?verbose` | daemon `status:` in the top bar (`starting` / `ok` / …) |
| Services | `GET /api/services` | configured runtime services loaded by sermod (not `sermoctl services` catalog inventory); `status_observed_at` identifies the real init-status sample behind a cached row; `operation_active` is true while the engine holds the service's operation lock, so an action started from any client, `sermoctl` or automatic remediation shows as in progress and its action buttons stay disabled |
| Sessions | `GET /api/sessions` | dashboard-wide SSH, tmux and screen inventory; each present configured source reports `available`, `collecting` or `unavailable`; an available tmux server with zero sessions is `empty`, while an absent tmux/screen namespace is omitted; SSH uses the shared short-lived sampler cache, while tmux/screen rows come only from daemon-published `terminal_sessions` samples |
| Service expansion | `GET /api/services/{name}` | checks, process info, rules |
| Service check metrics | `GET /api/services/{name}/metrics?check=NAME[&metric=KEY]` | the detail renders latency when `metric` is omitted and one graph for every named numeric metric published by a check |
| Service runtime metrics | `GET /api/services/{name}/runtime` | read-only persisted service CPU/memory/IO history sampled exclusively by worker cycles; `current` is the latest published sample and dashboard reads never repeat process discovery |
| Service SLA | `GET /api/services/{name}/sla[?check=NAME]` | availability history for the service detail SLA timeline and API clients, at the resolution that window is stored at; `check` scopes it to one of the service's checks, which is where the checks table reads its strip from, so both scopes share one series path and one window selector; a check that reports no verdict serves no points; observed-SLA ratios count only monitored minutes, so unmeasured time is a gap, not downtime; each point also carries `down_buckets`, the one-minute buckets inside it that saw a failure |
| Service events | `GET /api/services/{name}/events` | per-service event feed |
| Watches | `GET /api/watches` | host-level and service-scoped watches; `scope` distinguishes them and service watch names use `service:watch` |
| Applications | `GET /api/applications` | installed catalog apps; `observed_at` remains fixed while the version/status inventory is served from cache |
| Libraries | `GET /api/libraries` | installed catalog libraries; `observed_at` remains fixed while the file/version inventory is served from cache |
| Mount units | `GET /api/mounts` | storage watches with `mount:` backed by fstab |
| Notifiers | `GET /api/notifiers` | notifier targets |
| Daemon settings | `GET /api/daemon` | engine/runtime config |
| Daemon process metrics | `GET /api/daemon/metrics` | read-only persisted sermod CPU/memory/IO history sampled by the daemon independently of dashboard clients |
| Host metrics | `GET /api/host` | current host CPU, memory and load values |
| Locks | `GET /api/locks` | named runtime locks |
| Events | `GET /api/events` | cursor page of service/watch activity; supports `limit`, `service`, `watch`, `kind`, `status`, `only_errors` |
| Activity summary | `GET /api/activity` | internal recent-event rollup used for dashboard attention indicators |
| Monitoring counts | `GET /api/monitoring` | monitored vs paused service counts |

Init status, application inspection and SLA timeline caches expose their actual
sample times, and SLA segment timestamps stay anchored to `observed_at` instead
of sliding forward on the browser clock while cached.
Dashboard refreshes are single-flight: automatic, manual and post-action reloads
never execute concurrently, and the next automatic delay starts after the prior
refresh completes.

## SLA timeline strip

The availability strips and the service detail's SLA chart colour each cell by
**how much of it was down**, not by its availability. Stored history keeps one
bucket span per window ([Stored history
resolution](configuration.md#stored-history-resolution)), so on the wide windows a
cell covers hours or a whole day: a 40-second outage inside a day-long cell is
99.93% available, which any availability threshold reads as healthy. Colouring by
the down share instead keeps that outage visible.

Five bands, with green reserved for exactly zero — no cell containing a failure can
read as healthy, however little of the cell it affected:

| Down share of the cell | Colour |
|---|---|
| 0% | green (`.sla-down-none`) |
| up to 25% | amber (`.sla-down-low`) |
| up to 50% | orange (`.sla-down-mid`, a `--warn`/`--crit` blend) |
| up to 75% | red-orange (`.sla-down-high`, a `--warn`/`--crit` blend) |
| up to 100% | red (`.sla-down-full`) |

The band says how much of the cell was affected, which is what separates a brief
blip from a half-day outage. A cell with no observation at all stays a hatched
`.sla-gap`, distinct from both — a gap is unmonitored time, not downtime.

Colour is never the only carrier of this (WCAG 2.2 1.4.1): each cell's `title` and
`aria-label` state the availability, the down share and how many one-minute buckets
inside it saw a failure, and the visually-hidden data table beside each strip
repeats them per sub-span in an `Affected` column.

Incident counts come from those per-minute buckets rather than from the number of
points with failures, so three separate bad minutes inside one consolidated bucket
report as three affected minutes, not one.

## Action Endpoints

Every state-changing request (any non-`GET`) must carry the header
`X-Sermo-Csrf: 1`; without it the server responds `403`. This CSRF guard is
enforced unconditionally — in open (no-auth) mode too — so an API client must
always send it. When web auth is enabled these endpoints are additionally
admin-only. Target-scoped actions also carry the current
`X-Sermo-Generation`; the server holds that backend generation through the
action and executes nothing when the header is missing (`428`) or stale after a
reload (`412`). The UI refreshes before a later retry. Other stable status codes
are `401` (auth challenge), `403` (missing CSRF header or guest attempting a
write), `421` (rejected `Host` in open mode), `404` (unknown target), and `200`
with an `{"ok": bool, "message": string}` body for a handled action.

| Area | Endpoint | Notes |
| --- | --- | --- |
| Service action | `POST /api/services/{name}/{action}[?no_cascade=1]` | `monitor`, `unmonitor`, `start`, `stop`, `restart`, `reload`, `resume`, `repair`; `restart` is the primary action for failed/inactive services, while `repair` is a manual-only secondary fallback that uses the guarded stale-pidfile and failed-init-state recovery path before starting; `reload` is offered only when the service reports `can_reload` from init backend reload support or a valid `reload:` fallback; `no_cascade` skips `also_apply` targets on start/stop/restart |
| Service preflight | `POST /api/services/{name}/preflight` | run preflight checks without changing service state |
| Close SSH session | `POST /api/services/{name}/sessions/{pid}/close?start_ticks=TICKS&terminal=PTS` | admin-only, confirmation-required graceful close of one displayed SSH terminal; the backend re-discovers the terminal plus exact configured `sshd` executable and real user, then requires the same PID and start ticks before sending only `SIGTERM` |
| Close terminal session | `POST /api/services/{name}/terminal-sessions/{check}/close?multiplexer=TYPE&session=NAME&user=USER&identity=IDENTITY` | admin-only, confirmation-required close of one tmux/screen session; the backend freshly lists the configured user/socket namespace, requires the same multiplexer generation identity and invokes only the client's exact session-close argv |
| Close empty tmux server | `POST /api/services/{name}/terminal-sessions/{check}/close-empty` | admin-only, confirmation-required close available only for a present, empty tmux source with an explicit configured socket; the backend revalidates it is still empty, runs tmux's exact `kill-server` argv, verifies the namespace is gone and removes only the unchanged stale socket it may leave |
| Watch action | `POST /api/watches/{name}/{action}` | `monitor`, `unmonitor`, `expand`, `probe` (one manual sample), plus RAID `pause`/`resume`, which run a check-and-verify operation and require the `X-Sermo-Confirm` header |
| Notifier test | `POST /api/notifiers/{name}/test` | sends one test notification through the named notifier after confirmation |
| Mount action | `POST /api/mounts/{name}/{action}[?force=1&lazy=1&kill=1]` | `mount`, `umount`, `alert`; `force=1` allows `umount -f`, `lazy=1` allows `umount -l` as the last fallback, and `kill=1` enables `kill_only_if`-gated blocker signalling for `umount`; `/` rejects unmount paths |
| Mount blockers | `GET /api/mounts/{name}/blockers` | read-only fresh blocker scan for one mount unit; guests get command lines redacted like `GET /api/mounts` |
| Lock release | `POST /api/locks/{service}/release?name=NAME` | releases inactive stale/expired named locks; active locks are refused |
| Events clear | `POST /api/events/clear?before=TIME` | clears persisted event/activity rows; `before` accepts a positive duration or non-future RFC3339 timestamp |
| State compact | `POST /api/state/compact?before=TIME` | consolidates and prunes stored history to the configured retention, then vacuums the state database; `before` optionally drops whatever remains older than an explicit cutoff; matches `sermoctl state compact` |
| Panic mode | `POST /api/panic/{action}` | `on` / `off`; admin-only daemon-wide suspension of hooks, alerts and automatic remediation |
| Daemon reload | `POST /api/reload` | requests a `sermod` configuration reload |

## Top bar

| Element | Current representation |
| --- | --- |
| Brand | `Sermo` with status dot |
| Role | admin / read-only label |
| Find target | one autocomplete over loaded services, watches, applications and mounts; selection clears only that panel's filters and opens the target |
| Refresh | select with refresh interval, manual refresh button |
| Notifications | opt-in browser-notification bell; once granted, targets that newly start failing raise one grouped notification while the tab is hidden |
| Status | last complete refresh age, connection errors, or panels retaining older data after a partial refresh; `#statusbar` ends with host `uptime:` then daemon `status:` (`ok` / `starting` / …) as a paired tail |
| Sessions | when a configured SSH service can be attributed safely, `sessions (console/SSH): X/Z` is the number of logged-in local-console and SSH terminals; it replaces the former distinct-user count, so three terminals of the same account read `0/3`, not `1` |
| System status | host identity, host type, daemon/backend/runtime summary |
| Browser tab title | after the first full load: `Sermo - <host>` when healthy, `(N) Sermo - <host>` when attention has N signals, `Sermo - <host> · starting` while the daemon is starting; `<host>` is the short hostname from `GET /api/daemon` (same identity as the Basic auth realm) |

Editable notes:

- Keep top bar compact and sticky.
- Do not move operational controls into marketing-style hero blocks.
- Refresh controls should stay visible on narrow screens.
- `Ctrl+K`/`Cmd+K` focuses the global target search. It uses the current
  dashboard snapshot and does not issue another request.
- The `uptime:` reading in the status line is the **host/server** uptime (from
  `/proc/uptime`, surfaced as `host_uptime` on `GET /api/daemon`), not the sermod
  process uptime. The sermod process uptime stays on the Daemon panel and
  `GET /livez?verbose`.
- Action feedback (the `#err` status line, ok/warn/err) stays visible for at
  least ~5 seconds: the dashboard refresh that a completed action triggers does
  not clear it, so a result like `umount failed: device busy` remains readable.
  Starting a new action clears it immediately, and the disconnected banner is
  exempt — it disappears on the first successful refresh.
- Connection state has three levels. Connected: normal rendering. Partially
  degraded: the services list request failed but some other dashboard endpoint
  still answered — the page keeps the last rendered services list (or shows
  none on a cold load), stays undimmed, and warns `services unavailable` in the
  status line. Disconnected: no dashboard endpoint answered — the panels dim
  (`body.disconnected`) and the `Disconnected — retrying…` banner shows until
  the first successful refresh.

## Overview tiles

Rendered by `renderOverview` from already-loaded state, without extra requests.

| Tile kind | Current content |
| --- | --- |
| Services active | count / total for services in `started`, `collecting`, `warning` or `monitored`; critical when any service is `failed`, warning while any service is `collecting` or `warning`, neutral while any target is settling, otherwise active; click opens the matching `failed`, `starting`, `collecting` or `warning` service filter when applicable |
| Watches | count / total; critical when any watch is `failed`, neutral while any target is settling (subtitle names starting watches, services or apps), otherwise quiet; click opens the matching `starting`/`failed` filter |
| Alerts | count of failing services, firing watches, failed installed apps and active locks, with a per-kind breakdown; click routes to `failed-services`, `failed-watches`, `failed-apps` or `locks-section` in priority order |
| Monitored | services in state `monitored` vs enabled services; warning while services are `collecting` or `warning` (subtitle names those needing attention), neutral with settling subtitle during startup, click opens the relevant service filter |
| Host gauges | memory, load, fds, pids, conntrack, etc. when present |
| Volumes | one gauge per mounted storage watch, crit when its watch is firing |

Editable notes:

- Tiles should jump to the related panel. During startup settling, Services active and
  Watches tiles open the `starting` filter on the panel that still has unsettled
  targets (`starting-services`, `starting-watches` or `starting-apps`). After a
  config reload the daemon header stays `ok` (no grey favicon) even when
  individual targets are still `starting`.
- Usage bars stay at the bottom of each tile.
- Do not add explanatory text inside tiles.

## Attention required

| Element | Current representation |
| --- | --- |
| Container | visible only when signals exist |
| Items | warning / critical buttons |
| Click behavior | opens the related panel |

Signals include failing services, firing watches, failed installed
applications, recent errors and readiness issues (including
`shutting_down`). A failing-services item opens the Services panel with the
`failed` filter; a firing-watches item opens Watches with the `failed`
filter (`failed-watches` target); a failing-apps item opens Installed
applications with the `failed` filter (`failed-apps` target). Daemon startup
progress stays in the top-bar `status: starting` line, not in this box.

## Live operations

| Element | Current representation |
| --- | --- |
| Container | visible while operations are active/recent |
| Cards | action, service, state, elapsed time, message |

Session-local for operations started from the current browser.

## Services panel

Section id: `services-section`

Lists **configured** service entries from the loaded config, excluding Docker
containers (`category: docker`) and virtual machines (`category:
virtual-machine`), which render in their own panels. This is not `sermoctl
services`, which inventories **catalog** service profiles under
`catalog/services`. See [cli.md](cli.md#catalog-inventory).

| Part | Current representation |
| --- | --- |
| Title | `Services` plus total count |
| Title icons | group by category, collapse/expand all groups |
| Controls | search, category select, status filters, showing count |
| Status filters | all, disabled, stopped, started, starting, collecting, monitored, warning, failed |
| Sorting | Service, Category, State |
| Grouping | category group rows, collapsible |

Columns:

| Column | Meaning |
| --- | --- |
| Service | display name, falling back to name, capitalized |
| Category | YAML category or fallback |
| State | single normalized service state: `disabled`, `stopped`, `started`, `starting`, `collecting`, `monitored`, `warning` or `failed`; `warning` marks either a healthy service without an attributable process tree or a workload with a failed init unit but a verified live process and passing functional checks; its inline reason distinguishes the cases |
| Uptime | age of the oldest discovered service process, when available |
| CPU total | latest whole process-tree CPU usage; blank for `no_resident_process` services |
| Memory | latest process-tree resident memory; blank for `no_resident_process` services |
| FDs | open file-descriptor count from the process tree; blank for `no_resident_process` services |
| IO R/W | cumulative process-tree disk read/write bytes; blank for `no_resident_process` services |
| Strays | count of control-group members no selector claims, from the `strays` check's published snapshot; a dash when there are none. Non-zero, it carries a reap button for admins — confirmed, and gated server-side by the service's own `reap.kill_only_if`. Retired below 640px, where the count and its button move into the expansion |
| Actions | compact, individual state-aware icon buttons for start/stop, restart, reload, resume and monitor/unmonitor; reload is disabled when `can_reload` is false; the start/stop/restart confirm dialog offers **skip also_apply** when `also_apply` is set |
| Pin | a per-row star toggle lifts hand-picked services to the top of the panel (and of their group), persisted locally with the rest of the UI state |

## Containers and virtual machines panels

Section ids: `containers-section`, `vms-section`

Docker container services and libvirt virtual machine services use the same
service API and row expansion as the Services panel, but are separated by
category for operators. These panels keep the `resume` action because paused
containers and paused VMs can be resumed through the service operation path.

| Panel | Source category | Extra action |
| --- | --- | --- |
| Containers | `docker` | `resume` when the container backend reports `paused` |
| Virtual machines | `virtual-machine` | `resume` when the VM backend reports `paused` |

Both panels expose the same category grouping and collapse controls as Services.

## Service row expansion

Shared by the Services, Containers and Virtual machines panels.

| Area | Content |
| --- | --- |
| General data | an unheaded grid, first area of the expansion: name, state, category, unit/backend, uptime, interval, policy, locks, last event, next remediation, remediation state and process totals; while the row badge is `starting`, expansion may still show the raw init backend (`inactive`) and in-flight check samples from the observe-only cycle |
| Graphs | full-width SLA timeline followed by latency, CPU, memory and IO charts; each service persists its own time window and latency check; `no_resident_process` services show only SLA because they have no process runtime to chart |
| Processes | full-width detected process tree table, with child processes marked in CMD and kept under their parent; **Max core** follows CPU and reports the most a single core was used by that process — its busiest thread — whose tooltip says whether the daemon measured it per thread or bounded it by the process rate; the **Role** cell reads `stray` for a control-group member no selector claims, in place of the backend seed's misleading `main`; discovery warnings are listed above it, one per line; omitted when `no_resident_process` is true |
| Checks | configured checks and current result; the SLA column carries the same availability band the Graphs SLA timeline draws, on the window that section's selector is on, so an unobserved stretch reads as a hatched gap in both instead of a flat percentage in one |
| Named locks | runtime lock state |
| Rules | remediation/alert rule state |
| Preflight | inline preflight runner and results |
| Events | recent retained service events |

The expansion complements the row rather than repeating it: it carries no name
heading (the row is the heading) and no summary line restating the grid. A General
data field whose reading is already a table column is shown **only at the widths
where that column is hidden** — Category, Uptime, CPU total, Memory and IO R/W below
1420px, Last event below 640px — so each reading appears exactly once and a phone
loses nothing. Name and State stay at every width as the expansion's anchor, and
`FDs / Threads` is never hidden because the FDs column does not carry the thread
count. The busiest-thread figure is not restated in the grid: it belongs to a
process, so the process table carries it per row (see **Processes** below) instead of
floating as a total that hides which process it came from.

## Sessions panel

The top-level Sessions panel combines interactive SSH terminals with configured
tmux and GNU screen namespaces. Search covers type, service, user and session;
type buttons select SSH, tmux or screen. The table shows only the user in its
User column and can sort by type, user, session, state, idle, CPU, memory or IO.
A type filter is hidden when that type has no active sessions, using the same
counted-filter behavior as the other panels. A configured source remains
visible under `all` when it has no sessions, while collecting and sampling
failures use distinct states. Attributable rows expose idle time and
process-tree CPU, resident memory and read/write IO rates. An admin can confirm
a close only when the backend can freshly revalidate the exact SSH or
multiplexer session identity. An empty successful source uses a red `empty`
pill and has no process identity to close; its `close` button dismisses only
that empty row in the current browser. No multiplexer command or signal runs,
and the row returns after that source reports an active session.

Open service expansions fetch and fully render fresh detail once per dashboard
refresh; SLA, metric, runtime and event subrequests plus open watch/application
details must finish before the header advances `fully updated`. Re-renders in
between (filter keystrokes, sorting, the live-operations ticker) redraw from
cached detail without extra requests. A late response from an older graph
selection is ignored instead of overwriting the service's current charts.

Empty states:

- `No services.`
- `No services match the filter.`

## Installed applications panel

Section id: `apps-section`

| Part | Current representation |
| --- | --- |
| Title | `Installed applications` plus total count |
| Title icons | group by category, collapse/expand all groups |
| Controls | search, category select, status filters, showing count |
| Status filters | all, ok, starting, warning, failed |
| Sorting | Application, Category, Status, Version |
| Visibility | hidden when no installed apps are returned; catalog apps without an installed binary are never listed and do not show `starting` during daemon settling |
| Grouping | category group rows, collapsible |

Columns:

| Column | Meaning |
| --- | --- |
| Application | display name, falling back to name, capitalized |
| Category | YAML category or fallback |
| Status | app inspection state (`Ok`, `Starting` while the daemon settles, warning, failed) plus the age of its actual probe |
| Version | short version, falling back to raw version |

Row expansion:

| Field | Meaning |
| --- | --- |
| Version | full version output |
| Version source | provider app name when `version_from` supplied the version |
| Category | YAML category or fallback |
| Location | resolved binary path |
| Permissions | mode string |
| User | binary owner |
| Group | binary group |
| Status | app inspection status |

Empty state:

- `No applications match the filter.`

## Installed libraries panel

Section id: `libraries-section`

| Part | Current representation |
| --- | --- |
| Title | `Installed libraries` plus total count |
| Title icons | group by category, collapse/expand all groups |
| Controls | search, category select, status filters, showing count |
| Status filters | all, ok, warning, failed |
| Sorting | Library, Category, Status, Version |
| Visibility | hidden when no installed library files are returned |
| Grouping | category group rows, collapsible |

Columns: Library (display name), Category, Status (inspection state and probe age),
and Version (short version when available). Expanding a row shows version source,
file location, permissions, user, group and full status. Library rows do not show
application SLA or application events.

Empty state:

- `No libraries match the filter.`

## Mount units panel

Section id: `mounts-section`

| Part | Current representation |
| --- | --- |
| Title | `Mount units` plus total count |
| Visibility | hidden when no configured mount units are returned |
| Title icons | group by mount group, collapse/expand all groups (hidden when only one group exists) |
| Controls | search by mount text, group dropdown when more than one group exists, state filters (`all`, `active`, `inactive`) |
| Grouping | mount group rows, collapsible |

Columns:

| Column | Meaning |
| --- | --- |
| Name | display name, falling back to mount name |
| Group | mount category/group label |
| Path | configured mount path; appends `mounting` or `unmounting` while an operation is in progress |
| Mounted | live mount state |
| Refcount | Sermo runtime refcount, or `off` |
| Processes | compact list of processes currently using the mount path |
| Users | unique users for those processes |
| State | active/inactive/error pill, or `mounting`/`unmounting` while an operation is in progress |
| Actions | compact admin-only mount/umount icon plus alert; mounted rows open a single unmount dialog with force/lazy/kill-blockers choices; buttons for that row are disabled while a mount operation is in progress; `/` renders this unmount flow disabled |

The column headers except Actions are sortable.
`GET /api/mounts` includes a cached read-only blocker summary for the table and
an optional `operation` object (`action`, `state`, `started_at`, `message`) when
the daemon is currently mounting or unmounting that unit.
Before `umount` or `alert`, the UI asks `GET /api/mounts/{name}/blockers` and
shows a fresh process list for the path. The unmount dialog always shows the
blocker table; `kill blockers` is enabled only when `has_kill_policy` and
`can_kill` are true, and only rows marked `killable` can be signalled. `alert`
sends a native TTY message to logged-in blocking users. For `path: /`,
`GET /api/mounts` returns `can_umount: false`; the Web UI disables the
unmount-flow buttons and the API rejects `umount?kill=1` without scanning
blockers or sending signals.

## Watch panel

Section id: `watches-section`

`Watches` contains both host-level and service-scoped watches. Host scope is the
panel default, so only a service-scoped row marks its `scope` next to the name;
those names use `service:watch` and run as part of that service's worker rather
than independently as host watches do. Every row's expansion shows the full
`scope` value, and both values remain searchable.

A `storage` watch summary shows the path, filesystem, mount point and used/free
space from the latest fresh daemon-cycle snapshot. The Web UI does not rescan
mounts, filesystems or open file descriptors for this panel; before the first
cycle or after a stale snapshot it reflects the watch's collecting/stale state.
The service list row likewise shows a service's open file-descriptor count
(`fds`) in its own column, from the same per-process totals already in the
service detail.

| Part | Current representation |
| --- | --- |
| Title | Panel name plus total count for that panel's watch subset |
| Title icons | group by panel type, collapse/expand all type groups (hidden when only one group exists) |
| Controls | search, type filter (per panel, see below), state filters, showing count |
| Type filter | panel-specific `all ... types` plus the distinct values currently present in that panel; Storage filters by filesystem type (all its watches share one check type), Certificate watches by public-key algorithm; the selector is hidden when only one value exists |
| Grouping | collapsible rows by the same panel-specific type used by the type filter |
| State filters | all, disabled, ok, starting, warning, failed |
| Search | display name, raw name, category, type, summary, interval, polarity, hook state/command, notifier names, expand/dry-run/monitoring state and conditions |
| Sorting | every data column except Actions is sortable independently inside its check-type table; each table defaults to Name ascending |
| Visibility | hidden when no watches are configured for that panel's subset |

Watches are grouped as System, Storage, Network and Security, then split
into a check-type table. Every type table ends with Last checked, Last activity,
State and Actions; it does not use a generic Summary column. Last checked is the
latest completed daemon-cycle or manual sample, while Last activity is an event.

| Check type | Type-specific columns |
| --- | --- |
| `storage` | Name, Usage, Filesystem, Mount point; filters by filesystem when more than one is present |
| `file` | Name, Path, current age, configured age limit; a configured check `summary` replaces the age and limit columns with Summary |
| `net` | Name, interface, link, speed, errors |
| `hdparm` | Name, device, buffered read, cached read |
| `lvm` | Name, health, VG, LV, VG size, VG free, reasons |
| `smart` | Name, device, health, temperature, wear, reallocated sectors, formatted power-on time |
| `diskio` | Name, device, utilization, read, write, await, read total, written total (cumulative since boot, so an idle disk is distinguishable from one nothing ever touches) |
| `cert` | Name, source, days left, expiry, issuer |
| `raid` | Name, array, size, degraded, recovering |
| Other types | Name and their primary live value |

The health column carries two vocabularies and colours both: the `lvm` check
normalises its own to `ok`/`error`, while `smart` reports the drive's verdict in
smartctl's words. `ok` and `PASSED` read green, `unknown` — a drive that answered
without a verdict — reads amber, and everything else, `FAILED` and `missing`
included, reads red. A check with no health reading at all shows an em dash.

Those columns read the current watch readings published by the latest daemon
cycle and rehydrated from persistent state after a daemon restart. File age is
the already formatted value used by `older_than`; SQL service checks expose their
observed scalar as `Value` and the effective comparison as `Condition` in their
readings, so a result such as `51 > 50` is shown without parsing event text.

Shared columns:

| Column | Meaning |
| --- | --- |
| Name | display name, falling back to name, capitalized |
| Last checked | latest completed daemon-cycle or manual sample |
| Last activity | latest watch event, such as a manual probe, notification or remediation |
| State | normalized watch state: `disabled` when config/monitor state excludes it from active checks, `starting` before the first monitored sample, `failed` for an active failure, `warning` for a failure the watch declared an advisory with `severity: warning` (amber row, kept out of the alert count), otherwise `ok`; active device work takes precedence as `testing`, `recovering`, `rebuilding`, `repairing`, `moving` or `merging`, and a device that stopped answering as `missing`, which reads as a failure |
| Actions | supported primary action plus an overflow menu for monitor/unmonitor |

While a manual `diskio`, `hdparm`, `lvm`, `raid` or `smart` sample is running, State shows
an amber **checking** badge, its elapsed time and the previous health state.
The action is disabled until completion. The Events feed records both the start
and the final result with its elapsed time. The UI shows a percentage only where
the underlying check reports real progress; a probe without such a source uses
the elapsed timer rather than a synthetic percentage. The probe is bounded by the
check's own `timeout:`, the same budget its scheduled cycle uses, falling back to
`engine.default_timeout` only for a check that declares none.

Interval, polarity (fires on fail / on threshold), hook and notifiers are not
table columns; they live in the row expansion's config grid and remain
searchable.

Row expansion:

| Area | Content |
| --- | --- |
| Config | type, category, interval, fires (on fail / on threshold), state, monitor flag, hook, notifiers, dry run |
| Readings | current host readings, then check conditions and thresholds |
| Activity | recent watch events |
| Expand | storage expansion action when configured |

Empty states:

- `No watches.`
- `No watches match the filter.`
- `No storage watches.`
- `No storage watches match the filter.`
- `No network watches.`
- `No network watches match the filter.`
- `No certificate watches.`
- `No certificate watches match the filter.`
- `No disk I/O watches.`
- `No disk I/O watches match the filter.`

## Events panel

Section id: `events-section`

| Part | Current representation |
| --- | --- |
| Title | `Events` plus dry-run note |
| Controls | guided service, watch, kind, status and time-range selects; absolute from/until date-time pickers; only errors, optional group actions, reset filters, optional `before` cutoff, clear log (admin) |
| Table | chronological event rows by default; optional client-side grouping by action |
| Limit | latest matching events; **load older** continues with a stable event-ID cursor |

Editable notes:

- Service/watch choices follow the currently known targets while kind/status
  use the daemon event vocabulary. The time-range presets request `since` from
  the backend. The absolute from/until pickers (local time) apply their exact
  bounds client-side; a set "from" also narrows the server fetch, since the
  API's `since` accepts only durations. Escape or **reset filters** clears
  every filter. The `only errors` checkbox refetches on change. Grouping stays
  client-side, optional and off by default; raw chronology is the default view.
- Event expansion state is keyed by the persisted event ID. Loading older rows
  appends a cursor page without duplicating events or shifting open rows.
- **clear log** (admin only) calls `POST /api/events/clear` after confirmation,
  matching `sermoctl events clear`. An optional **before** field passes
  `?before=TIME` (positive duration or non-future RFC3339) to prune only older
  rows.
- The `kind` filter covers the emitted event kinds: `action`, `suppressed`,
  `panic-suppressed`, `alert`, `error`, `warning` (what an advisory watch raises
  in place of `error` and `firing`), `firing`, `recovered`, `dry-run`,
  `reload` (a successful config reload of the running daemon),
  `hook`/`hook-failed`, `notify`/`notify-failed`/`notify-suppressed`,
  `expand`/`expand-skipped`/`expand-failed`, `kill`/`kill-failed`,
  `makestep`/`makestep-skipped`/`makestep-failed`, and `cascade`
  (a service operation triggered through a cascade action).

## Notifiers panel

Section id: `notifiers-section`

| Part | Current representation |
| --- | --- |
| Title | `Notifiers` plus total count |
| Visibility | hidden when no notifiers are configured |
| Columns | Name, Type, Destination, Watches, State, Actions |
| Actions | An administrator can send a clearly marked test message through one enabled notifier. |

Empty state:

- Hidden panel rather than an empty table.

## Daemon / Engine settings panel

Section id: `daemon-section`

| Block | Fields |
| --- | --- |
| Daemon | Backend, Host type, Config, Runtime, State |
| Engine | Interval, Max parallel checks, Max parallel ops, Default timeout, Operation timeout, Startup delay |
| Runtime | Started, Uptime, Go version, Ready |
| Process counters | PID, live CPU, memory, IO, FDs, threads |
| Process metrics | CPU, memory and IO charts with 1h/24h/7d/30d/1y windows |

Editable notes:

- This panel is informational. Config reload, **compact state** and the
  **panic mode** toggle live in the page footer (admin only).

### Panic mode

The footer's red **panic mode** button is the daemon-wide emergency switch. It
asks for confirmation (with a warning icon) in both directions so it is not
triggered by accident. While panic mode is on, the daemon status in the header
shows **`panic mode`** (red), a banner appears under the header, and the daemon
keeps monitoring while suppressing hooks, alert notifications and automatic
remediation. The same toggle is available from the CLI as `sermoctl panic
on|off|status`. See [cli.md](cli.md#panic-mode).

## Runtime locks panel

Section id: `locks-section`

| Part | Current representation |
| --- | --- |
| Title | `Runtime Locks` plus count |
| Visibility | hidden when no locks are returned |
| Release action | shown when the user can act and the lock is releasable |

Columns:

| Column | Meaning |
| --- | --- |
| Service | locked service |
| Name | lock name |
| State | active / stale / expired |
| TTL | remaining or configured TTL |
| Owner | owner PID/process info |
| Created | creation time |
| Blocks | blocked actions |
| Reason | operator-supplied reason |
| Action | release button when allowed |

## Action confirmation dialog

Dialog id: `action-confirm`

| Part | Current representation |
| --- | --- |
| Header | action title and service |
| Body | action warnings, preflight output, lock/remediation context |
| Footer | cancel, run preflight, confirm |

Safety note: this dialog must not bypass locks, guards, preflight or operation
timeouts. It only confirms actions that still go through the backend operation
engine.

## Change template

Copy this section when proposing a Web UI change.

```markdown
## Proposed Web UI change

### Panel

Services / Watches / Installed applications / Installed libraries / Events / Notifiers /
Daemon settings / Runtime locks / Service detail /
Action dialog / Overview

### Title

Current:
Wanted:

### Controls

Current:
Wanted:

### Columns or fields

Keep:
Remove:
Add:
Rename:
Order:

### Grouping / sorting / filters

Current:
Wanted:

### Row expansion or detail view

Current:
Wanted:

### Actions

Current:
Wanted:
Safety notes:

### Empty states

Current:
Wanted:
```
