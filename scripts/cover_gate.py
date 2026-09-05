#!/usr/bin/env python3
"""Fail when safety-package statement coverage drops below the no-regression floor.

Usage:
  go test -coverprofile=coverage-safety.out ./internal/operation ./internal/process ...
  python3 scripts/cover_gate.py coverage-safety.out

Thresholds sit a few points under the measured baseline so a real regression
fails the gate without churning on every tiny swing in a large package.
"""

from __future__ import annotations

import sys
from pathlib import Path

# Package path suffix (as printed by `go tool cover -func`) → minimum %.
# Keep AGENTS.md in step when these floors change.
THRESHOLDS: dict[str, float] = {
    "sermo/internal/operation": 88.0,
    "sermo/internal/process": 75.0,
    "sermo/internal/locks": 75.0,
    "sermo/internal/rules": 80.0,
    "sermo/internal/config": 85.0,
}

def package_totals(profile: Path) -> dict[str, float]:
    """Return per-package statement coverage % from a cover profile."""
    # Package totals are not printed per package by `go tool cover -func`, only
    # a global total. Recompute from statement blocks in the profile itself.
    covered: dict[str, int] = {}
    total: dict[str, int] = {}
    for line in profile.read_text(encoding="utf-8").splitlines():
        if line.startswith("mode:"):
            continue
        # file:start.line,start.col,end.line,end.col numstmt count
        # e.g. sermo/internal/operation/engine.go:12.2,14.3 3 1
        try:
            meta, num_stmt_s, count_s = line.rsplit(" ", 2)
            num_stmt = int(num_stmt_s)
            count = int(count_s)
        except ValueError:
            continue
        file_path = meta.split(":")[0]
        if not file_path.startswith("sermo/internal/"):
            continue
        parts = file_path.split("/")
        if len(parts) < 3:
            continue
        pkg = "/".join(parts[:3])  # sermo/internal/<pkg>
        total[pkg] = total.get(pkg, 0) + num_stmt
        if count > 0:
            covered[pkg] = covered.get(pkg, 0) + num_stmt

    pcts: dict[str, float] = {}
    for pkg, n in total.items():
        if n == 0:
            continue
        pcts[pkg] = 100.0 * covered.get(pkg, 0) / n
    return pcts


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print(f"usage: {argv[0]} <coverage.out>", file=sys.stderr)
        return 2
    profile = Path(argv[1])
    if not profile.is_file():
        print(f"cover profile not found: {profile}", file=sys.stderr)
        return 2

    pcts = package_totals(profile)
    failed = False
    for pkg, floor in sorted(THRESHOLDS.items()):
        got = pcts.get(pkg)
        if got is None:
            print(f"FAIL  {pkg}: no coverage data (package not in profile)")
            failed = True
            continue
        status = "ok" if got + 1e-9 >= floor else "FAIL"
        if status == "FAIL":
            failed = True
        print(f"{status:4}  {pkg}: {got:.1f}% (floor {floor:.1f}%)")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
