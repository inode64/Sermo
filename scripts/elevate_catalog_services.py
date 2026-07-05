#!/usr/bin/env python3
"""One-shot catalog elevation for minimal service profiles (steps 1–2)."""

from __future__ import annotations

import copy
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
SERVICES = ROOT / "catalog" / "services"

RESTART_TCP = {
    "restart-if-tcp-failed": {
        "type": "remediation",
        "if": {"failed": {"check": "tcp"}},
        "for": {"cycles": 3},
        "then": {"action": "restart"},
    }
}

RESTART_PORT = {
    "restart-if-port-failed": {
        "type": "remediation",
        "if": {"failed": {"check": "port"}},
        "for": {"cycles": 3},
        "then": {"action": "restart"},
    }
}

CPU_THREAD = {
    "restart-if-worker-thread-hot": {
        "type": "remediation",
        "if": {
            "metric": {
                "scope": "service",
                "name": "cpu_thread",
                "op": ">",
                "value": "90%",
            }
        },
        "for": {"duration": "6m"},
        "then": {"action": "restart"},
    }
}

ALERT_FDS = {
    "alert-if-fds-high": {
        "type": "alert",
        "if": {
            "metric": {
                "scope": "service",
                "name": "fds",
                "op": ">",
                "value": 50000,
            }
        },
        "within": {"cycles": 10, "min_matches": 3},
        "then": {
            "action": "alert",
            "message": "${display_name} file descriptors high (>50k) — raise the ulimit",
        },
    }
}

ALERT_MEMORY = {
    "alert-if-memory-high": {
        "type": "alert",
        "if": {
            "metric": {
                "scope": "service",
                "name": "memory",
                "op": ">",
                "value": "60%",
            }
        },
        "for": {"duration": "10m"},
        "then": {
            "action": "alert",
            "message": "${display_name} memory usage is high",
        },
    }
}

METRIC_FDS_CHECK = {
    "fds": {
        "type": "metric",
        "scope": "service",
        "name": "fds",
        "op": ">",
        "value": 50000,
        "optional": True,
    }
}

METRIC_MEMORY_CHECK = {
    "memory": {
        "type": "metric",
        "scope": "service",
        "name": "memory",
        "op": ">",
        "value": "60%",
        "optional": True,
    }
}


def restart_failed(check: str) -> dict:
    key = f"restart-if-{check}-failed"
    return {
        key: {
            "type": "remediation",
            "if": {"failed": {"check": check}},
            "for": {"cycles": 3},
            "then": {"action": "restart"},
        }
    }


def merge_rules(doc: dict, *blocks: dict) -> None:
    rules = doc.setdefault("rules", {})
    for block in blocks:
        for name, rule in block.items():
            if name not in rules:
                rules[name] = copy.deepcopy(rule)


def merge_checks(doc: dict, extra: dict) -> None:
    checks = doc.setdefault("checks", {})
    for name, chk in extra.items():
        if name not in checks:
            checks[name] = copy.deepcopy(chk)


def set_verify(doc: dict, check_name: str) -> None:
    # Flag the named health check to also run as post-operation start
    # verification (verify: true). This replaces the retired postflight: section,
    # which used to be a duplicate of the same check.
    chk = doc.get("checks", {}).get(check_name)
    if isinstance(chk, dict):
        chk["verify"] = True


def set_reload_on_change(doc: dict) -> None:
    if "reload_on_change" in doc:
        return
    paths = doc.get("config_files")
    if not paths:
        return
    if isinstance(paths, str):
        paths = [paths]
    doc["reload_on_change"] = {"paths": list(paths)}


# Per-catalog-service elevation specs: extra checks, optional preflight, rules, verify key.
SPECS: dict[str, dict] = {
    "apcsmart.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${apcsmart_binary}",
                "user": "root",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "bitbucketrunner.yml": {
        "rules": [ALERT_MEMORY, ALERT_FDS],
    },
    "bluetooth.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "/usr/libexec/bluetooth/bluetoothd",
                "user": "root",
                "state": "running",
                "requires": ["service"],
                "optional": True,
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "ceph-mds.yml": {
        "apps_add": ["ceph"],
        "checks": {
            "status": {
                "type": "command",
                "command": ["${ceph_binary}", "--admin-daemon", "${socket}", "status"],
                "timeout": "5s",
                "analyze": {"use": ["ceph"]},
            }
        },
        "verify": "status",
        "rules": [restart_failed("status"), ALERT_MEMORY],
    },
    "ceph-mgr.yml": {
        "apps_add": ["ceph"],
        "checks": {
            "status": {
                "type": "command",
                "command": ["${ceph_binary}", "--admin-daemon", "${socket}", "status"],
                "timeout": "5s",
                "analyze": {"use": ["ceph"]},
            }
        },
        "verify": "status",
        "rules": [restart_failed("status"), ALERT_MEMORY],
    },
    "ceph-osd.yml": {
        "apps_add": ["ceph"],
        "checks": {
            "status": {
                "type": "command",
                "command": ["${ceph_binary}", "--admin-daemon", "${socket}", "status"],
                "timeout": "5s",
                "analyze": {"use": ["ceph"]},
            }
        },
        "verify": "status",
        "rules": [restart_failed("status"), ALERT_MEMORY],
    },
    "containerd.yml": {
        "checks": {
            "version": {
                "type": "command",
                "command": ["${containerd_binary}", "--version"],
                "timeout": "5s",
            }
        },
        "verify": "version",
        "rules": [restart_failed("version"), CPU_THREAD, ALERT_FDS, ALERT_MEMORY],
        "reload": True,
        "metric_checks": True,
    },
    "dcc.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${dcc_binary}",
                "user": "${user}",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [CPU_THREAD, ALERT_FDS],
    },
    "dmeventd.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${dmeventd_binary}",
                "user": "root",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "fcron.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${fcron_binary}",
                "user": "root",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "fetchmail.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${fetchmail_binary}",
                "user": "root",
                "state": "running",
                "requires": ["service"],
                "optional": True,
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
        "reload": True,
    },
    "filebeat.yml": {
        "checks": {
            "config-output": {
                "type": "command",
                "command": ["${filebeat_binary}", "test", "output", "-c", "${config}"],
                "timeout": "30s",
                "optional": True,
            }
        },
        "verify": "config-output",
        "rules": [ALERT_FDS, ALERT_MEMORY],
        "reload": True,
        "metric_checks": True,
    },
    "firewalld.yml": {
        "checks": {
            "state": {
                "type": "command",
                "command": ["firewall-cmd", "state"],
                "timeout": "10s",
            }
        },
        "verify": "state",
        "rules": [restart_failed("state"), ALERT_MEMORY],
        "reload": True,
    },
    "freshclam.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${freshclam_binary}",
                "user": "${user}",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "garbd.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${garbd_binary}",
                "user": "root",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "irqbalance.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${irqbalance_binary}",
                "user": "root",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "libvirt-dbus.yml": {
        "checks": {"dbus": {"type": "dbus", "timeout": "5s"}},
        "verify": "dbus",
        "rules": [restart_failed("dbus"), ALERT_FDS],
    },
    "lldpd.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${lldpd_binary}",
                "user": "${user}",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "lm_sensors.yml": {
        "checks": {
            "sensors": {
                "type": "command",
                "command": ["${lm_sensors_binary}", "-A"],
                "timeout": "10s",
                "optional": True,
            }
        },
        "verify": "sensors",
    },
    "lvm.yml": {},
    "lvm2-monitor.yml": {},
    "networkmanager.yml": {
        "checks": {
            "status": {
                "type": "command",
                "command": ["nmcli", "general", "status"],
                "timeout": "10s",
            }
        },
        "verify": "status",
        "rules": [restart_failed("status"), CPU_THREAD, ALERT_FDS],
    },
    "nfsdcld.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${nfsdcld_binary}",
                "user": "${user}",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "nmbd.yml": {
        "checks": {
            "netbios": {"type": "tcp", "port": 137, "timeout": "3s", "optional": True},
            "smb": {"type": "smb", "port": 445, "timeout": "3s", "optional": True},
        },
        "verify": "netbios",
        "rules": [ALERT_MEMORY],
    },
    "node.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${node_binary}",
                "user": "root",
                "state": "running",
                "requires": ["service"],
                "optional": True,
            }
        },
        "verify": "process",
        "rules": [CPU_THREAD, ALERT_FDS, ALERT_MEMORY],
    },
    "numad.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${numad_binary}",
                "user": "root",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "ovsdb-client.yml": {
        "checks": {
            "openvswitch": {
                "type": "openvswitch",
                "socket": "/run/openvswitch/db.sock",
                "timeout": "5s",
                "optional": True,
            }
        },
        "verify": "openvswitch",
    },
    "pmie.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${pmie_binary}",
                "user": "${user}",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "pmie_farm.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${pmie_farm_binary}",
                "user": "${user}",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "pmlogger_farm.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${pmlogger_farm_binary}",
                "user": "${user}",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "polkit.yml": {
        "checks": {"dbus": {"type": "dbus", "timeout": "5s"}},
        "verify": "dbus",
        "rules": [restart_failed("dbus")],
    },
    "qemu-ga.yml": {
        "checks": {
            "version": {
                "type": "command",
                "command": ["${qemu_ga_binary}", "--version"],
                "timeout": "5s",
            }
        },
        "verify": "version",
        "rules": [restart_failed("version")],
    },
    "rasdaemon.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${rasdaemon_binary}",
                "user": "root",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "rngd.yml": {
        "apps_set": ["rngd"],
        "checks": {
            "process": {
                "type": "process",
                "exe": "${rngd_binary}",
                "user": "root",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
    },
    "rpc-idmapd.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${rpc_idmapd_binary}",
                "user": "root",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "rpc-mountd.yml": {
        "checks": {
            "mountd": {
                "type": "mountd",
                "host": "127.0.0.1",
                "port": 20048,
                "timeout": "3s",
                "optional": True,
            }
        },
        "verify": "mountd",
        "rules": [ALERT_MEMORY],
    },
    "rpc-pipefs.yml": {},
    "rpc-statd.yml": {
        "checks": {
            "statd": {
                "type": "statd",
                "host": "127.0.0.1",
                "port": 662,
                "timeout": "3s",
                "optional": True,
            }
        },
        "verify": "statd",
        "rules": [ALERT_MEMORY],
    },
    "rrdcached.yml": {
        "variables": {"port": 42217},
        "checks": {
            "tcp": {"type": "tcp", "host": "127.0.0.1", "port": "${port}", "timeout": "3s"}
        },
        "verify": "tcp",
        "rules": [RESTART_TCP, ALERT_FDS],
    },
    "salt-minion.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "/usr/bin/salt-minion",
                "user": "root",
                "state": "running",
                "requires": ["service"],
                "optional": True,
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "smartd.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${smartd_binary}",
                "user": "root",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "snmp-ups.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${snmp_ups_binary}",
                "user": "root",
                "state": "running",
                "requires": ["service"],
                "optional": True,
            }
        },
        "verify": "process",
    },
    "supervisord.yml": {
        "preflight": {
            "config": {
                "type": "command",
                "command": ["${supervisord_binary}", "check"],
                "timeout": "30s",
                "optional": True,
            }
        },
        "checks": {
            "ping": {
                "type": "command",
                "command": ["supervisorctl", "status"],
                "timeout": "15s",
                "optional": True,
            }
        },
        "verify": "ping",
        "rules": [restart_failed("ping"), CPU_THREAD, ALERT_FDS],
    },
    "syslog-ng.yml": {
        "checks": {
            "ctl": {
                "type": "command",
                "command": ["${syslog_ng_binary}", "stats"],
                "timeout": "10s",
                "optional": True,
            }
        },
        "verify": "ctl",
        "rules": [CPU_THREAD, ALERT_FDS],
        "reload": True,
        "metric_checks": True,
    },
    "tuned.yml": {
        "checks": {
            "active": {
                "type": "command",
                "command": ["tuned-adm", "active"],
                "timeout": "10s",
            }
        },
        "verify": "active",
        "rules": [restart_failed("active")],
    },
    "udisks2.yml": {
        "checks": {"udisks2": {"type": "udisks2", "timeout": "5s"}},
        "verify": "udisks2",
        "rules": [restart_failed("udisks2"), ALERT_MEMORY],
    },
    "upower.yml": {
        "checks": {"dbus": {"type": "dbus", "timeout": "5s"}},
        "verify": "dbus",
        "rules": [restart_failed("dbus")],
    },
    "upsdrv.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "/sbin/upsdrvctl",
                "user": "root",
                "state": "running",
                "requires": ["service"],
                "optional": True,
            }
        },
        "verify": "process",
    },
    "upsmon.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${upsmon_binary}",
                "user": "root",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [ALERT_MEMORY],
    },
    "usbhid-ups.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${usbhid_ups_binary}",
                "user": "root",
                "state": "running",
                "requires": ["service"],
                "optional": True,
            }
        },
        "verify": "process",
    },
    "virtlockd.yml": {
        "checks": {
            "libvirt": {
                "type": "libvirt",
                "socket": "/run/libvirt/virtlockd-sock",
                "timeout": "5s",
            }
        },
        "verify": "libvirt",
        "rules": [restart_failed("libvirt")],
    },
    "virtlogd.yml": {
        "checks": {
            "libvirt": {
                "type": "libvirt",
                "socket": "/run/libvirt/virtlogd-sock",
                "timeout": "5s",
            }
        },
        "verify": "libvirt",
        "rules": [restart_failed("libvirt")],
    },
    "virtnetworkd.yml": {
        "checks": {
            "libvirt": {
                "type": "libvirt",
                "socket": "/run/libvirt/virtnetworkd-sock",
                "timeout": "5s",
            }
        },
        "verify": "libvirt",
        "rules": [restart_failed("libvirt"), ALERT_FDS],
    },
    "xinetd.yml": {
        "checks": {
            "process": {
                "type": "process",
                "exe": "${xinetd_binary}",
                "user": "root",
                "state": "running",
                "requires": ["service"],
            }
        },
        "verify": "process",
        "rules": [CPU_THREAD, ALERT_FDS],
    },
    "zigbee2mqtt.yml": {
        "variables": {"port": 8080},
        "checks": {
            "http": {
                "type": "http",
                "url": "http://127.0.0.1:${port}/",
                "expect_status": {"op": "<", "value": 500},
                "timeout": "5s",
                "optional": True,
            }
        },
        "verify": "http",
        "rules": [restart_failed("http"), CPU_THREAD, ALERT_MEMORY],
    },
}

# Step 2/3 enrichments for already-rich profiles.
ENRICH: dict[str, dict] = {
    "postgres.yml": {
        "apps_add": [],
        "preflight_analyze": ["postgres"],
        "checks": {
            "ready": {
                "type": "postgres",
                "host": "127.0.0.1",
                "port": "${port}",
                "user": "${user}",
                "timeout": "5s",
            }
        },
        "verify": "ready",
        "rules": [restart_failed("ready"), RESTART_PORT, ALERT_MEMORY],
        "reload": True,
        "metric_checks": True,
    },
    "redis.yml": {
        "verify": "ping",
        "rules_extra": [ALERT_FDS, ALERT_MEMORY],
        "reload": True,
        "metric_checks": True,
    },
    "keydb.yml": {
        "verify": "ping",
        "rules_extra": [ALERT_FDS, ALERT_MEMORY],
        "reload": True,
        "metric_checks": True,
    },
    "nginx.yml": {
        "preflight_analyze": ["web"],
        "verify": "http",
        "rules_extra": [ALERT_FDS, ALERT_MEMORY],
        "reload": True,
        "metric_checks": True,
    },
    "apache.yml": {
        "preflight_analyze": ["web"],
        "verify": "http",
        "rules_extra": [ALERT_FDS, ALERT_MEMORY],
        "reload": True,
        "metric_checks": True,
    },
    "mysql.yml": {"verify": "ping", "rules_extra": [ALERT_FDS], "metric_checks": True},
    "mariadb.yml": {"verify": "ping", "rules_extra": [ALERT_FDS], "metric_checks": True},
    "mosquitto.yml": {
        "preflight": {
            "config": {
                "type": "command",
                "command": ["${mosquitto_binary}", "-c", "/etc/mosquitto/mosquitto.conf"],
                "timeout": "15s",
                "optional": True,
            }
        },
        "verify": "mqtt",
        "rules": [restart_failed("mqtt"), RESTART_PORT],
        "reload": True,
        "metric_checks": True,
    },
    "memcached.yml": {
        "verify": "memcached",
        "rules": [restart_failed("memcached"), RESTART_TCP, ALERT_FDS],
        "metric_checks": True,
    },
    "tomcat.yml": {
        "checks": {
            "http": {
                "type": "http",
                "url": "http://127.0.0.1:${port}/",
                "expect_status": {"op": "<", "value": 500},
                "timeout": "5s",
            }
        },
        "verify": "http",
        "rules": [restart_failed("http"), RESTART_PORT, CPU_THREAD, ALERT_FDS],
        "reload": True,
    },
    "rsync.yml": {
        "checks": {"rsync": {"type": "rsync", "host": "127.0.0.1", "port": "${port}", "timeout": "3s"}},
        "verify": "rsync",
        "rules": [restart_failed("rsync"), RESTART_TCP],
    },
    "docker.yml": {"verify": "engine", "rules_extra": [ALERT_FDS], "reload": True},
    "libvirtd.yml": {
        "verify": "libvirt",
        "rules": [restart_failed("libvirt"), ALERT_FDS],
    },
    "prometheus.yml": {"verify": "prometheus", "rules_extra": [ALERT_FDS], "reload": True},
    "grafana.yml": {"verify": "http", "rules_extra": [ALERT_FDS], "reload": True},
    "loki.yml": {"verify": "http", "rules_extra": [ALERT_FDS], "reload": True},
    "ceph-mon.yml": {
        "apps_add": ["ceph"],
        "checks": {
            "status": {
                "type": "command",
                "command": ["${ceph_binary}", "--admin-daemon", "${socket}", "mon_status"],
                "timeout": "5s",
                "analyze": {"use": ["ceph"]},
            }
        },
        "verify": "messenger",
        "rules_extra": [restart_failed("messenger"), restart_failed("status")],
    },
}


def apply_spec(path: Path, spec: dict) -> None:
    with path.open() as f:
        doc = yaml.safe_load(f)
    if not doc:
        raise SystemExit(f"empty document: {path}")

    if vars_add := spec.get("variables"):
        v = doc.setdefault("variables", {})
        v.update(vars_add)

    if apps_set := spec.get("apps_set"):
        doc["apps"] = list(apps_set)
    elif apps_add := spec.get("apps_add"):
        apps = doc.setdefault("apps", [])
        if isinstance(apps, str):
            apps = [apps]
            doc["apps"] = apps
        for app in apps_add:
            if app not in apps:
                apps.append(app)

    if pre := spec.get("preflight"):
        doc.setdefault("preflight", {}).update(copy.deepcopy(pre))

    if checks := spec.get("checks"):
        merge_checks(doc, checks)

    if pf := spec.get("verify"):  # spec value names the check to verify after start
        set_verify(doc, pf)

    if spec.get("reload"):
        set_reload_on_change(doc)

    if spec.get("metric_checks"):
        merge_checks(doc, METRIC_FDS_CHECK)
        merge_checks(doc, METRIC_MEMORY_CHECK)

    for block in spec.get("rules", []):
        merge_rules(doc, block)
    for block in spec.get("rules_extra", []):
        merge_rules(doc, block)

    if analyze := spec.get("preflight_analyze"):
        pre = doc.get("preflight", {}).get("config")
        if isinstance(pre, dict):
            use = pre.setdefault("analyze", {}).setdefault("use", [])
            for pattern in analyze:
                if pattern not in use:
                    use.append(pattern)

    with path.open("w") as f:
        yaml.dump(doc, f, default_flow_style=False, sort_keys=False, allow_unicode=True)


def main() -> int:
    updated = 0
    for name, spec in {**SPECS, **ENRICH}.items():
        path = SERVICES / name
        if not path.exists():
            print(f"skip missing {name}", file=sys.stderr)
            continue
        apply_spec(path, spec)
        updated += 1
        print(f"updated {name}")
    print(f"done: {updated} files")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())