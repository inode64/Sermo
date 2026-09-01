#!/usr/bin/env python3
"""Validate immutable, synchronized static-analyzer pins."""

from __future__ import annotations

import re
import shlex
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW_PATH = ROOT / ".github/workflows/ci.yml"
CUSTOM_GCL_PATH = ROOT / ".custom-gcl.yml"

GOLANGCI_MODULE = "github.com/golangci/golangci-lint/v2/cmd/golangci-lint"
GOIMPORTS_MODULE = "golang.org/x/tools/cmd/goimports"
DEADCODE_MODULE = "golang.org/x/tools/cmd/deadcode"
MUTABLE_GO_PINS = {"latest", "main", "master", "head"}
EXACT_PYTHON_REQUIREMENT = re.compile(
    r"^[A-Za-z0-9_.-]+(?:\[[A-Za-z0-9_,.-]+\])?==[^=<>!~\s]+$"
)
TOP_LEVEL_VERSION = re.compile(r"^version:\s*([^\s#]+)", re.MULTILINE)
NILAWAY_PLUGIN = re.compile(
    r"^\s*-\s+module:\s*go\.uber\.org/nilaway\s*$"
    r"(?:(?!^\s*-\s+module:).)*?"
    r"^\s+version:\s*([^\s#]+)",
    re.MULTILINE | re.DOTALL,
)


def go_installs(workflow: str) -> tuple[dict[str, str], list[str]]:
    """Return Go install module pins and structural problems."""
    installs: dict[str, str] = {}
    problems: list[str] = []
    for lineno, line in enumerate(workflow.splitlines(), 1):
        stripped = line.strip()
        if not stripped.startswith("go install "):
            continue
        try:
            words = shlex.split(stripped)
        except ValueError as error:
            problems.append(f"CI line {lineno} has invalid go install syntax: {error}")
            continue
        if len(words) != 3 or "@" not in words[2]:
            problems.append(f"CI line {lineno} must install one Go analyzer at an exact pin")
            continue
        module, version = words[2].rsplit("@", 1)
        if not module or not version:
            problems.append(f"CI line {lineno} has an incomplete Go analyzer pin")
            continue
        if module in installs:
            problems.append(f"Go analyzer {module} is installed more than once")
            continue
        installs[module] = version
        if version.casefold() in MUTABLE_GO_PINS or not version.startswith("v"):
            problems.append(f"Go analyzer {module} uses mutable pin {version}")
    return installs, problems


def python_pin_problems(workflow: str) -> list[str]:
    """Reject mutable Python requirements in analyzer installation commands."""
    problems: list[str] = []
    for lineno, line in enumerate(workflow.splitlines(), 1):
        stripped = line.strip()
        if not stripped.startswith("pip install "):
            continue
        try:
            words = shlex.split(stripped)
        except ValueError as error:
            problems.append(f"CI line {lineno} has invalid pip install syntax: {error}")
            continue
        for requirement in words[2:]:
            if requirement.startswith("-"):
                continue
            if not EXACT_PYTHON_REQUIREMENT.fullmatch(requirement):
                problems.append(
                    f"Python analyzer {requirement} is not exactly pinned"
                )
    return problems


def custom_gcl_versions(custom_gcl: str) -> tuple[str, str]:
    """Return custom golangci-lint and NilAway pins, if present."""
    golangci = TOP_LEVEL_VERSION.search(custom_gcl)
    nilaway = NILAWAY_PLUGIN.search(custom_gcl)
    return (
        golangci.group(1) if golangci else "",
        nilaway.group(1) if nilaway else "",
    )


def validate_pins(workflow: str, custom_gcl: str) -> list[str]:
    """Return reproducibility and shared-owner errors for analyzer pins."""
    installs, problems = go_installs(workflow)
    problems.extend(python_pin_problems(workflow))

    ci_golangci = installs.get(GOLANGCI_MODULE, "")
    custom_golangci, nilaway = custom_gcl_versions(custom_gcl)
    if not ci_golangci:
        problems.append("CI does not pin golangci-lint")
    if not custom_golangci:
        problems.append(".custom-gcl.yml does not pin golangci-lint")
    if ci_golangci and custom_golangci and ci_golangci != custom_golangci:
        problems.append(
            f"golangci-lint is {ci_golangci} in CI but {custom_golangci} "
            "in .custom-gcl.yml"
        )

    if not nilaway:
        problems.append(".custom-gcl.yml does not pin NilAway")
    elif nilaway.casefold() in MUTABLE_GO_PINS or not nilaway.startswith("v"):
        problems.append(f"NilAway uses mutable pin {nilaway}")

    goimports = installs.get(GOIMPORTS_MODULE, "")
    deadcode = installs.get(DEADCODE_MODULE, "")
    if not goimports or not deadcode:
        problems.append("CI must pin both goimports and deadcode")
    elif goimports != deadcode:
        problems.append(
            "goimports and deadcode must use the same golang.org/x/tools version"
        )
    return problems


def main() -> int:
    """Check the repository's analyzer installation sources."""
    problems = validate_pins(
        WORKFLOW_PATH.read_text(encoding="utf-8"),
        CUSTOM_GCL_PATH.read_text(encoding="utf-8"),
    )
    if not problems:
        print("analyzer pins: ok")
        return 0
    print(f"analyzer pins: {len(problems)} problem(s)", file=sys.stderr)
    for problem in problems:
        print(f"- {problem}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
