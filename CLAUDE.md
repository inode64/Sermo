# Sermo — project conventions

## Reuse and shared behavior

Before adding a new helper, parser, validator, runner or web/backend adapter,
look for existing code that already solves the same problem and extend it when
that keeps the ownership boundary clear. Do not duplicate validation, parsing,
comparison, notification, monitoring or action-dispatch logic across `sermod`,
`sermoctl`, web, watches and daemons.

When a new check, option, monitor flag, notification behavior or web action is
generally useful to both host `watches:` and service daemons, implement it for
both surfaces in the same change unless there is a documented reason not to. If
the feature intentionally applies only to one surface, document that limitation
where the dispatch/validation decision lives and in the user docs. Keep
`docs/configuration.md`, `docs/rules.md`, daemon docs and `configs/sermo.yml` in
step with YAML behavior.

## Web UI cohesion

The web UI is a single embedded document, `internal/web/index.html` (HTML, CSS
and JS in one file). **Before adding or changing any UI element, find the
existing element that already solves the same problem and copy its structure,
classes and styling exactly** — do not invent a parallel way to do the same
thing. Cohesion across panels is a hard requirement, not a preference.

Concretely, every data panel is a `<details id="{name}-section">` with a
`<summary>`, an optional flex `#{name}-controls` row (search + filters + count)
and a `<table class="{name}-table">` with a sticky header. The one deliberate
distinction:

- **Scrollable panel** — wrap the table in `<div class="table-wrap">`, which
  adds `overflow:auto; max-height:calc(100vh - 13rem)`. Used by Services and
  Host watches (long, unbounded lists).
- **Non-scrollable panel** — place the bare `<table>` directly inside the
  `<details>`, no wrapper. Used by Events, Notifiers and Applications (the page
  scrolls as a whole instead of trapping a panel in its own scrollbar).

Pick the variant that matches the panel's nature and reuse it verbatim; never
hand-roll bespoke `overflow`/`max-height` rules on a single panel. When you
introduce a genuinely new pattern, document it here and in `AGENTS.md` so the
next change can follow it.

## Wizard option selection

The interactive wizard (`sermoctl wizard`, `internal/assist`) drives every
selection through the shared `Prompt` helpers — never hand-roll a bespoke
question. Multi-selects use `Prompt.MultiChoose`, which accepts item numbers,
the keyword `all`, or an option's name. Reuse one consistent **all / none /
default** vocabulary across menus: `all` selects everything; `none` opts out;
`default` inherits the global setting. In the notifier menu the `none` and
`default` entries are **always offered, even when the config defines no
notifiers**, so an expand-only or opt-out watch still has a valid pick — keep
that invariant when adding new assistants or selection steps, and update
`docs/configuration.md` and the wizard spec in the same change.

## Catalog: instanced systemd daemons

When a catalog daemon targets a systemd **instance** unit (`unit@instance`), do
not invent a hand-typed `${id}` variable the operator must remember to set —
derive the instance from code, reusing existing machinery:

- **Single instance keyed by host** (e.g. `ceph-mon@radon`, `ceph-mds@radon`):
  use the built-in `${hostname}` (the short hostname) — `service:
  "ceph-mon@${hostname}"`. It resolves with zero per-service config; an explicit
  `hostname` variable or `SERMO_HOSTNAME` overrides it. `${hostname}` is the short
  form, distinct from `${host}` (the bind-address fallback) — see `docs/daemons.md`.
- **Numeric multi-instance** (e.g. one OSD per device, `ceph-osd@0..N`): make the
  daemon a `%n` version template (`name: ceph-osd%n`) with `versions: { from:
  "/var/lib/ceph/osd/ceph-${n}" }`. `internal/config/versions.go` globs that path
  on the host and materializes one concrete daemon per discovered id, with `${n}`
  baked into `service: "ceph-osd@${n}"`. Honest limitation: this auto-discovers
  daemon *definitions*; the operator still enables one `kind: service` per
  instance (Sermo monitors services, not catalog daemons).

Keep `docs/daemons.md` (built-in variable table) in step when adding a built-in.

## Code formatting (Go)

**Every Go file must be `gofmt`-clean after any modification.** Run `gofmt` on a
file whenever you change it, so the tree always conforms to the standard Go
formatting. This keeps diffs minimal and consistent with the rest of the codebase.

```sh
gofmt -w <file.go>        # format one file
gofmt -l ./internal ./cmd # list any non-conforming files (should be empty)
```

This is enforced automatically in Claude Code: a `PostToolUse` hook in
`.claude/settings.json` runs `gofmt -w` on every `.go` file written or edited, so
formatting never drifts. If you edit Go outside Claude Code (another editor, a
script), run `gofmt -w` yourself before committing — configure your editor to
"format on save" with gofmt to make this automatic.

## Static analysis & linting (Go)

**Every modification must pass the project's static-analysis tools before it is
committed**, to comply with the programming standards. These complement `gofmt`
and catch bugs, vulnerabilities, security issues, and style drift. The binaries
live in `~/go/bin` (add it to `PATH`).

Run all of them against the whole module after any change:

```sh
export PATH="$HOME/go/bin:$PATH"

govulncheck ./...                 # known vulnerabilities (deps + std lib)
staticcheck ./...                 # correctness / bug static analysis
revive -config revive.toml ./...  # style/lint (config tuned for this repo)
golangci-lint run                 # runs gosec (security); uses .golangci.yml
```

All four must report **no findings** before committing. Notes:

- **`revive`** is driven by `revive.toml` at the repo root. It mirrors revive's
  default rule set but disables `unused-parameter`, because many methods
  implement interfaces (e.g. `web.Backend`) whose signature includes a `ctx`
  the implementation legitimately ignores; renaming those to `_` hurts
  readability. Document new exported symbols (the `exported` rule is on).
- **`gosec`** is not installed standalone in `~/go/bin`, so it is run through
  golangci-lint (which bundles it) via `.golangci.yml` at the repo root — that
  config enables only gosec. Accepted exceptions are documented there: `G115`
  (noisy integer-overflow rule on already-bounded values) and `G306` in test
  fixtures. By-design cases — `G204` (executing operator-configured commands)
  and intentional `0644` writes (pidfile, generated YAML) — are suppressed at
  the call site with `//nolint:gosec` plus a justifying comment. Keep that
  pattern: prefer a justified inline `//nolint:gosec` over widening the config.
- Unlike `gofmt` (auto-applied per file by the Claude Code hook), these tools
  analyze the whole module and are too slow to run on every edit — run them once
  before committing, or wire them into CI / a pre-commit hook.

## Before committing — checklist

```sh
export PATH="$HOME/go/bin:$PATH"
gofmt -l ./internal ./cmd         # must print nothing
go build ./... && go test ./...   # must pass
govulncheck ./...                 # no vulnerabilities
staticcheck ./...                 # no findings
revive -config revive.toml ./...  # no findings
golangci-lint run                 # gosec: no findings (.golangci.yml)
```
