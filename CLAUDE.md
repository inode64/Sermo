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

Before committing Go changes, verify the tree is clean:

```sh
gofmt -l ./internal ./cmd   # must print nothing
go build ./... && go test ./...
```
