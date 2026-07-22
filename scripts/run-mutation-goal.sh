#!/bin/bash
# Run 10 forward mutation cycles with closed-loop verification.
set -euo pipefail
SCRATCH=/tmp/grok-goal-f82b50473721/implementer
export SCRATCH
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"
export PATH="${HOME}/go/bin:${HOME}/.local/bin:${PATH}"

chmod +x scripts/run-make-check-audited.sh scripts/mutate-one-cycle.sh

echo "=== mutation goal start $(date -Iseconds) ===" | tee "$SCRATCH/cycle-run.log"
git status --short --branch | tee "$SCRATCH/git-status-start.log"

# Cycles 1-8: cfgval
for n in $(seq 1 8); do
	echo "--- cycle $n: cfgval ---" | tee -a "$SCRATCH/cycle-run.log"
	scripts/mutate-one-cycle.sh "$n" internal/cfgval/cfgval.go internal/cfgval/cfgval_test.go
done

# Cycle 9: locks validateIdentifier
echo "--- cycle 9: locks lock.go ---" | tee -a "$SCRATCH/cycle-run.log"
scripts/mutate-one-cycle.sh 9 internal/locks/lock.go internal/locks/helpers_test.go

make check 2>&1 | tee "$SCRATCH/make-check-final.log"
go test ./... -count=1 2>&1 | tee "$SCRATCH/go-test-final.log"
git status --short --branch | tee "$SCRATCH/git-status-final.log"
git log --oneline -12 -- . | tee "$SCRATCH/git-log.log"

mkdir -p "$SCRATCH/commits"
i=0
git log --reverse --format=%H affb6b8..HEAD | while read -r sha; do
	i=$((i+1))
	git show --stat --oneline "$sha" >"$SCRATCH/commits/cycle-${i}-${sha}.txt"
done

echo "=== mutation goal complete ===" | tee -a "$SCRATCH/cycle-run.log"