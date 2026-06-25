#!/bin/bash
# Replays the same gates as `make check` with explicit phase echoes for audit transcripts.
set -euo pipefail
export PATH="${HOME}/go/bin:${HOME}/.local/bin:${PATH}"
cd "$(dirname "$0")/.."

run() {
	echo "==> $*"
	"$@"
	echo "<== exit 0"
}

run go vet ./...
run make lint
run make yaml-fmt-check
run make yaml-lint
run make web-check
run go test ./...
echo "==> audited make check equivalent: ALL PASSED"