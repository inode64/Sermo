#!/usr/bin/env python3
"""Fail when a tracked file names the operator's private fleet.

This repository is public, so the names to reject cannot be listed in it: they
come from the operator's inventory outside the checkout, when one exists. CI has
none and skips the scan. Only the inventory's first column is read, and a
finding prints the matched host label or address prefix, nothing else.
"""

from __future__ import annotations

import csv
import os
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_INVENTORY = ROOT.parent / "conf" / "hosts"
ADDRESS_INVENTORY_GLOB = "inventario-red-*.csv"
ADDRESS_COLUMN = "ip_vpn"

# Host labels that are ordinary words in this project's prose and fixtures.
GENERIC_LABELS = frozenset({"sandbox"})
# Generated or third-party content that the checked sources already cover.
SKIPPED_FILES = frozenset({"go.sum", "package-lock.json"})
SKIPPED_PREFIXES = ("internal/web/src/vendor/",)
SKIPPED_SUFFIXES = (".png", ".ico", ".woff", ".woff2")


def inventory_terms(inventory: str) -> set[str]:
    """Return host labels and multi-label domains from the first column."""
    terms: set[str] = set()
    for raw in inventory.splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        labels = line.split()[0].rsplit("@", 1)[-1].lower().split(".")
        terms.add(labels[0])
        if len(labels) >= 3:
            terms.add(".".join(labels[-2:]))
    return terms - GENERIC_LABELS


def address_prefixes(rows: list[dict[str, str]]) -> set[str]:
    """Return the /16 prefix of every private address in an address inventory."""
    prefixes: set[str] = set()
    for row in rows:
        # Spreadsheet exports prefix the address with an apostrophe to keep it text.
        octets = (row.get(ADDRESS_COLUMN) or "").strip().strip("'\"").split(".")
        if len(octets) == 4 and all(octet.isdigit() for octet in octets):
            prefixes.add(f"{octets[0]}.{octets[1]}.")
    return prefixes


def term_pattern(terms: set[str], prefixes: set[str]) -> re.Pattern[str] | None:
    """Compile the terms, longest first so a domain wins over its own label."""
    if not terms and not prefixes:
        return None
    alternatives = [rf"\b{re.escape(term)}\b" for term in sorted(terms, key=len, reverse=True)]
    alternatives += [rf"(?<![\d.]){re.escape(prefix)}\d" for prefix in sorted(prefixes)]
    return re.compile("|".join(alternatives), re.IGNORECASE)


def scanned(path: str) -> bool:
    """Report whether a tracked path is first-party text worth reading."""
    return (
        Path(path).name not in SKIPPED_FILES
        and not path.startswith(SKIPPED_PREFIXES)
        and not path.endswith(SKIPPED_SUFFIXES)
    )


def findings(paths: list[str], root: Path, pattern: re.Pattern[str]) -> list[str]:
    """Return one `path:line: term` entry per match."""
    found: list[str] = []
    for path in filter(scanned, paths):
        try:
            text = (root / path).read_text(encoding="utf-8")
        except (OSError, UnicodeDecodeError):
            continue
        for number, line in enumerate(text.splitlines(), 1):
            found.extend(f"{path}:{number}: {match.group(0)}" for match in pattern.finditer(line))
    return found


def tracked_files() -> list[str]:
    """List the files this repository publishes."""
    listing = subprocess.run(["git", "ls-files", "-z"], cwd=ROOT, capture_output=True, check=True, timeout=30)
    return [path for path in listing.stdout.decode("utf-8").split("\0") if path]


def main() -> int:
    """Scan tracked files against the operator's inventory."""
    inventory = Path(os.environ.get("SERMO_SSH_ENV") or DEFAULT_INVENTORY)
    if not inventory.is_file():
        print("privacy-check: no operator inventory here; fleet-name scan skipped")
        return 0
    rows: list[dict[str, str]] = []
    for path in inventory.parent.glob(ADDRESS_INVENTORY_GLOB):
        # Only an ASCII column is read, whatever encoding the other columns use.
        with path.open(encoding="utf-8", errors="replace", newline="") as handle:
            rows.extend(csv.DictReader(handle))
    pattern = term_pattern(inventory_terms(inventory.read_text(encoding="utf-8")), address_prefixes(rows))
    if pattern is None:
        return 0
    problems = findings(tracked_files(), ROOT, pattern)
    if problems:
        print("private fleet data in tracked files:", file=sys.stderr)
        for problem in problems:
            print(f"  {problem}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
