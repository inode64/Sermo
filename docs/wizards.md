# Wizards (`sermoctl wizard`)

The interactive wizard generates Sermo configuration — a host **watch**
(`volume`, `net`, `uplink`) or a monitored **service**. Every assistant lives in
`internal/assist/` and is driven from `internal/cli/wizard.go`.

This document defines the **one question flow all wizards follow** — present and
future. It exists so the same mistakes are not repeated: divergent orders,
hand-rolled prompts, name-typing where detection should drive, or notifier
questions that dead-end. When you add or change an assistant, keep to this flow
and the invariants below, and update this file in the same change.

## The canonical flow

1. **Wizard type.** `sermoctl wizard <type>` runs that assistant; with no type,
   the wizard lists them and asks (`selectAssistant`). Never require the type.
2. **Select detected targets.** Each assistant detects what is monitorable
   (services → active installed catalog daemons first, then optional active
   units with no catalog profile; `volume` → mounts; `net`/`uplink` →
   interfaces) and offers them with `Prompt.MultiChoose`. **Never ask the
   operator to type a name** — the target's identity comes from detection. The
   service assistant completes the catalog group before asking about uncataloged
   units.
3. **Per-service properties (services only).** For each selected catalog service,
   ask only the properties that legitimately differ per service, such as a port
   override. PID/process ownership belongs in the catalog daemon under
   `catalog/services`, so generated catalog service entries should normally only
   write `uses:` plus explicit overrides. For active units with no catalog
   profile, ask the **PID source** because there is no daemon profile to inherit:
   a pidfile path writes `pidfile:`; with no pidfile, an executable derived from
   the unit offers a `command_match` process selector.
4. **Batch.** When more than one target was selected, ask once whether to apply
   the following shared answers to all of them (`Prompt.Confirm`).
5. **Monitor state.** `Prompt.AskMonitorState` → `monitor: enabled | disabled |
   previous`.
6. **Interval.** `Prompt.AskInterval` → `interval:` (blank inherits the global
   engine interval). Steps 5–6 are `Prompt.AskMonitoring`.
7. **Wizard-specific options.** For watches: thresholds (`volume`), metrics
   (`net`), probes (`uplink`), the **notifier** question (`chooseNotifiers`),
   and the optional `then.dry_run` rehearsal flag when the generated `then`
   block has a real action to skip. For services: ask whether service
   remediation should start in `shadow` mode (`remediation.shadow: true`) after
   the shared monitor/interval answers.
8. **Preview & accept.** Render the YAML that will be written and confirm.
9. **Cleanup.** Offer to delete managed files whose target is **no longer
   detected** on the host (`planWizardWatchDeletes` / `planStaleServiceDeletes`).

Steps 5–7 are gathered once and reused for all targets when step 4 was accepted;
otherwise they are asked per target.

## Invariants (do not break these)

- **Shared prompts only.** Use the `Prompt` helpers in
  `internal/assist/prompt.go` and `common.go`; never hand-roll a question or its
  re-prompt/EOF handling.
- **Forced yes/no.** `Prompt.Confirm` forces an explicit `y`/`n`: an empty line
  re-prompts, it does not silently take a default. (EOF aborts with
  `ErrInputClosed`, like every required prompt.)
- **No name typing in choices.** Selection is by number, `all`, or an existing
  option's name. The wizard never invents or asks for a new name.
- **`all` / `none` / `default` vocabulary.** `all` selects everything; `none`
  opts out; `default` inherits the global setting.
- **`none` and `default` are always selectable** — even when the config defines
  zero notifiers. The wizard must never block on the notifier question.
  - `none` → monitor-only watch (`notify: [none]`: state and events, no
    delivery), always accepted.
  - `default` → inherits the global notify when one is configured; when there is
    nothing to inherit it **degrades to monitor-only** with a one-line note. It
    must never re-ask or abort. This logic lives once in `chooseNotifiers`
    (`internal/assist/notify.go`) — do not reimplement it per assistant.

  Hand-written configs have an additional form: omitting the `then` key entirely
  on a watch (or per-metric block) is also valid and produces alert-only
  behaviour (firing state visible in web UI + "firing" events in logs, but no
  hook/notify and no inheritance of globals). The wizard always generates an
  explicit `then` (using `none` / `default` / names as chosen).
- **Monitor + interval everywhere.** Every generated entry carries the step-5/6
  answers via `Monitoring.apply` (`internal/assist/common.go`).
- **Dry-run is watch-local.** Watch assistants ask for `then.dry_run` only when
  the chosen `then` block has a real side effect (`notify`, inherited global
  notify, or native `expand`). `dry_run` never stands alone as the only action.
- **Shadow is service-local.** The service assistant asks whether to write
  `remediation: {shadow: true}` for each generated service (or once for the
  selected batch). This rehearses service remediation rules and operations; it
  does not suppress host watch actions.
- **Batch service setup avoids port noise.** When several catalog services are
  selected, the service assistant asks whether to review per-service port
  overrides. The default is no: generated services inherit catalog ports and the
  wizard moves straight to the shared monitor/interval/shadow questions. Select
  review only when the host runs a service on a non-catalog port.
- **Interface shortcuts.** Network assistants accept the keyword `active` at the
  interface multi-select prompt to pick only currently up non-loopback
  interfaces. The uplink assistant also accepts `default` when a default-route
  interface is detected; use it to generate route/ping/DNS checks for the
  current internet egress instead of hand-picking tunnel or helper interfaces.
- **Detection drives cleanup.** Step 9 only offers files whose target is absent
  from the current detection; with detection unavailable it offers nothing, so a
  valid file is never proposed for deletion.
- **Generated config must validate.** `internal/assist/contract_test.go`
  round-trips every builder's output through `config.Validate`. Keep it green.

## PID detection (services)

`servicemgr.DetectProc` derives a stable pidfile path and main executable from
the service's init definition, best-effort (unknown fields come back `""`):

- **systemd**: `systemctl show` `PIDFile` and `ExecStart` (the `path=` token).
- **OpenRC**: the init script and its `conf.d` override — `pidfile=`, a
  `start-stop-daemon --pidfile`, `--exec`, `command=`, `command_user=`, and
  simple OpenRC variables/defaults (`${RC_SVCNAME}`, `${VAR:-default}`).
  Unknown `$`-built paths are skipped; runtime `/run/openrc/daemons/<unit>/001`
  options may fill dynamic pidfiles/executables.

Detected pidfile and socket paths must be written with canonical `/run`
spelling. If the backend reports `/var/run/...`, normalize it to `/run/...`
before adding it to `catalog/services` or a generated uncataloged service.
Before storing a newly detected path, resolve symlinks on the target host
(`readlink -f <path>` or `namei -l <path>`) and keep the canonical target path.

`listInstalledDaemons` (`internal/cli/wizard_service.go`) fills each
`DaemonCandidate.Pidfile`/`Exe`/`Cmd`/`User`. Catalog services use those facts to
improve the catalog daemon profile, not the generated `kind: service` entry:
they write `uses:` and inherit PID/process selectors from `catalog/services`.
Uncataloged active units write `service.name` plus a basic `checks.service`, and
their PID question is prefilled from detection and only accepts absolute pidfile
paths.

The service wizard writes new generated `kind: service` files under a
`services/` include directory. Older installs may already load `apps/` as an
include directory for concrete service files; keep that path configured while
those files exist. The wizard preserves any loaded `apps/` include and appends
`services/` instead of moving or deleting legacy files.

## Adding a new wizard

1. Implement `assist.Assistant` (`Name`, `Title`, `Run`) in `internal/assist/`.
2. Detect targets and select with `MultiChoose` (step 2). No name prompts.
3. Gather monitor + interval with `Prompt.AskMonitoring`; inject with
   `Monitoring.apply` (steps 5–6). Batch them with `Prompt.Confirm` when >1.
4. Ask notifiers (if any) through `chooseNotifiers` (step 7) — never duplicate
   its `none`/`default` handling. If the assistant emits watch actions, use
   `Prompt.AskWatchDryRun` instead of hand-rolling `dry_run`.
5. Register it in `registry` (`internal/assist/assist.go`).
6. If it has host targets, extend `detectedTargetKeys` and (for watches)
   `watchFileTargets` so step-9 cleanup works.
7. Add an assistant test plus a case in `contract_test.go`.
