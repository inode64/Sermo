# Semgrep call-boundary rules

Sermo uses Semgrep for invariants that golangci-lint cannot express: a
package is allowed to import another, but a specific call is still forbidden.
`.golangci.yml` owns import and analyzer policy. This directory owns
call-level contracts that were violated at least once.

## Files

- [rules/sermo.yml](rules/sermo.yml) — every rule. The header of that file is
  the inventory; do not copy rule ids into other docs.
- [tests/sermo.go](tests/sermo.go) — fixtures. The `semgreptest` build tag and
  the `.semgrep` directory keep Go tooling from compiling them.

`make semgrep` (via `make check`) runs `semgrep --test` against the fixtures,
then scans `cmd`, `internal` and `tools`. A rule that matches nothing, or a
`ruleid:` that no longer fires, fails the gate.

Import boundaries have the same executable contract: `make analyzer-config-check`
(via `make lint` and `make check`) verifies the golangci-lint schema and runs
`scripts/check_depguard.py` against the real configuration in a temporary Go
module. Forbidden imports must produce the expected rule diagnostics; permitted
imports and test/primitive exceptions must remain clean. Depguard matches
absolute filenames, so scoped file globs need the `**/` prefix.

`make yaml-fmt`, `make yaml-fmt-check` and `make yaml-lint` share the inventory
in `scripts/normalize_yaml_flow.py`: root YAML configs, `.yamlfmt`, and both
`.yml` and `.yaml` under the configured project directories, including this
directory. Flow spacing uses YAML tokens to preserve code patterns and regex
scalars. The Python tools use PyYAML, also required by yamllint.

## When to add a rule

Add a rule only when all of these hold:

1. The contract is a **call**, not an import (depguard already owns imports).
2. A generic Go linter cannot see it.
3. The defect reached the repository at least once, or a cleanup just removed
   it and the same shape would regress easily.
4. You can write one **positive** fixture (`ruleid: <id>`) and one **negative**
   fixture (`ok: <id>`).

Do not add a rule for a one-line alias, a domain projection that only looks
like Unique, or a wrapcheck-required identity wrap without a documented
exception.

## How to add a rule

1. Pick a kebab-case `id` that names the contract (`enabled-must-use-cfgval-disabled`),
   not the syntax.
2. Append the rule to `rules/sermo.yml`. `languages: [go]`, `severity: ERROR`.
3. Exclude the owning package and `**/*_test.go` unless the contract applies
   there. Named exceptions (certificate inspection, wrapcheck identity wraps)
   belong in `paths.exclude` or a `// nosemgrep: <id>` comment that states the
   design reason.
4. Add both fixtures to `tests/sermo.go` on the matching line. The `--test`
   pass is what keeps a rule from silently going dormant.
5. Run `make semgrep`. If production still has the old shape, fix the code
   rather than broadening an exclusion.
6. Finish with `make check`.

## Exceptions

`// nosemgrep: <id>` is the Semgrep counterpart of `//nolint`. Name the exact
rule and explain why that site is allowed. Do not add a path exclusion to land
a one-off.
