#!/usr/bin/env python3
"""Prove the configured import boundaries and their exceptions with real lint runs."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import tempfile
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
LINT_TIMEOUT_SECONDS = 120

# One diagnostic per forbidden import. Compare filenames AND rule names so a
# different rule cannot accidentally make a broken boundary look effective.
FORBIDDEN = {
    "internal/checks/blocked.go": ("sermo/internal/operation", "checks-no-operation"),
    "internal/conn/blocked.go": ("sermo/internal/operation", "conn-no-operation"),
    "internal/config/blocked.go": ("sermo/internal/operation", "config-no-operation"),
    "internal/rules/operation.go": ("sermo/internal/operation", "rules-no-operation"),
    "internal/rules/exec.go": ("sermo/internal/execx", "rules-no-execx"),
    "internal/app/blocked.go": ("os/exec", "no-direct-os-exec"),
}
PERMITTED = {
    "internal/app/allowed.go": "sermo/internal/operation",
    "internal/checks/allowed.go": "sermo/internal/execx",
    "internal/conn/allowed.go": "sermo/internal/execx",
    "internal/config/allowed.go": "sermo/internal/execx",
    "internal/rules_extra/allowed.go": "sermo/internal/execx",
    "internal/execx/allowed.go": "os/exec",
    "internal/cli/lock.go": "os/exec",
    "internal/app/allowed_test.go": "os/exec",
    "internal/rules/allowed_test.go": "sermo/internal/execx",
    **{f"internal/{name}/operation_test.go": "sermo/internal/operation"
       for name in ("checks", "conn", "rules", "config")},
}


def write_import(root: Path, filename: str, dependency: str) -> None:
    path = root / filename
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(f'package {path.parent.name}\n\nimport _ "{dependency}"\n', encoding="utf-8")


def run_fixture(binary: Path, root: Path, expected: set[tuple[str, str]]) -> None:
    report = root / "issues.json"
    result = subprocess.run(
        [str(binary), "run", "--config", str(root / ".golangci.yml"),
         "--enable-only=depguard", "--path-mode=abs", "--output.json.path", str(report),
         "--output.text.path", "/dev/null", "--timeout=90s", "./..."],
        cwd=root, capture_output=True, text=True, check=False, timeout=LINT_TIMEOUT_SECONDS,
    )
    wanted_status = 1 if expected else 0
    if result.returncode != wanted_status:
        raise RuntimeError(
            f"depguard fixture exited {result.returncode}, expected {wanted_status}:\n"
            + result.stdout + result.stderr
        )
    issues = json.loads(report.read_text(encoding="utf-8")).get("Issues") or []
    actual = set()
    for issue in issues:
        rule = re.search(r"from list '([^']+)'", issue["Text"])
        if issue["FromLinter"] != "depguard" or not rule:
            raise RuntimeError(f"unexpected diagnostic: {issue}")
        filename = str(Path(issue["Pos"]["Filename"]).relative_to(root))
        actual.add((filename, rule.group(1)))
    if actual != expected or len(issues) != len(expected):
        raise RuntimeError(f"depguard fixture mismatch: missing={expected - actual}, extra={actual - expected}")


def check(binary: Path, config: Path) -> None:
    source = config.read_text(encoding="utf-8")
    # --enable-only isolates these fixtures, but must not mask a disabled gate.
    if "depguard" not in yaml.safe_load(source)["linters"]["enable"]:
        raise RuntimeError("depguard must be enabled in .golangci.yml")
    with tempfile.TemporaryDirectory(prefix="sermo-depguard-") as directory:
        root = Path(directory).resolve()
        (root / ".golangci.yml").write_text(source, encoding="utf-8")
        go_version = re.search(r"^go (\S+)$", (ROOT / "go.mod").read_text(), re.MULTILINE)
        if go_version is None:
            raise RuntimeError("go.mod has no Go version")
        (root / "go.mod").write_text(f"module sermo\n\ngo {go_version[1]}\n", encoding="utf-8")
        operation = root / "internal/operation/stub.go"
        operation.parent.mkdir(parents=True)
        operation.write_text("package operation\n", encoding="utf-8")
        for filename, dependency in PERMITTED.items():
            write_import(root, filename, dependency)
        run_fixture(binary, root, set())
        for filename, (dependency, _) in FORBIDDEN.items():
            write_import(root, filename, dependency)
        run_fixture(binary, root, {(name, rule) for name, (_, rule) in FORBIDDEN.items()})


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=ROOT / "bin/custom-gcl")
    parser.add_argument("--config", type=Path, default=ROOT / ".golangci.yml")
    args = parser.parse_args()
    check(args.binary.resolve(), args.config.resolve())
    print(f"depguard contracts: {len(FORBIDDEN)} forbidden imports, {len(PERMITTED)} permitted imports: ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
