# Sermo — project conventions

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
