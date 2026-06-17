# Native disk volume auto-expansion (watch action) - historical plan

> Historical implementation plan. Native storage-watch `then.expand` is
> implemented; use [`docs/configuration.md`](../../configuration.md#thenexpand--volume-growth-storage-watches)
> for current behavior.

**Goal:** When a `disk` watch detects low space on an LVM-backed filesystem,
Sermo automatically grows the volume **natively in Go** — orchestration in Go,
shelling out only to the LVM/filesystem tools that have no Go API (`lvs`, `vgs`,
`lvextend`, `resize2fs`, `xfs_growfs`, `btrfs`). Repeated firing is bounded by
reusing the existing remediation `Policy` (cooldown/backoff/max_actions).

**Decisions locked with the user:**
- v1 scope: **LVM + ext4/xfs/btrfs core only.** No DRBD, KVM, app-specific
  cleanups, or email. (Email → existing `notifiers`; cleanups → a pre-hook;
  DRBD/KVM → possible later phases.)
- Trigger/config: a **native `then.expand` action on the disk watch**, not a
  hook and not a new disk-check option.
- Anti-repeat: **reuse `rules.Policy`** (`policy:` block on the watch).
- Native Go orchestration; external commands only where Go has no API.

**Non-goals (v1):** DRBD remote resize, libvirt/KVM resume, `/var/www` &
`/var/spool/exim` cleanups, email reports, non-LVM volumes (plain partitions),
shrinking. These are out of scope and must fail clearly rather than guess.

---

## Architecture

### New package `internal/volume`
Pure orchestration, all external calls through an injected `execx.Runner`
(testable, argv-only, timeouts), mirroring `servicemgr`/`checks`.

- `type Target struct { Path, Mountpoint, FSType, Device, VG, LV string }`
- `Resolve(path) (Target, error)` — **native**: read `/proc/self/mountinfo`
  (reuse the parsing already in `internal/checks/mount.go`) to get the
  mountpoint, fstype and mount-source device for `path`. Then resolve the device
  to its VG/LV with `lvs --noheadings -o vg_name,lv_name --separator=, <device>`
  (LVM has no Go API). A non-LVM device → a clear `not an LVM volume` error.
- `vgFreeBytes(vg) (int64, error)` — `vgs --noheadings -o vg_free --units b
  --nosuffix <vg>`, parsed to bytes.
- `Expand(ctx, t Target, by int64) (Result, error)`:
  - `grow = by`; if `grow > vgFree` → `grow = vgFree` (use all free, like the
    script); if `vgFree == 0` → error `no free space in VG <vg>`.
  - `lvextend -L+<grow>b /dev/<vg>/<lv>`.
  - grow FS by type: `ext2|ext3|ext4` → `resize2fs <device>`; `xfs` →
    `xfs_growfs <mountpoint>`; `btrfs` → `btrfs filesystem resize max
    <mountpoint>`. Unknown fstype → error.
  - return `Result{GrewBytes, NewSizeBytes, VG, LV, FSType}` for events/notify.

All five tools (`lvs`, `vgs`, `lvextend`, `resize2fs`, `xfs_growfs`, `btrfs`)
are added to the **AGENTS.md justified external-process exceptions** list (no
native Go API; the orchestration/decision logic is all Go).

### Size parsing
No size parser exists in the repo. Add `parseSize("5G") (int64, error)` (accepts
`K/M/G/T`, decimal optional, binary units i.e. 1G = 2^30) next to the existing
field parsers used by watch_build (e.g. in `internal/app/watch_build.go` or a
small `internal/app/size.go`).

### Watch integration (`internal/app/watch.go`, `watch_build.go`)
- New optional config on a watch's `then`:
  ```yaml
  watches:
    expand-backup:
      check: { type: disk, path: /mnt/backup, free_pct: { op: "<", value: 10 } }
      for: { cycles: 3 }
      policy: { cooldown: 30m, backoff: { initial: 30m, factor: 2, max: 6h } }
      then:
        expand: { by: 5G }      # native action
        notify: [ops-email]     # optional: report what happened
  ```
- `Watch` gains: `Expand *ExpandSpec` (`{ By int64 }`), `Policy rules.Policy`,
  and in-memory `policyState rules.RemediationState` (lives across cycles on the
  Watch, like the existing `state rules.WindowState`).
- `RunCycle`: after `state.Fires(...)` is true and before/alongside hook/notify,
  if `Expand != nil`:
  1. `if allowed, reason := Policy.Allow(&policyState, now); !allowed` → emit a
     `expand-skipped` event (`reason`: cooldown/rate limit) and return (still
     notify if configured? → notify only on actual action to avoid spam).
  2. Resolve the target from the check's path (`res.Data["path"]`), run
     `volume.Expand`, `policyState.Record(now, Policy)`.
  3. Emit `expand` (success, with grew/new-size) or `expand-failed` event;
     dispatch notify with the outcome message.
- The disk path comes from the check Result `Data["path"]`; expand only makes
  sense for `disk` checks, so building `then.expand` on a non-disk watch is a
  config warning.

### Wiring
- `watch_build.go`: parse `then.expand` (size via `parseSize`) and the watch
  `policy:` block (via `rules.ParsePolicy`) for both the single-check builder
  (~line 99) and the multi-metric builder (~line 167). A `volume.Expander`
  (with `deps.ExecxRunner`) is injected onto the Watch.

---

## Tasks (TDD, each: write failing test → run → implement → run → stop)

> Per the user's review preference: implement and STOP. Do not commit; leave the
> diff for review.

### Task 1 — `parseSize`
- Test: `5G`→5*2^30, `500M`, `2T`, `1024`, bad input errors.
- Implement in `internal/app/size.go`.

### Task 2 — `volume.Resolve` (native mount + LVM device→VG/LV)
- Refactor: expose the mountinfo parsing from `internal/checks/mount.go` (or
  duplicate minimally) so `volume` can read mountpoint/fstype/device for a path.
- Test with an injected runner + a fake mountinfo source: a path under an LVM
  mount resolves to `{Mountpoint, FSType, Device, VG, LV}`; a non-LVM device
  errors clearly.
- Implement `Resolve` (native mountinfo + `lvs` for VG/LV).

### Task 3 — `volume.Expand` (vgs free, lvextend, grow FS)
- Test (fake runner records argv):
  - ext4: asserts `vgs … vg_free`, `lvextend -L+<n>b /dev/vg/lv`, `resize2fs
    <device>`, in order.
  - xfs → `xfs_growfs <mountpoint>`; btrfs → `btrfs filesystem resize max
    <mountpoint>`.
  - requested `by` > free → caps to free; free == 0 → error, no lvextend.
  - unknown fstype → error before any command.
- Implement `vgFreeBytes` + `Expand`.

### Task 4 — Watch `expand` action + `policy` gating
- Test (`internal/app/watch_test.go`): a disk watch with `then.expand` and a
  `policy.cooldown` runs `volume.Expand` once on fire, records the action, and
  on the next firing cycle within cooldown is **skipped** (no second expand);
  emits `expand`/`expand-skipped` events. Uses an injected fake expander.
- Implement: add fields to `Watch`, gate in `RunCycle`, emit events.

### Task 5 — Builder wiring
- Test: `BuildWatches` parses `then.expand: { by: 5G }` + `policy:` into a Watch
  with the expander set; `then.expand` on a non-disk check warns.
- Implement parsing in `watch_build.go` (both builders) + inject
  `volume.Expander{Runner: deps.ExecxRunner}`.

### Task 6 — Docs
- `docs/configuration.md` (Host watches): document `then.expand: { by }` + the
  watch-level `policy:` block, with the example above and the LVM/ext4/xfs/btrfs
  scope + "uses all free space when `by` exceeds VG free; alerts when the VG is
  full".
- `AGENTS.md`: add `lvs/vgs/lvextend/resize2fs/xfs_growfs/btrfs` to the justified
  external-process exceptions, and note the native `expand` watch action.

### Task 7 — Full verification
- `gofmt -l`, `go build ./... && go test ./...`, `govulncheck`, `staticcheck`,
  `revive`, `golangci-lint` (gosec) — all clean. STOP for user review (no commit).

---

## Open questions / risks
- **Concurrency:** within one daemon, watch cycles run sequentially, so two
  expansions won't overlap; the `policy.cooldown` (≫ expansion time) is the
  guard. No extra lock needed in v1 (note it).
- **`free_pct` vs `used_pct`:** for expansion, triggering on `free_pct < N` (or
  `free_bytes`) is usually more meaningful than `used_pct`; both work — doc uses
  `free_pct`.
- **Failure visibility:** an `lvextend` that succeeds but `resize2fs` that fails
  leaves the LV grown but FS not — the event/notify must report the exact step
  that failed (the `Result`/error carries the stage).
- **DRBD/KVM/cleanups/email:** explicitly deferred; expansion on a DRBD-backed
  or non-LVM device must error clearly, not half-act.
