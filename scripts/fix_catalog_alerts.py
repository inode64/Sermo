#!/usr/bin/env python3
"""Fix alert rules, fds thresholds and ceph socket paths."""

from __future__ import annotations

import copy
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
SERVICES = ROOT / "catalog" / "services"

CEPH_STATUS_CMD = {
    "ceph-mds.yml": ["${ceph_binary}", "--admin-daemon", "/run/ceph/ceph-mds.${hostname}.asok", "status"],
    "ceph-mgr.yml": ["${ceph_binary}", "--admin-daemon", "/run/ceph/ceph-mgr.${hostname}.asok", "status"],
    "ceph-osd.yml": None,  # uses ${n}
    "ceph-mon.yml": ["${ceph_binary}", "--admin-daemon", "/run/ceph/ceph-mon.${hostname}.asok", "mon_status"],
}


def strip_requires(obj):
    if isinstance(obj, dict):
        obj.pop("requires", None)
        for v in obj.values():
            strip_requires(v)
    elif isinstance(obj, list):
        for item in obj:
            strip_requires(item)


def fix_rules(rules: dict) -> None:
    for name, rule in list(rules.items()):
        if not isinstance(rule, dict):
            continue
        if name.startswith("alert-if-") or rule.get("then", {}).get("action") == "alert":
            rule["type"] = "alert"
        metric = rule.get("if", {}).get("metric", {})
        if metric.get("name") == "fds":
            metric["value"] = 50000
            rule.setdefault("within", {"cycles": 10, "min_matches": 3})


def fix_checks(checks: dict) -> None:
    fds = checks.get("fds")
    if isinstance(fds, dict) and fds.get("name") == "fds":
        fds["value"] = 50000


def fix_doc(path: Path, doc: dict) -> None:
    if checks := doc.get("checks"):
        fix_checks(checks)
    if rules := doc.get("rules"):
        fix_rules(rules)

    name = path.name
    if name in CEPH_STATUS_CMD and CEPH_STATUS_CMD[name]:
        cmd = CEPH_STATUS_CMD[name]
        for section in ("checks",):
            sec = doc.get(section, {})
            st = sec.get("status")
            if isinstance(st, dict) and st.get("type") == "command":
                st["command"] = list(cmd)

    if name == "ceph-osd.yml":
        for section in ("checks",):
            sec = doc.get(section, {})
            st = sec.get("status")
            if isinstance(st, dict) and st.get("type") == "command":
                st["command"] = [
                    "${ceph_binary}",
                    "--admin-daemon",
                    "/run/ceph/ceph-osd.${n}.asok",
                    "status",
                ]

    if name == "memcached.yml" and (rules := doc.get("rules")):
        r = rules.get("restart-if-memcached-failed")
        if isinstance(r, dict):
            r.setdefault("if", {}).setdefault("failed", {})["check"] = "stats"
        r = rules.get("restart-if-tcp-failed")
        if isinstance(r, dict):
            r.setdefault("if", {}).setdefault("failed", {})["check"] = "port"


def main() -> None:
    for path in sorted(SERVICES.glob("*.yml")):
        with path.open() as f:
            doc = yaml.safe_load(f)
        if not doc:
            continue
        fix_doc(path, doc)
        with path.open("w") as f:
            yaml.dump(doc, f, default_flow_style=False, sort_keys=False, allow_unicode=True)
        print(path.name)


if __name__ == "__main__":
    main()