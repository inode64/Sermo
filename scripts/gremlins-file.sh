#!/bin/bash
# Run gremlins mutation testing scoped to a single source file within its package,
# printing only LIVED (surviving) mutants. Usage: gremlins-file.sh <path/to/file.go>
set -euo pipefail
export PATH="${HOME}/go/bin:${HOME}/.local/bin:${PATH}"
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

SRC="${1:?source file required (e.g. internal/checks/zombies.go)}"
PKG_DIR="$(dirname "$SRC")"
BASE="$(basename "$SRC")"

# Build an exclude regexp matching every *other* .go file in the package directory,
# so gremlins only mutates the target file.
others="$(ls "$PKG_DIR"/*.go | sed 's#.*/##' | grep -vx "$BASE" | sed 's/\./\\./g' | paste -sd'|')"

# workers=1 avoids per-mutant compile contention; high coefficient prevents the
# package recompile (seconds) from being clipped by a sub-second test baseline.
gremlins unleash "./$PKG_DIR" \
	-S l \
	--workers 1 \
	--timeout-coefficient 100 \
	--exclude-files "(${others})" \
	"${@:2}"
