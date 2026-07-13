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

## Data sources

| Area | Endpoint | Notes |
| --- | --- | --- |
| Current user | `GET /api/whoami` | role and action permissions |
| Dashboard snapshot | `GET /api/dashboard?since=WINDOW` | aggregate of the frequently refreshed service/runtime panels; the browser falls back to the individual endpoints if unavailable |
| Readiness | `GET /readyz?verbose` | daemon `status:` in the top bar (`starting` / `ok` / …) |
| Services | `GET /api/services` | configured runtime services loaded by sermod (not `sermoctl services` catalog inventory); `status_observed_at` identifies the real init-status sample behind a cached row |
| Service expansion | `GET /api/services/{name}` | checks, process info, rules |
| Service check metrics | `GET /api/services/{name}/metrics?check=NAME[&metric=KEY]` | latency chart when `metric` is omitted; named numeric metric series when present |
| Service runtime metrics | `GET /api/services/{name}/runtime` | read-only persisted service CPU/memory/IO history sampled exclusively by worker cycles |
| Service SLA | `GET /api/services/{name}/sla` | per-minute availability history for the service detail SLA timeline and API clients |
| Service events | `GET /api/services/{name}/events` | per-service event feed |
| Host watches | `GET /api/watches` | host-level watches |
| Applications | `GET /api/applications` | installed catalog apps; `observed_at` remains fixed while the version/status inventory is served from cache |
| Libraries | `GET /api/libraries` | installed catalog libraries; `observed_at` remains fixed while the file/version inventory is served from cache |
| Mount units | `GET /api/mounts` | storage watches with `mount:` backed by fstab |
| Notifiers | `GET /api/notifiers` | notifier targets |
| Daemon settings | `GET /api/daemon` | engine/runtime config |
| Daemon process metrics | `GET /api/daemon/metrics` | read-only persisted sermod CPU/memory/IO history sampled by the daemon independently of dashboard clients |
| Host metrics | `GET /api/host` | current host CPU, memory and load values |
| Locks | `GET /api/locks` | named runtime locks |
| Events | `GET /api/events` | service/watch activity; supports `limit`, `service`, `watch`, `kind`, `status`, `only_errors` |
| Activity summary | `GET /api/activity` | internal recent-event rollup used for dashboard attention indicators |
| Monitoring counts | `GET /api/monitoring` | monitored vs paused service counts |
| Live operations | `GET /api/ops` | active operation slots |

Init status, application inspection and SLA timeline caches expose their actual
sample times. The UI labels their ages, and SLA segment timestamps stay anchored
to `observed_at` instead of sliding forward on the browser clock while cached.
Dashboard refreshes are single-flight: automatic, manual and post-action reloads
never execute concurrently, and the next automatic delay starts after the prior
refresh completes.

## Action Endpoints

State-changing endpoints are CSRF-protected and admin-only when web auth is
enabled.

| Area | Endpoint | Notes |
| --- | --- | --- |
| Service action | `POST /api/services/{name}/{action}[?no_cascade=1]` | `monitor`, `unmonitor`, `start`, `stop`, `restart`, `reload`, `resume`; `reload` is offered only when the service reports `can_reload` from init backend reload support or a valid `reload:` fallback; `no_cascade` skips `also_apply` targets on start/stop/restart |
| Service preflight | `POST /api/services/{name}/preflight` | run preflight checks without changing service state |
| Watch action | `POST /api/watches/{name}/{action}` | `monitor`, `unmonitor`, `expand` |
| Mount action | `POST /api/mounts/{name}/{action}[?force=1&lazy=1&kill=1]` | `mount`, `umount`, `blockers`, `alert`; `force=1` allows `umount -f`, `lazy=1` allows `umount -l` as the last fallback, and `kill=1` enables `kill_only_if`-gated blocker signalling for `umount`; `/` rejects unmount paths |
| Lock release | `POST /api/locks/{service}/release?name=NAME` | releases inactive stale/expired named locks; active locks are refused |
| Events clear | `POST /api/events/clear?before=TIME` | clears persisted event/activity rows; `before` accepts a positive duration or non-future RFC3339 timestamp |
| State compact | `POST /api/state/compact?before=TIME` | prunes old SLA/metrics/event history and vacuums the state database; matches `sermoctl state compact` |
| Daemon reload | `POST /api/reload` | requests a `sermod` configuration reload |

## Top bar

| Element | Current representation |
| --- | --- |
| Brand | `Sermo` with status dot |
| Role | admin / read-only label |
| Find target | one autocomplete over loaded services, watches, applications and mounts; selection clears only that panel's filters and opens the target |
| Refresh | select with refresh interval, manual refresh button |
| Status | last complete refresh age, connection errors, or panels retaining older data after a partial refresh; `#statusbar` ends with host `uptime:` then daemon `status:` (`ok` / `starting` / …) as a paired tail |
| System status | host identity, host type, daemon/backend/runtime summary |

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

## Overview tiles

Rendered by `renderOverview` from already-loaded state, without extra requests.

| Tile kind | Current content |
| --- | --- |
| Services active | count / total for services in `started`, `collecting` or `monitored`; critical when any service is `failed`, warning while any service is `collecting`, neutral while any target is settling, otherwise active; click opens the matching `failed`, `starting` or `collecting` service filter when applicable |
| Watches | count / total; critical when any watch is `failed`, neutral while any target is settling (subtitle names starting watches, services or apps), otherwise quiet; click opens the matching `starting`/`failed` filter |
| Alerts | count of failing services, firing watches, failed installed apps and active locks, with a per-kind breakdown; click routes to `failed-services`, `failed-watches`, `failed-apps` or `locks-section` in priority order |
| Monitored | services in state `monitored` vs enabled services; warning while services are `collecting`, neutral with settling subtitle during startup, click opens the relevant service filter |
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

Signals include failing services, firing host watches, failed installed
applications, recent errors and readiness issues (including
`shutting_down`). A failing-services item opens the Services panel with the
`failed` filter; a firing-watches item opens Host watches with the `failed`
filter (`failed-watches` target); a failing-apps item opens Installed
applications with the `failed` filter (`failed-apps` target). Daemon startup
progress stays in the top-bar `status: starting` line, not in this box.

## Live operations

| Element | Current representation |
| --- | --- |
| Container | visible while operations are active/recent |
| Slot text | operation slots in use / total |
| Cards | action, service, state, elapsed time, message |

Session-local for operations started from the current browser; enriched with
`/api/ops` where available.

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
| Status filters | all, disabled, stopped, started, starting, collecting, monitored, failed |
| Sorting | Service, Category, State |
| Grouping | category group rows, collapsible |

Columns:

| Column | Meaning |
| --- | --- |
| Service | display name, falling back to name, capitalized |
| Category | YAML category or fallback |
| State | single normalized service state: `disabled`, `stopped`, `started`, `starting`, `collecting`, `monitored` or `failed` |
| Uptime | age of the oldest discovered service process, when available |
| CPU total | latest whole process-tree CPU usage; blank for `no_resident_process` services |
| Memory | latest process-tree resident memory; blank for `no_resident_process` services |
| FDs | open file-descriptor count from the process tree; blank for `no_resident_process` services |
| IO R/W | cumulative process-tree disk read/write bytes; blank for `no_resident_process` services |
| Actions | compact, individual state-aware icon buttons for start/stop, restart, reload, resume and monitor/unmonitor; reload is disabled when `can_reload` is false; the start/stop/restart confirm dialog offers **skip also_apply** when `also_apply` is set |

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
| General data | state, category, unit/backend, uptime, interval, policy, locks, last event, next remediation, remediation state and process totals; while the row badge is `starting`, expansion may still show the raw init backend (`inactive`) and in-flight check samples from the observe-only cycle |
| Graphs | full-width SLA timeline followed by latency, CPU, memory and IO charts; each service persists its own time window and latency check; `no_resident_process` services show only SLA because they have no process runtime to chart |
| Processes | full-width detected process tree table, with child processes marked in CMD and kept under their parent; omitted when `no_resident_process` is true |
| Checks | configured checks and current result |
| Named locks | runtime lock state |
| Rules | remediation/alert rule state |
| Preflight | inline preflight runner and results |
| Events | recent retained service events |

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
Before `umount` or `alert`, the UI asks `POST /api/mounts/{name}/blockers` and
shows a fresh process list for the path. The unmount dialog always shows the
blocker table; `kill blockers` is enabled only when `has_kill_policy` and
`can_kill` are true, and only rows marked `killable` can be signalled. `alert`
sends a native TTY message to logged-in blocking users. For `path: /`,
`GET /api/mounts` returns `can_umount: false`; the Web UI disables the
unmount-flow buttons and the API rejects `umount?kill=1` without scanning
blockers or sending signals.

## Host watch panels

Section ids: `storage-section`, `network-section`, `cert-section`,
`diskio-section`, `watches-section`

`Storage` contains `storage` watches, `Network` contains `net`/`icmp` watches,
`Certificate watches` contains `cert` watches, `Disk I/O watches` contains
`diskio` watches, and `Host watches` contains the remaining host watch types.

A `storage` watch summary shows the path, filesystem, mount point and used/free
space, plus — when any exist — the count of **open files** on that filesystem
(fds whose target resolves under the mount). That count comes from a cached
host-wide `/proc/<pid>/fd` scan shared by all storage watches and refreshed at
most once per minute; it is display only (no threshold/alert). The service list
row likewise shows a service's open file-descriptor count (`fds`) in its own
column, from the same per-process totals already in the service detail.

| Part | Current representation |
| --- | --- |
| Title | Panel name plus total count for that panel's watch subset |
| Title icons | group by panel type, collapse/expand all type groups (hidden when only one group exists) |
| Controls | search, type filter (per panel, see below), state filters, showing count |
| Type filter | panel-specific `all ... types` plus the distinct values currently present in that panel; Storage filters by filesystem type (all its watches share one check type), Certificate watches by public-key algorithm; the selector is hidden when only one value exists |
| Grouping | collapsible rows by the same panel-specific type used by the type filter |
| State filters | all, disabled, ok, starting, failed |
| Search | display name, raw name, category, type, summary, interval, polarity, hook state/command, notifier names, expand/dry-run/monitoring state and conditions |
| Sorting | every data column except Actions is sortable independently inside its check-type table; each table defaults to Name ascending |
| Visibility | hidden when no watches are configured for that panel's subset |

Host watches are grouped as System, Storage, Network and Security, then split
into a check-type table. Every type table ends with Last checked, Last activity,
State and Actions; it does not use a generic Summary column. Last checked is the
latest completed daemon-cycle or manual sample, while Last activity is an event.

| Check type | Type-specific columns |
| --- | --- |
| `storage` | Name, Usage, Filesystem, Mount point; filters by filesystem when more than one is present |
| `file` | Name, Path, current age, configured age limit |
| `net` | Name, interface, link, speed, errors |
| `hdparm` | Name, device, buffered read, cached read |
| `lvm` | Name, health, VG, LV, VG size, VG free, reasons |
| `smart` | Name, device, health, temperature, wear, formatted power-on time |
| `diskio` | Name, device, utilization, read, write, await |
| `cert` | Name, source, days left, expiry, issuer |
| `raid` | Name, array, size, degraded, recovering |
| Other types | Name and their primary live value |

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
| State | normalized watch state: `disabled` when config/monitor state excludes it from active checks, `starting` before the first monitored sample, `failed` for an active failure, otherwise `ok`; active device work takes precedence as `testing`, `recovering`, `rebuilding`, `repairing`, `moving` or `merging` |
| Actions | supported primary action plus an overflow menu for monitor/unmonitor |

While a manual `hdparm`, `lvm`, `raid` or `smart` sample is running, State shows
an amber **checking** badge, its elapsed time and the previous health state.
The action is disabled until completion. The Events feed records both the start
and the final result with its elapsed time. The UI shows a percentage only where
the underlying check reports real progress; a probe without such a source uses
the elapsed timer rather than a synthetic percentage.

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
| Controls | guided service, watch, kind, status and time-range selects; only errors, optional group actions, reset filters, optional `before` cutoff, clear log (admin) |
| Table | chronological event rows by default; optional client-side grouping by action |
| Limit | latest matching events; **load older** continues with a stable event-ID cursor |

Editable notes:

- Service/watch choices follow the currently known targets while kind/status
  use the daemon event vocabulary. The time-range presets request `since` from
  the backend. Escape or **reset filters** clears every filter. The `only
  errors` checkbox refetches on change. Grouping stays client-side, optional and
  off by default; raw chronology is the default view.
- Event expansion state is keyed by the persisted event ID. Loading older rows
  appends a cursor page without duplicating events or shifting open rows.
- **clear log** (admin only) calls `POST /api/events/clear` after confirmation,
  matching `sermoctl events clear`. An optional **before** field passes
  `?before=TIME` (positive duration or non-future RFC3339) to prune only older
  rows.
- The `kind` filter covers the emitted event kinds: `action`, `suppressed`,
  `panic-suppressed`, `alert`, `error`, `firing`, `recovered`, `dry-run`,
  `reload` (a successful config reload of the running daemon),
  `hook`/`hook-failed`, `notify`/`notify-failed`/`notify-suppressed`,
  `expand`/`expand-skipped`/`expand-failed`, `kill`/`kill-failed`, and `cascade`
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

Services / Host watches / Installed applications / Installed libraries / Events / Notifiers /
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
