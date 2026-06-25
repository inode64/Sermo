#!/bin/bash
# One closed-loop mutation cycle.
# go-mutesting semantics: FAIL = survivor (escaped), PASS = killed.
set -euo pipefail

CYCLE="${1:?cycle number required}"
SRC="${2:?source file required}"
TEST="${3:?test file required}"
SCRATCH="${SCRATCH:-/tmp/grok-goal-f82b50473721/implementer}"
export PATH="${HOME}/go/bin:${HOME}/.local/bin:${PATH}"
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

SRC_BASE="$(basename "$SRC")"
PKG_DIR="$(dirname "$SRC")"
case "$PKG_DIR" in
	internal/cfgval) PKG="./internal/cfgval/" ;;
	internal/locks) PKG="./internal/locks/" ;;
	*) echo "unsupported package dir $PKG_DIR" >&2; exit 1 ;;
esac

mkdir -p "$SCRATCH"
: >>"$SCRATCH/skip-ids.txt"
LOG="$SCRATCH/cycle-${CYCLE}-trace.log"
exec > >(tee -a "$LOG") 2>&1

echo "=== CYCLE $CYCLE $(date -Iseconds) ==="
echo "scope: $SRC -> $TEST"
echo "go-mutesting: FAIL=survivor (escaped), PASS=killed"
git status --short --branch

mutate() {
	go-mutesting --match='^[^_]' "$SRC" 2>&1
	git checkout -- "$SRC" 2>/dev/null || true
	rm -f "${SRC}.tmp"
}

next_survivor() {
	local log="$1"
	python3 - "$log" "$SRC_BASE" "$SCRATCH/skip-ids.txt" <<'PY'
import re, sys
log, base, skip_file = sys.argv[1], sys.argv[2], sys.argv[3]
skip = set()
with open(skip_file) as f:
    skip = {line.strip() for line in f if line.strip()}
pat = re.compile(r'^FAIL .*' + re.escape(base) + r'\.(\d+)"')
for line in open(log):
    m = pat.match(line)
    if not m:
        continue
    key = f"{base}:{m.group(1)}"
    if key in skip:
        continue
    print(m.group(1))
    break
PY
}

mutate | tee "$SCRATCH/mutant-cycle-${CYCLE}-before.log" >/dev/null
BEFORE="$SCRATCH/mutant-cycle-${CYCLE}-before.log"

TARGET=""
for _try in $(seq 1 60); do
	TARGET="$(next_survivor "$BEFORE")"
	[[ -n "$TARGET" ]] || break
	echo "try survivor: ${SRC_BASE}.${TARGET}"
	set +e
	python3 scripts/apply_mutant_fix.py "$SRC" "$TARGET" "$TEST"
	rc=$?
	set -e
	if [[ $rc -eq 3 || $rc -eq 4 ]]; then
		echo "skip ${SRC_BASE}.${TARGET} (rc=$rc)"
		echo "${SRC_BASE}:${TARGET}" >>"$SCRATCH/skip-ids.txt"
		continue
	fi
	if [[ $rc -ne 0 ]]; then
		echo "apply_mutant_fix failed rc=$rc for .${TARGET}" >&2
		exit 1
	fi
	gofmt -w "$TEST"
	go test "$PKG" -count=1
	mutate | tee "$SCRATCH/mutant-cycle-${CYCLE}-after.log" >/dev/null
	if grep -q "^PASS .*${SRC_BASE}\\.${TARGET}\"" "$SCRATCH/mutant-cycle-${CYCLE}-after.log"; then
		echo "killed: ${SRC_BASE}.${TARGET} (PASS in after log)"
		break
	fi
	echo "fix did not kill ${SRC_BASE}.${TARGET}; revert and skip"
	git checkout -- "$TEST"
	echo "${SRC_BASE}:${TARGET}" >>"$SCRATCH/skip-ids.txt"
	TARGET=""
done

if [[ -z "$TARGET" ]] || ! grep -q "^PASS .*${SRC_BASE}\\.${TARGET}\"" "$SCRATCH/mutant-cycle-${CYCLE}-after.log"; then
	echo "no killable survivor found in cycle $CYCLE for $SRC" >&2
	exit 6
fi

echo "${SRC_BASE}.${TARGET}" >"$SCRATCH/cycle-${CYCLE}-target.txt"
grep "^FAIL .*${SRC_BASE}\\.${TARGET}\"" "$BEFORE" || true
grep "^PASS .*${SRC_BASE}\\.${TARGET}\"" "$SCRATCH/mutant-cycle-${CYCLE}-after.log" || true

bash scripts/run-make-check-audited.sh | tee "$SCRATCH/make-check-cycle-${CYCLE}.log"
git status --short --branch | tee "$SCRATCH/git-status-cycle-${CYCLE}.log"
git add "$TEST"
MSG="agent: test($(basename "$PKG_DIR")): kill mutant .${TARGET} ${SRC_BASE}"
if [[ "$CYCLE" == "3" && "$SRC_BASE" == "cfgval.go" && "$TARGET" == "19" ]]; then
	MSG="agent: test(cfgval): pin mutant .14 FormatFloat + kill .19 StringArray nil"
fi
git commit -m "$MSG"
echo "committed cycle $CYCLE target .${TARGET}"