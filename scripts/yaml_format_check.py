#!/usr/bin/env python3
"""Format, check or lint the same YAML inventory."""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
from pathlib import Path

from normalize_yaml_flow import ROOT, iter_yaml_files, normalize_flow

YAMLFMT = os.environ.get("YAMLFMT", "yamlfmt")
YAMLLINT = os.environ.get("YAMLLINT", "yamllint")
CONF = ROOT / ".yamlfmt"
TOOL_TIMEOUT_SECONDS = 60


def tool_env() -> dict[str, str]:
    env = os.environ.copy()
    env["PATH"] = f"{Path.home() / 'go' / 'bin'}:{Path.home() / '.local' / 'bin'}:" + env.get("PATH", "")
    return env


def canonicalize(text: str) -> str:
    proc = subprocess.run(
        [YAMLFMT, "-conf", str(CONF), "-in"],
        input=text, capture_output=True, text=True, env=tool_env(),
        check=True, timeout=TOOL_TIMEOUT_SECONDS,
    )
    return normalize_flow(proc.stdout)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--write", action="store_true", help="apply the checked formatting")
    mode.add_argument("--lint", action="store_true", help="run yamllint on the shared inventory")
    args = parser.parse_args()
    paths = iter_yaml_files()
    if args.lint:
        return subprocess.run(
            [YAMLLINT, "--strict", "-c", str(ROOT / ".yamllint.yml"), *map(str, paths)],
            env=tool_env(), check=False, timeout=TOOL_TIMEOUT_SECONDS,
        ).returncode
    bad: list[Path] = []
    for path in paths:
        original = path.read_text(encoding="utf-8")
        updated = canonicalize(original)
        if updated != original:
            if args.write:
                path.write_text(updated, encoding="utf-8")
            bad.append(path)
    for path in bad:
        print(path.relative_to(ROOT))
    if bad and not args.write:
        print(f"{len(bad)} YAML file(s) need make yaml-fmt", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
