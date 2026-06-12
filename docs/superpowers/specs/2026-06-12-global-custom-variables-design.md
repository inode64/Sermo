# Global custom variables (`defaults.variables`) — design

## Goal

Let an operator declare custom variables once under `defaults.variables` and use
them as `${name}` anywhere a value is variable-expanded — every service, daemon,
and (per the chosen scope) host watch.

```yaml
defaults:
  policy: { cooldown: 5m }
  variables:
    custom_var1: /opt/myapp
    custom_var2: 8443
    secret: ${env:MY_SECRET}        # same env support as per-service variables
    libdir: [/usr/lib64, /usr/lib]  # list = first existing path
```

Confirmed decisions: section name `defaults.variables`; precedence **service
variables > defaults.variables > builtins** (a custom var may override a builtin
like `host`/`port`); scope includes **host watches** as well as services/daemons.

## How variables resolve today

`collectVariables(tree)` reads a service's merged `variables:` into a flat string
map (stringifying scalars, resolving `${env:...}`, and a list → first existing
path). `Resolve` (services, resolve.go:29) and `resolveDoc` (daemons/apps/libs,
resolve.go:425) build `vars` from `collectVariables(merged)`, then
`injectBuiltinVariables` fills gaps (`name`, `host`, `hostname`, `port`,
`pidfile`, `user`, `init`, `service`, `display_name`), then `expandTree`
substitutes `${var}` across the tree. Host **watches** (`Global.Raw["watches"]`)
are built by `internal/app` `BuildWatches` and are **not** variable-expanded
today.

## Design

### Global var layer

A new `(*Config).globalVars()` returns the custom variables, reusing
`collectVariables` over a synthetic tree so they get the same env/list handling:

```go
func (c *Config) globalVars() map[string]string {
	return collectVariables(map[string]any{"variables": c.Global.Defaults["variables"]})
}
```

### Services & daemons (precedence service > custom > builtins)

In both `Resolve` and `resolveDoc`, seed `vars` with the global layer, then
overlay the service/daemon's own variables, then inject builtins:

```go
vars := c.globalVars()
maps.Copy(vars, collectVariables(merged)) // service/daemon variables win
errs := validateVariableValues(vars)
injectBuiltinVariables(vars, name, merged) // builtins only fill gaps
```

(`"maps"` is **not** imported in `resolve.go` today — add it; it is currently
only imported by `variables.go`.) This applies to services, daemons, apps and
libs alike (all route through `Resolve`/`resolveDoc`), which matches the intended
scope.

Because `injectBuiltinVariables` only sets a builtin when the key is absent, a
custom `host`/`port` already present in `vars` suppresses the builtin — giving the
chosen `service > custom > builtins` order.

### Host watches (new expansion)

Watches gain `${var}` expansion against `globalVars()` + host-level builtins
(`host`, `hostname`, `init`, `user` — no service-specific `name`/`port`/`pidfile`).
A new exported `(*Config).ResolveWatches() (map[string]any, []string)` expands the
raw watches map once and returns it directly (`expandTree` already returns the
expanded `map[string]any`):

```go
func (c *Config) ResolveWatches() (map[string]any, []string) {
	raw, _ := c.Global.Raw["watches"].(map[string]any)
	if raw == nil {
		return nil, nil
	}
	vars := c.globalVars()
	injectHostBuiltins(vars) // host, hostname, init, user (the service-independent builtins)
	return expandTree(raw, vars)
}
```

**Every consumer of the raw watches map must switch to `ResolveWatches()`**, or
the web UI / `diagnose` would show or check literal `${var}`. The reviewer
enumerated three (the rest correctly stay on raw):

| Consumer | Switch? |
|---|---|
| `internal/app/watch_build.go:34` `BuildWatches` | **yes** (fires the watch) |
| `internal/app/webbackend.go:216` (web watch listing) | **yes** (UI) |
| `internal/diag/checks.go:48` `diagWatches` | **yes** (diagnose) |
| `serviceMonitorWatches` (watch_build.go:514) | no — already expanded via `Resolve` |
| `HasConfiguredTargets` (daemon.go:670), `validateWatches`, `loader.go` assembly, `wizard.go` | no — presence/structure/raw-file |

`BuildWatches` is invoked on initial boot and `Monitor.Reload` (monitor.go:118),
so taking `cfg` covers reload; the web listing is rebuilt on reload too
(monitor.go:140) and must use the expanded watches.

## Validation

`defaults.variables`, when present, must be a mapping; each value must be a scalar
or a list of scalars and must not itself contain `${...}` (reuse
`validateVariableValues` on `globalVars()`), surfaced as a `defaults.variables`
issue, via a `validateDefaultsVariables` hooked into `validateGlobal` (next to the
existing `defaultsCooldown` check). There is no closed-set check on `defaults.*`
keys, so `defaults.variables` is not rejected as unknown.

**Undefined `${custom_x}` in a watch — surface at validate time.** Services get
undefined-variable errors at `config validate` (because `validateServices` runs
`Resolve`, which expands). Watches are validated on the *raw* map today, so a
watch var typo would only error at daemon start. To keep parity, `config
validate` calls `ResolveWatches()` and reports its errors (scoped to
`watches`).

## Touch points

- `internal/config/resolve.go`: `globalVars()`; seed in `Resolve` and
  `resolveDoc`; `ResolveWatches()`; `injectHostBuiltins` factored from
  `injectBuiltinVariables`.
- `internal/config/validate_global.go` + `validate.go`: `validateDefaultsVariables`
  in `validateGlobal`; `validateWatches` (or `validateGlobal`) calls
  `ResolveWatches()` and reports its errors so watch var typos fail validation.
- `internal/app/watch_build.go:34` `BuildWatches`,
  `internal/app/webbackend.go:216` (web watch listing), and
  `internal/diag/checks.go:48` `diagWatches` all read `cfg.ResolveWatches()`
  instead of `cfg.Global.Raw["watches"]`. (`serviceMonitorWatches` already gets
  expanded watches via `Resolve`; presence/structure/raw-file readers stay raw.)
- `docs/configuration.md`: document `defaults.variables`, precedence, and the
  watch scope.

## Edge cases

- A custom var named like a builtin (`host`) overrides it for every service that
  does not set its own — intended (decision 2). Document this clearly.
- `defaults.variables` referencing `${env:VAR}` resolves at load like per-service
  variables; a missing env var yields an empty string (existing behavior).
- A list-valued custom var resolves to the first existing path (existing
  `firstExistingPath`), so a custom `libdir` works like a daemon's.
- Watches still have no per-watch `name`/`port` builtins; only host-level builtins
  + custom vars apply there.

## Testing

- Service/daemon: a `defaults.variables.custom1` used in a check expands; a
  service-level `variables.custom1` overrides it; a custom `host` overrides the
  builtin host but a service's own host still wins.
- Watch: a watch using `${custom1}` expands via `ResolveWatches`.
- Validation: non-map `defaults.variables`, nested `${}` value → issue.

## Out of scope (v1)

- Per-watch builtins (`name` etc.) in watch expansion.
- Computed/derived variables or variable references inside variables (still no
  nesting, per section 10).
