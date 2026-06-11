# `sermoctl wizard` — assisted check creation — design

Date: 2026-06-09. Status: approved, implementation pending. Not committed (user reviews).

## Goal

An interactive, **extensible** assistant that generates watch config. v1 ships two
assistants — **volume** (disk watches, with notify thresholds and optional native
`then.expand`) and **net** (network-interface watches) — behind a shared skeleton
so new check types are added by implementing one interface.

## Command

`sermoctl wizard [<name>]`. With no name it lists the registered assistants and
the user chooses; `sermoctl wizard volume` / `sermoctl wizard net` go straight in.
The generated `watches:` block is printed, then the user is asked whether to merge
it into the global `sermo.yml` (a `.bak` is written first; collisions abort).

## Package `internal/assist` (engine, CLI-independent)

- **`Prompt`** wraps an `io.Reader`+`io.Writer`; helpers re-prompt on bad input:
  `Ask(q, def)`, `Confirm(q, def)`, `Choose(q, opts)` (single, 1-based input →
  0-based index), `MultiChoose(q, opts)` (`1,3`, `all`, or an option name such as
  `none`/`default`), `AskInt(q, def)`,
  `AskNonEmpty(q)`.
- **`Env`** carries dependencies without CLI coupling:
  ```go
  type Volume struct{ Mountpoint, FSType, Device string }
  type Iface  struct{ Name string; Up, Loopback bool }
  type Env struct {
      Notifiers []string
      Volumes   func() ([]Volume, error)
      Ifaces    func() ([]Iface, error)
  }
  ```
- **`Result`** = `{ Watches map[string]any; Summary string }` — `Watches` is the
  map merged under `watches:` (watch-name → entry).
- **`Assistant`** interface: `Name() string`, `Title() string`,
  `Run(p *Prompt, env Env) (Result, error)`.
- **Registry**: ordered `[]Assistant` = {`volume`, `net`}; `Assistants()`,
  `Lookup(name)`.

## Volume assistant (`internal/assist/volume.go`)

1. List candidate volumes (`Env.Volumes`), multi-select.
2. Per volume (with "apply to all" shortcut): notify metric (`free_pct` `<` /
   `used_pct` `>=`) + value; notifier(s) from `Env.Notifiers`; `for` cycles
   (default 3); optional auto-expand → `by` size (string, e.g. `5G`) + a
   `policy.cooldown` (default `30m`). Expand is offered with a note that it needs
   an LVM volume (the runtime validates; the wizard does not call LVM tools).
3. Emit per volume:
   ```yaml
   disk-mnt-backup:
     check: { type: disk, path: /mnt/backup, free_pct: { op: "<", value: 10 } }
     for: { cycles: 3 }
     policy: { cooldown: 30m }      # only with expand
     then: { notify: [ops-email], expand: { by: 5G } }
   ```
   At least one action (notify or expand) is required.

## Net assistant (`internal/assist/net.go`)

1. List interfaces (`Env.Ifaces`, loopback excluded by default), multi-select.
2. Per interface (with "apply to all"): choose metrics (multi-select):
   - **state** → `on: change` (default) or `expect: down`.
   - **errors** → `delta: { op: ">", value: N }` (default 100; default counters).
   - **speed** → `on: change` (offered, off by default).
   Then notifier(s).
3. Emit the multi-metric shape:
   ```yaml
   net-eth0:
     check: { type: net, interface: eth0 }
     metrics:
       state:  { on: change, then: { notify: [ops-email] } }
       errors: { delta: { op: ">", value: 100 }, then: { notify: [ops-email] } }
   ```
   Matches `validateNetCheck`: interface required, metrics ∈ {state, speed,
   errors}, each with its own `then`.

## CLI (`internal/cli/wizard.go`)

- Add `Stdin io.Reader` to `App` (default `os.Stdin` in `Main`).
- `runWizard`: build `Env` (notifier names from `cfg.Global.Raw["notifiers"]`;
  `Volumes` from `volume.List`; `Ifaces` from `net.Interfaces`); pick/dispatch the
  assistant; print the YAML (`goccy/go-yaml`); offer to merge.
- `mergeWatches(globalPath, fragment)`: read bytes → unmarshal root map → merge
  into `root["watches"]` (collision → error) → write `<path>.bak` (original
  bytes) → marshal → write `0644`. Comments/order are not preserved (documented;
  hence the backup + printed preview + confirmation).
- Dispatch: add `case "wizard": return a.runWizard(ctx, opts)`.

## Supporting native helpers

- `volume.List() ([]Mount, error)` — real disk mounts (`Device` starts with
  `/dev/`), pseudo filesystems skipped. Native `/proc/mounts`.
- Interface listing via stdlib `net.Interfaces()` (name, up, loopback). Native.

## Testing

- `Prompt`: scripted `strings.Reader` stdin + captured stdout; selection parsing,
  defaults, re-prompt on bad input.
- `volume` / `net` assistants: fake `Env` + scripted answers → assert the produced
  `Watches` map (paths, thresholds, notify, expand+policy / metrics).
- CLI: `runWizard` with scripted stdin + a temp `sermo.yml` → assert printed YAML
  and that merge writes the file and a `.bak`; collision aborts.

## Out of scope (v1)

Editing existing watches, non-watch config (services), preserving comments on
merge, LVM verification inside the wizard, assistants beyond volume/net.
