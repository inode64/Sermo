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

## Global rules

- The Web UI is one embedded document: `internal/web/index.html`.
- Data panels are `<details>` cards. The page scrolls as a whole; do not add
  panel-local scrollbars.
- Services and Applications can be filtered, sorted and grouped by `category`.
- A top-level YAML `category` field is the category source. If it is absent,
  services fall back to `service` and applications fall back to `app`.
- State-changing buttons use the same safe backend path as `sermoctl`.

## Data sources

| Area | Endpoint | Notes |
| --- | --- | --- |
| Current user | `GET /api/whoami` | role and action permissions |
| Readiness | `GET /readyz?verbose` | startup / shutdown banner |
| Services | `GET /api/services` | main service list |
| Service expansion | `GET /api/services/{name}` | checks, process info, rules |
| Service check metrics | `GET /api/services/{name}/metrics?check=NAME[&metric=KEY]` | latency chart when `metric` is omitted; named numeric metric series when present |
| Service runtime metrics | `GET /api/services/{name}/runtime` | persisted service CPU/memory/IO history sampled by worker cycles |
| Service SLA | `GET /api/services/{name}/sla` | per-minute availability history for the service detail SLA timeline and API clients |
| Service events | `GET /api/services/{name}/events` | per-service event feed |
| Host watches | `GET /api/watches` | host-level watches |
| Applications | `GET /api/applications` | installed catalog apps |
| Notifiers | `GET /api/notifiers` | notifier targets |
| Daemon settings | `GET /api/daemon` | engine/runtime config |
| Daemon process metrics | `GET /api/daemon/metrics` | persisted sermod CPU/memory/IO history |
| Host metrics | `GET /api/host` | current host CPU, memory and load values |
| Locks | `GET /api/locks` | named runtime locks |
| Events | `GET /api/events` | service/watch activity; supports `limit`, `service`, `watch`, `kind`, `status`, `only_errors` |
| Recent activity | `GET /api/activity` | summary of recent events |
| Monitoring counts | `GET /api/monitoring` | monitored vs paused service counts |
| Diagnostics | `GET /api/diagnostics` | backend/runtime diagnostics |
| Live operations | `GET /api/ops` | active operation slots |

## Action Endpoints

State-changing endpoints are CSRF-protected and admin-only when web auth is
enabled.

| Area | Endpoint | Notes |
| --- | --- | --- |
| Service action | `POST /api/services/{name}/{action}` | `monitor`, `unmonitor`, `start`, `stop`, `restart`, `reload`, `resume`; service operations use the safe engine |
| Service preflight | `POST /api/services/{name}/preflight` | run preflight checks without changing service state |
| Watch action | `POST /api/watches/{name}/{action}` | `monitor`, `unmonitor`, `expand` |
| Lock release | `POST /api/locks/{service}/release?name=NAME` | releases inactive stale/expired named locks; active locks are refused |
| Events clear | `POST /api/events/clear?before=TIME` | clears persisted event/activity rows; `before` accepts RFC3339 or duration |
| Diagnostics clean | `POST /api/diagnostics/clean` | removes stale control state for unconfigured targets; metric/SLA/event history is kept; returns 404 when diagnostics are disabled |
| Daemon reload | `POST /api/reload` | requests a `sermod` configuration reload |

## Top bar

| Element | Current representation |
| --- | --- |
| Brand | `Sermo` with status dot |
| Role | admin / read-only label |
| Refresh | select with refresh interval, manual refresh button |
| Status | last refresh age, connection errors, ready state |
| System status | daemon/backend/runtime summary |

Editable notes:

- Keep top bar compact and sticky.
- Do not move operational controls into marketing-style hero blocks.
- Refresh controls should stay visible on narrow screens.
- The `uptime:` reading in the status line is the **host/server** uptime (from
  `/proc/uptime`, surfaced as `host_uptime` on `GET /api/daemon`), not the sermod
  process uptime. The sermod process uptime stays on the Daemon panel and
  `GET /livez?verbose`.

## Overview tiles

Rendered by `renderOverview` from already-loaded state, without extra requests.

| Tile kind | Current content |
| --- | --- |
| Services up | count / total, warning when degraded |
| Watches | count and failing state |
| Alerts | errors / critical signals |
| Monitored | monitored vs unmonitored services |
| Host gauges | memory, load, fds, pids, conntrack, etc. when present |
| Volumes | one gauge per mounted disk/volume watch, crit when its watch is firing |

Editable notes:

- Tiles should jump to the related panel.
- Usage bars stay at the bottom of each tile.
- Do not add explanatory text inside tiles.

## Attention required

| Element | Current representation |
| --- | --- |
| Container | visible only when signals exist |
| Items | warning / critical buttons |
| Click behavior | opens the related panel |

Signals include failing services, firing host watches, recent errors and
readiness issues. A failing-services item opens the Services panel with the
`failed` filter; a firing-watches item opens Host watches with the `failed`
filter (`failed-watches` target).

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

| Part | Current representation |
| --- | --- |
| Title | `Services` plus total count |
| Title icons | group by category, collapse/expand all groups |
| Controls | search, category select, status filters, showing count |
| Status filters | all, disabled, running, paused, stopped, unmonitorized, monitorized, failed |
| Sorting | Service, Category, State |
| Grouping | category group rows, collapsible |

Columns:

| Column | Meaning |
| --- | --- |
| Service | display name, falling back to name, capitalized |
| Category | YAML category or fallback |
| State | normalized state plus monitor hint |
| Uptime | age of the oldest discovered service process, when available |
| CPU total | latest whole process-tree CPU usage |
| Memory | latest process-tree resident memory |
| IO R/W | cumulative process-tree disk read/write bytes |
| Actions | start, stop, restart, reload, resume, monitor/unmonitor |

Row expansion:

| Area | Content |
| --- | --- |
| General data | state, category, unit/backend, uptime, interval, policy, locks, last event, next remediation, remediation state and process totals |
| Graphs | full-width SLA timeline followed by latency, CPU, memory and IO charts; all use the shared `1h`, `24h`, `7d`, `30d`, `1y` window selector |
| Processes | full-width detected process tree table, with child processes marked in CMD and kept under their parent; omitted when `no_resident_process` is true |
| Checks | configured checks and current result |
| Named locks | runtime lock state |
| Rules | remediation/alert rule state |
| Preflight | inline preflight runner and results |
| Events | recent retained service events |

Empty states:

- `No services.`
- `No services match the filter.`

## Installed applications panel

Section id: `apps-section`

| Part | Current representation |
| --- | --- |
| Title | `Installed applications` plus total count |
| Title icons | group by category, collapse/expand all groups |
| Controls | search, category select, showing count |
| Sorting | Application, Category, Status, Version |
| Visibility | hidden when no installed apps are returned |
| Grouping | category group rows, collapsible |

Columns:

| Column | Meaning |
| --- | --- |
| Application | display name, falling back to name, capitalized |
| Category | YAML category or fallback |
| Status | app inspection state (`Ok`, warning, failed) |
| Version | short version, falling back to raw version |

Row expansion:

| Field | Meaning |
| --- | --- |
| Version | full version output |
| Category | YAML category or fallback |
| Location | resolved binary path |
| Permissions | mode string |
| User | binary owner |
| Group | binary group |
| Status | app inspection status |

Empty state:

- `No applications match the filter.`

## Host watch panels

Section ids: `storage-section`, `network-section`, `watches-section`

`Storage` contains `storage`/`disk` watches, `Network` contains `net`/`icmp`
watches, and `Host watches` contains the remaining host watch types.

| Part | Current representation |
| --- | --- |
| Title | Panel name plus total count for that panel's watch subset |
| Controls | search, type filter, state filters, showing count |
| Type filter | panel-specific `all ... types` plus the distinct check types currently present in that panel |
| State filters | all, disabled, ok, monitorized, unmonitorized, failed |
| Sorting | Name, Type, Summary, Interval, Polarity, Hook, Notifiers, Last activity, State |
| Visibility | hidden when no watches are configured for that panel's subset |

Columns:

| Column | Meaning |
| --- | --- |
| Name | display name, falling back to name, capitalized |
| Type | check type |
| Summary | watch-specific status summary |
| Interval | resolved watch interval |
| Polarity | fires on fail / fires on condition |
| Hook | configured hook state |
| Notifiers | configured notifier count/list |
| Last activity | latest hook/notify activity |
| State | normalized watch state |
| Actions | monitor/unmonitor and supported actions |

Row expansion:

| Area | Content |
| --- | --- |
| Config | check conditions and thresholds |
| Readings | current host readings |
| Activity | recent watch events |
| Expand | storage expansion action when configured |

Empty states:

- `No watches.`
- `No watches match the filter.`
- `No storage watches.`
- `No storage watches match the filter.`
- `No network watches.`
- `No network watches match the filter.`

## Events panel

Section id: `events-section`

| Part | Current representation |
| --- | --- |
| Title | `Events` plus shadow-event note |
| Controls | service, watch, kind, status, only errors, group actions, clear |
| Table | event rows grouped by action when enabled |
| Limit | latest matching events |

Editable notes:

- Service/watch/kind/status filter live as the operator types (300ms debounce),
  matching the services and watches panels; Enter applies immediately, Escape
  clears. The `only errors` checkbox refetches on change. Grouping stays
  client-side and optional; raw chronology is still useful.
- The `kind` filter covers the emitted event kinds: `cycle`, `action`,
  `suppressed`, `shadow`, `alert`, `error`, `firing`, `recovered`, `dry-run`,
  `hook`/`hook-failed`, `notify`/`notify-failed`, `expand`/`expand-skipped`/`expand-failed`,
  and `cascade` (a service operation triggered through a cascade action).

## Notifiers panel

Section id: `notifiers-section`

| Part | Current representation |
| --- | --- |
| Title | `Notifiers` plus total count |
| Visibility | hidden when no notifiers are configured |
| Columns | Name, Type, State |

Empty state:

- Hidden panel rather than an empty table.

## Daemon / Engine settings panel

Section id: `daemon-section`

| Block | Fields |
| --- | --- |
| Daemon | Backend, Config, Runtime, State |
| Engine | Interval, Max parallel checks, Max parallel ops, Default timeout, Operation timeout, Startup delay |
| Runtime | Started, Uptime, Go version, Ready |
| Process counters | PID, live CPU, memory, IO, FDs, threads |
| Process metrics | CPU, memory and IO charts with 1h/24h/7d/30d/1y windows |

Editable notes:

- This panel is informational. Avoid action buttons here except config reload,
  which currently lives in Diagnostics.

## Recent activity panel

Section id: `activity-section`

| Field | Meaning |
| --- | --- |
| Service actions | recent service operation count |
| Watch hooks | recent hook count |
| Watch notifies | recent notifier count |
| Errors | recent error count |
| Last activity | newest activity summary |

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

## Service row expansion

Container: `tr.exp-row` under the selected service row.

Opened from a service row/name. There is no separate lower detail panel.

| Area | Current representation |
| --- | --- |
| Header | service name, unit and state |
| Actions | service row operation buttons and inline preflight |
| Checks | resolved check state |
| Metrics | selectable metric/check series |
| Events | recent service events |
| Rules | remediation and alert rules |

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

## Diagnostics panel

Section id: `diag-section`

| Part | Current representation |
| --- | --- |
| Title | `Diagnostics` |
| Buttons | refresh; clean stale data when admin and stale database findings exist; reload config when admin |
| Help text | `sermoctl daemon reload`; cleanup warning for stale control state only |
| Table | diagnostic time, level, scope and message rows |

## Change template

Copy this section when proposing a Web UI change.

```markdown
## Proposed Web UI change

### Panel

Services / Host watches / Installed applications / Events / Notifiers /
Daemon settings / Recent activity / Runtime locks / Service detail /
Action dialog / Diagnostics / Overview

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
