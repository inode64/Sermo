#!/usr/bin/env python3
"""Verify YAML matches the yamlfmt + flow-normalize pipeline."""

from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))

from normalize_yaml_flow import iter_yaml_files, normalize_line  # noqa: E402

YAMLFMT = os.environ.get("YAMLFMT", "yamlfmt")
CONF = ROOT / ".yamlfmt"


def canonicalize(text: str) -> str:
    env = os.environ.copy()
    env["PATH"] = f"{Path.home() / 'go' / 'bin'}:{Path.home() / '.local' / 'bin'}:" + env.get("PATH", "")
    proc = subprocess.run(
        [YAMLFMT, "-conf", str(CONF), "-in"],
        input=text,
        capture_output=True,
        text=True,
        env=env,
        check=True,
    )
    body = proc.stdout
    lines = [normalize_line(line) for line in body.splitlines()]
    out = "\n".join(lines)
    if text.endswith("\n"):
        out += "\n"
    return out


def main() -> int:
    bad: list[Path] = []
    for path in iter_yaml_files():
        original = path.read_text(encoding="utf-8")
        if canonicalize(original) != original:
            bad.append(path)
    if bad:
        for path in bad:
            print(path.relative_to(ROOT))
        print(f"{len(bad)} YAML file(s) need make yaml-fmt", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())