#!/usr/bin/env python3
"""Generate deployable Sermo remote-install configs from host inventory.

The input stage root must contain one directory per host. Each host directory
must contain an ``out/`` directory with the read-only evidence collected on that
host, including ``sermoctl --json services`` output and host resource snapshots.

For each host this script writes:

* ``<configs-root>/<host>/root/etc/sermo/...`` with one YAML document per target
* ``<configs-root>/<host>/sermo-config.tgz`` ready to extract at ``/``
* a JSON report listing enabled services, generated watches and skips

The generated configuration is intentionally dry-run by default. Local storage
capacity watches may emit ``then.expand`` actions, but dry-run prevents automatic
execution. Fstab-backed local storage watches also expose mount units. Network
filesystems are generated as mount-only watches so a stale NFS/CIFS mount cannot
block the daemon in ``statfs``. NFS mounts also receive a separate native NFS
endpoint check against their configured server.
"""

from __future__ import annotations

import argparse
import ipaddress
import json
import re
import shutil
import tarfile
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import urlsplit

import yaml


PSEUDO_FS = {
    "autofs",
    "binfmt_misc",
    "bpf",
    "cgroup",
    "cgroup2",
    "configfs",
    "debugfs",
    "devpts",
    "devtmpfs",
    "efivarfs",
    "fusectl",
    "hugetlbfs",
    "mqueue",
    "nsfs",
    "proc",
    "pstore",
    "rpc_pipefs",
    "securityfs",
    "selinuxfs",
    "squashfs",
    "sysfs",
    "tracefs",
    "tmpfs",
}

REAL_STORAGE_FS = {
    "bcachefs",
    "btrfs",
    "ceph",
    "cifs",
    "ext2",
    "ext3",
    "ext4",
    "exfat",
    "f2fs",
    "fuse.sshfs",
    "gfs2",
    "glusterfs",
    "nfs",
    "nfs4",
    "ntfs",
    "ocfs2",
    "reiserfs",
    "smb3",
    "vfat",
    "xfs",
    "zfs",
}

NETWORK_FS = {"nfs", "nfs4", "cifs", "smb3", "fuse.sshfs", "sshfs", "ceph", "glusterfs"}
NFS_FILESYSTEMS = {"nfs", "nfs4"}
SKIP_MOUNT_PREFIXES = (
    "/dev",
    "/proc",
    "/run",
    "/sys",
    "/var/lib/containerd",
    "/var/lib/docker/overlay2",
    "/var/lib/kubelet/pods",
)
SKIP_IFACE_PREFIXES = (
    "br-",
    "cni",
    "docker",
    "flannel",
    "kube",
    "lo",
    "veth",
    "virbr",
)

GEOIP_DATABASE_DIRECTORY = "/usr/share/GeoIP"
GEOIP_DATABASE_OLDER_THAN = "480h"
ROOT_MOUNT_TARGET = "/"
NFS_ENDPOINT_TIMEOUT = "5s"
ENDPOINT_CHECK_TYPES = {"dns", "http", "ports", "tcp"}
TCP_PROTOCOL = "tcp"
UDP_PROTOCOL = "udp"
WILDCARD_LISTEN_HOSTS = {"0.0.0.0", "::"}


@dataclass(frozen=True)
class GenerationOptions:
    web_port: int
    web_password: str
    storage_free_pct: str
    expand_by: str
    smart_interval: str
    hdparm_interval: str
    active_services_only: bool
    catalog_services_dir: Path


def read_text(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8", errors="replace")
    except FileNotFoundError:
        return ""


def read_json(path: Path) -> dict:
    text = read_text(path)
    if not text.strip():
        return {}
    try:
        data = json.loads(text)
    except json.JSONDecodeError:
        return {}
    return data if isinstance(data, dict) else {}


def read_json_list(path: Path) -> list[dict]:
    text = read_text(path)
    if not text.strip():
        return []
    try:
        data = json.loads(text)
    except json.JSONDecodeError:
        return []
    return [item for item in data if isinstance(item, dict)] if isinstance(data, list) else []


def yaml_quote(value: object) -> str:
    return json.dumps(str(value), ensure_ascii=False)


def slug(value: str, max_len: int = 72) -> str:
    value = value.strip().lower()
    value = value.replace("/", "-").replace("_", "-").replace(".", "-").replace("@", "-")
    value = re.sub(r"[^a-z0-9-]+", "-", value)
    value = re.sub(r"-+", "-", value).strip("-")
    if not value:
        value = "target"
    return value[:max_len].strip("-") or "target"


def ensure_dirs(root: Path) -> None:
    for rel in [
        "etc/sermo/services",
        "etc/sermo/apps",
        "etc/sermo/notifiers",
        "etc/sermo/watches",
        "etc/sermo/networks",
        "etc/sermo/storages",
        "etc/sermo/mounts",
        "etc/sermo/templates",
    ]:
        (root / rel).mkdir(parents=True, exist_ok=True)


def write_file(root: Path, rel: str, body: str) -> None:
    path = root / rel
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(body.rstrip() + "\n", encoding="utf-8")


def base_config(options: GenerationOptions, backend: str = "auto") -> str:
    if backend not in {"systemd", "openrc"}:
        backend = "auto"
    return f"""engine:
  backend: {backend}
  interval: 30s
  max_parallel_checks: 8
  max_parallel_operations: 2
  default_timeout: 10s
  operation_timeout: 90s
  startup_delay: 0
  user_lookup: auto
  user_lookup_timeout: 250ms

paths:
  services:
    - /etc/sermo/services
  apps:
    - /etc/sermo/apps
  notifiers:
    - /etc/sermo/notifiers
  watches:
    - /etc/sermo/watches
    - /etc/sermo/networks
    - /etc/sermo/storages
    - /etc/sermo/mounts
  runtime: /run/sermo
  state: /var/lib/sermo
  templates: /etc/sermo/templates

defaults:
  dry_run: true
  stop_policy:
    graceful_timeout: 30s
    term_timeout: 15s
    kill_timeout: 5s
    force_kill: false
  policy:
    cooldown: 5m

web:
  address: 0.0.0.0
  port: {options.web_port}
  password: {yaml_quote(options.web_password)}
"""


def simple_watch(
    name: str,
    category: str,
    interval: str,
    check_lines: list[str],
    cycles: int = 10,
    then_lines: list[str] | None = None,
    policy: bool = False,
    display_name: str | None = None,
) -> str:
    body = [
        f"name: {name}",
    ]
    if display_name and display_name != name:
        body.append(f"display_name: {yaml_quote(display_name)}")
    body.extend([
        f"category: {category}",
        "monitor: enabled",
        "dry_run: true",
        f"interval: {interval}",
        "check:",
    ])
    body.extend(f"  {line}" for line in check_lines)
    if cycles:
        body.append(f"for: {{ cycles: {cycles} }}")
    if then_lines:
        body.append("then:")
        body.extend(f"  {line}" for line in then_lines)
    if policy:
        body.append("policy: { cooldown: 30m, max_actions: 3, max_actions_window: 24h }")
    return "\n".join(body)


def mount_unit_block() -> str:
    return "\nmount:\n  refcount: true\n"


def metric_watch(name: str, category: str, interval: str, check_lines: list[str], metric_blocks: list[tuple[str, list[str]]]) -> str:
    body = [
        f"name: {name}",
        f"category: {category}",
        "monitor: enabled",
        "dry_run: true",
        f"interval: {interval}",
        "check:",
    ]
    body.extend(f"  {line}" for line in check_lines)
    body.append("metrics:")
    for metric, lines in metric_blocks:
        body.append(f"  {metric}:")
        body.extend(f"    {line}" for line in lines)
    return "\n".join(body)


def controlled_docker_service(name: str, display_name: str, container: str, socket: str) -> str:
    return f"""name: {name}
display_name: {yaml_quote(display_name)}
category: docker
monitor: enabled
dry_run: true
control:
  type: docker
  container: {yaml_quote(container)}
  socket: {yaml_quote(socket)}
watches:
  docker:
    check:
      type: docker
      socket: {yaml_quote(socket)}
      container: {yaml_quote(container)}
      on_change: true
      expect:
        container.status: {{ op: "==", value: running }}
"""


def controlled_vm_service(name: str, display_name: str, domain: str, uri: str, socket: str) -> str:
    return f"""name: {name}
display_name: {yaml_quote(display_name)}
category: virtual-machine
monitor: enabled
dry_run: true
control:
  type: libvirt
  uri: {yaml_quote(uri)}
  domain: {yaml_quote(domain)}
  socket: {yaml_quote(socket)}
watches:
  vm:
    check:
      type: libvirt
      query: {yaml_quote(uri)}
      domain: {yaml_quote(domain)}
      socket: {yaml_quote(socket)}
      on_change: true
      expect:
        domain.state: {{ op: "==", value: running }}
"""


def flatten_findmnt(nodes: list[dict] | None) -> list[dict]:
    out: list[dict] = []
    for node in nodes or []:
        out.append(node)
        out.extend(flatten_findmnt(node.get("children", [])))
    return out


def mount_is_local_storage(mount: dict) -> bool:
    target = mount.get("target") or ""
    fstype = (mount.get("fstype") or "").lower()
    if not target.startswith("/"):
        return False
    if target != ROOT_MOUNT_TARGET and any(target == prefix or target.startswith(prefix + "/") for prefix in SKIP_MOUNT_PREFIXES):
        return False
    if fstype in PSEUDO_FS or fstype in NETWORK_FS:
        return False
    if fstype in REAL_STORAGE_FS:
        return True
    return fstype.startswith("fuse.") and fstype != "fusectl"


def parse_fstab(text: str) -> list[dict[str, str]]:
    entries: list[dict[str, str]] = []
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        parts = line.split()
        if len(parts) < 3:
            continue
        source, target, fstype = parts[:3]
        if target != "none" and target.startswith("/"):
            entries.append({"source": source, "target": target, "fstype": fstype.lower()})
    return entries


def nfs_server_from_source(source: str) -> str:
    """Return the server portion of an NFS fstab source, if it is unambiguous."""
    source = source.strip()
    if source.startswith("["):
        end = source.find("]")
        if end > 1 and source[end + 1 :].startswith(":/"):
            return source[1:end]
        return ""
    host, separator, path = source.partition(":")
    if not separator or not host or not path.startswith("/"):
        return ""
    return host


def parse_nfs_routes(text: str) -> dict[str, dict[str, str]]:
    routes: dict[str, dict[str, str]] = {}
    for line in text.splitlines():
        host, address, iface = (line.split("\t") + ["", "", ""])[:3]
        if host and iface:
            routes[host] = {"address": address, "interface": iface}
    return routes


def walk_lsblk_devices(devices: list[dict] | None) -> list[dict]:
    out: list[dict] = []
    for dev in devices or []:
        out.append(dev)
        out.extend(walk_lsblk_devices(dev.get("children", [])))
    return out


def truthy(value: object) -> bool:
    if isinstance(value, bool):
        return value
    return str(value).lower() in {"1", "true", "yes"}


# Network block devices (NBD, DRBD) expose no SMART data: smartctl cannot
# query them, so no smart watch must ever be generated for these disks.
# They still get diskio watches — /proc/diskstats covers them fine.
SMARTLESS_DISK_PREFIXES = ("nbd", "drbd")


def block_inventory(stage: Path) -> tuple[list[dict], set[str]]:
    data = read_json(stage / "lsblk.json")
    disks: list[dict] = []
    usb_targets: set[str] = set()
    for dev in walk_lsblk_devices(data.get("blockdevices", [])):
        typ = str(dev.get("type") or "").lower()
        name = dev.get("name") or dev.get("kname") or ""
        path = dev.get("path") or (f"/dev/{name}" if name else "")
        ro = truthy(dev.get("ro"))
        tran = str(dev.get("tran") or "").lower()
        rm = truthy(dev.get("rm"))
        if typ == "disk" and path and not ro and not name.startswith(("loop", "sr", "ram")):
            disks.append({"name": name, "path": path, "tran": tran, "rm": rm})
        mounts = dev.get("mountpoints")
        if mounts is None and dev.get("mountpoint"):
            mounts = [dev.get("mountpoint")]
        for mountpoint in mounts or []:
            if mountpoint and (rm or tran == "usb"):
                usb_targets.add(mountpoint)
    return disks, usb_targets


def parse_features(stage: Path) -> dict[str, str]:
    features: dict[str, str] = {}
    for line in read_text(stage / "features").splitlines():
        if "=" in line:
            key, value = line.split("=", 1)
            features[key.strip()] = value.strip()
    return features


def parse_md_arrays(stage: Path) -> list[str]:
    """Return every Linux md array named by the staged /proc/mdstat sample."""
    names = {
        match.group(1)
        for match in re.finditer(r"^(md[A-Za-z0-9_.-]+)\s*:", read_text(stage / "proc_mdstat"), re.MULTILINE)
    }
    return sorted(names)


def parse_active_units(stage: Path) -> set[str]:
    init = read_text(stage / "init").strip()
    active: set[str] = set()
    if init == "systemd":
        for line in read_text(stage / "active_units").splitlines():
            fields = line.split()
            if not fields:
                continue
            unit = fields[0]
            active.add(unit)
            if unit.endswith(".service"):
                active.add(unit[: -len(".service")])
            else:
                active.add(unit + ".service")
        return active
    if init == "openrc":
        for line in read_text(stage / "openrc_status_all").splitlines():
            if not re.search(r"\[\s*started(?:\s|\])", line):
                continue
            name = line.strip().split()[0]
            if name:
                active.add(name)
        return active
    return active


def host_builtins(stage: Path) -> dict[str, str]:
    hostname = read_text(stage / "hostname").strip()
    short = hostname.split(".", 1)[0] if hostname else ""
    return {"hostname": short, "host": short, "current": ""}


def load_catalog_services(catalog_services_dir: Path) -> list[dict]:
    docs: list[dict] = []
    if not catalog_services_dir.exists():
        return docs
    for path in sorted(catalog_services_dir.rglob("*.yml")):
        try:
            data = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
        except yaml.YAMLError:
            continue
        if isinstance(data, dict) and data.get("name"):
            docs.append(data)
    return docs


def catalog_name_regex(pattern: str) -> re.Pattern[str]:
    out = []
    idx = 0
    while idx < len(pattern):
        token = pattern[idx : idx + 2]
        if token == "%v":
            rest = pattern[idx + 2 :]
            terminal = not any(marker in rest for marker in ("%n", "%s", "%i"))
            capture = r"[0-9][^/]*" if terminal else r"[0-9][^/_-]*"
            out.append(rf"(?P<version>{capture})")
            idx += 2
            continue
        if token == "%n":
            out.append(r"(?P<n>[0-9]+)")
            idx += 2
            continue
        if token == "%i":
            out.append(r"(?P<instance>(?:[A-Za-z0-9][A-Za-z0-9_.-]*)?)")
            idx += 2
            continue
        if token == "%s":
            out.append(r"(?P<sep>[-_]?)")
            idx += 2
            continue
        out.append(re.escape(pattern[idx]))
        idx += 1
    return re.compile("^" + "".join(out) + "$")


def catalog_doc_for_service(name: str, catalog_docs: list[dict]) -> tuple[dict | None, dict[str, str]]:
    for doc in catalog_docs:
        if doc.get("name") == name:
            return doc, {}
    for doc in catalog_docs:
        pattern = str(doc.get("name") or "")
        if "%" not in pattern:
            continue
        match = catalog_name_regex(pattern).match(name)
        if not match:
            continue
        values = {key: value for key, value in match.groupdict().items() if value is not None}
        return doc, values
    return None, {}


def values_for_service(name: str, stage: Path, catalog_docs: list[dict]) -> tuple[dict | None, dict[str, str]]:
    doc, values = catalog_doc_for_service(name, catalog_docs)
    merged = host_builtins(stage)
    merged.update(values)
    return doc, merged


def substitute_unit_vars(value: str, values: dict[str, str]) -> str:
    def repl(match: re.Match[str]) -> str:
        return values.get(match.group(1), "")

    return re.sub(r"\$\{([^}]+)\}", repl, value)


def service_unit_candidates(service_field: object, init: str, values: dict[str, str], fallback: str) -> list[str]:
    raw: object = service_field
    if isinstance(raw, dict):
        raw = raw.get(init) or raw.get("default") or []
    if isinstance(raw, str):
        items = [raw]
    elif isinstance(raw, list):
        items = [item for item in raw if isinstance(item, str)]
    else:
        items = [fallback]
    out: list[str] = []
    for item in items:
        item = substitute_unit_vars(item, values).strip()
        if not item:
            continue
        out.append(item)
        if init == "systemd":
            if item.endswith(".service"):
                out.append(item[: -len(".service")])
            else:
                out.append(item + ".service")
    return list(dict.fromkeys(out))


def active_service_filter(stage: Path, catalog_docs: list[dict]) -> tuple[set[str], dict[str, list[str]], bool]:
    active_units = parse_active_units(stage)
    init = read_text(stage / "init").strip()
    data = read_json(stage / "services_json.out")
    reports = data.get("services", []) if isinstance(data, dict) else []
    active_services: set[str] = set()
    candidates_by_service: dict[str, list[str]] = {}
    if not active_units:
        return active_services, candidates_by_service, False
    for rep in reports:
        name = rep.get("name") or ""
        if not name:
            continue
        doc, values = values_for_service(name, stage, catalog_docs)
        service_field = doc.get("service") if doc else None
        candidates = service_unit_candidates(service_field, init, values, name)
        candidates_by_service[name] = candidates
        if any(candidate in active_units for candidate in candidates):
            active_services.add(name)
    return active_services, candidates_by_service, True


def parse_services(stage: Path, options: GenerationOptions) -> tuple[list[dict], list[dict]]:
    data = read_json(stage / "services_json.out")
    reports = data.get("services", []) if isinstance(data, dict) else []
    catalog_docs = load_catalog_services(options.catalog_services_dir)
    active_services, candidates_by_service, active_inventory_ok = active_service_filter(stage, catalog_docs)
    services: list[dict] = []
    skipped: list[dict] = []
    for rep in reports:
        name = rep.get("name") or ""
        installed_ok = rep.get("installed") and rep.get("ok") and name
        active_ok = not options.active_services_only or (active_inventory_ok and name in active_services)
        if installed_ok and active_ok:
            services.append(rep)
        else:
            reason = rep.get("status", "")
            if installed_ok and options.active_services_only:
                if not active_inventory_ok:
                    reason = "installed but active unit inventory unavailable"
                elif name not in active_services:
                    candidates = ", ".join(candidates_by_service.get(name, []))
                    reason = "installed but no active unit matched"
                    if candidates:
                        reason += f" ({candidates})"
            skipped.append(
                {
                    "name": name or rep.get("display_name", ""),
                    "status": reason,
                    "installed": bool(rep.get("installed")),
                    "ok": bool(rep.get("ok")),
                }
            )
    return services, skipped


def parse_interfaces(stage: Path) -> list[str]:
    interfaces: list[str] = []
    for line in read_text(stage / "ip_link").splitlines():
        match = re.match(r"\d+:\s+([^:]+):\s+<([^>]*)>", line)
        if not match:
            continue
        iface = match.group(1).split("@", 1)[0]
        flags = set(match.group(2).split(","))
        if any(iface == prefix or iface.startswith(prefix) for prefix in SKIP_IFACE_PREFIXES):
            continue
        if "UP" not in flags and "LOWER_UP" not in flags:
            continue
        interfaces.append(iface)
    return list(dict.fromkeys(interfaces))


def interfaces_with_addresses(stage: Path) -> set[str]:
    interfaces: set[str] = set()
    for filename in ("ip_addr4", "ip_addr6"):
        for line in read_text(stage / filename).splitlines():
            match = re.match(r"\d+:\s+([^\s:]+).*\s(?:inet|inet6)\s+", line)
            if match:
                interfaces.add(match.group(1).split("@", 1)[0])
    return interfaces


def parse_default_routes(stage: Path) -> list[dict[str, str]]:
    routes: list[dict[str, str]] = []
    for line in read_text(stage / "ip_route4").splitlines():
        if not line.startswith("default "):
            continue
        via = ""
        iface = ""
        parts = line.split()
        for idx, part in enumerate(parts):
            if part == "via" and idx + 1 < len(parts):
                via = parts[idx + 1]
            if part == "dev" and idx + 1 < len(parts):
                iface = parts[idx + 1]
        if iface:
            routes.append({"family": "ipv4", "iface": iface, "via": via})
    for line in read_text(stage / "ip_route6").splitlines():
        if not line.startswith("default "):
            continue
        iface = ""
        parts = line.split()
        for idx, part in enumerate(parts):
            if part == "dev" and idx + 1 < len(parts):
                iface = parts[idx + 1]
        if iface:
            routes.append({"family": "ipv6", "iface": iface, "via": ""})
    return routes


def parse_ipv4_interfaces(stage: Path) -> list[ipaddress.IPv4Interface]:
    interfaces: list[ipaddress.IPv4Interface] = []
    for line in read_text(stage / "ip_addr4").splitlines():
        match = re.search(r"\binet\s+([0-9.]+/\d+)", line)
        if not match:
            continue
        try:
            interfaces.append(ipaddress.IPv4Interface(match.group(1)))
        except ValueError:
            continue
    return interfaces


def normalize_connect_host(host: str, default_host: str) -> str:
    host = host.strip().strip("[]")
    if host in {"", "*", "0.0.0.0", "::", "[::]"}:
        return default_host
    if host == "localhost":
        return "127.0.0.1"
    return host


def parse_endpoint(value: str, default_host: str, default_port: str) -> tuple[str, str] | None:
    value = value.strip().strip("\"'").strip()
    if not value:
        return None
    value = re.sub(r"^[A-Za-z][A-Za-z0-9+.-]*://", "", value)
    value = value.split("/", 1)[0].strip().strip("\"'")
    if not value:
        return None
    host = default_host
    port = default_port
    if value.startswith("["):
        end = value.find("]")
        if end >= 0:
            host = value[1:end]
            rest = value[end + 1 :]
            if rest.startswith(":") and rest[1:].isdigit():
                port = rest[1:]
    elif value.startswith(":") and value[1:].isdigit():
        port = value[1:]
    elif value.isdigit():
        port = value
    elif value.count(":") == 1:
        left, right = value.rsplit(":", 1)
        if right.isdigit():
            host = left
            port = right
        else:
            host = value
    else:
        host = value
    if not port.isdigit():
        return None
    return normalize_connect_host(host, default_host), port


def parse_web_listen_flag(text: str) -> str:
    match = re.search(r"--web\.listen-address(?:=|\s+)([^\s\"']+)", text)
    return match.group(1).strip() if match else ""


def parse_cloudflared_endpoint(hints: str) -> tuple[dict[str, str], str] | None:
    for line in hints.splitlines():
        if not line.startswith("cloudflared.metrics "):
            continue
        idx = line.find("metrics:")
        if idx < 0:
            continue
        value = line[idx + len("metrics:") :].split("#", 1)[0].strip()
        endpoint = parse_endpoint(value, "127.0.0.1", "60123")
        if endpoint:
            host, port = endpoint
            return {"host": host, "port": port}, line.split(" ", 1)[1].split(":", 2)[0]
    return socket_endpoint(hints, "cloudflared", "60123", "127.0.0.1")


def parse_mysqld_exporter_endpoint(hints: str) -> tuple[dict[str, str], str] | None:
    for line in hints.splitlines():
        if not line.startswith("mysqld_exporter.listen "):
            continue
        value = parse_web_listen_flag(line)
        endpoint = parse_endpoint(value, "127.0.0.1", "9104")
        if endpoint:
            host, port = endpoint
            return {"host": host, "port": port}, line.split(" ", 1)[1].split(":", 1)[0]
    return socket_endpoint(hints, "mysqld_exporter", "9104", "127.0.0.1")


def parse_bind_entries(line: str) -> list[tuple[str, str]]:
    if "listen-on-v6" in line:
        return []
    idx = line.find("listen-on")
    if idx < 0:
        return []
    text = line[idx:]
    port_match = re.search(r"\bport\s+(\d+)", text)
    port = port_match.group(1) if port_match else "53"
    body_match = re.search(r"\{([^}]*)\}", text)
    if not body_match:
        return []
    entries = []
    for raw in body_match.group(1).split(";"):
        entry = raw.strip().strip("\"'")
        if entry:
            entries.append((entry, port))
    return entries


def bind_entry_host(entry: str, interfaces: list[ipaddress.IPv4Interface]) -> str:
    lowered = entry.lower()
    if lowered in {"none", "::1"}:
        return ""
    if lowered in {"any", "0.0.0.0", "localhost"}:
        return "127.0.0.1"
    if lowered == "localnets":
        return str(interfaces[0].ip) if interfaces else ""
    try:
        if "/" in entry:
            network = ipaddress.IPv4Network(entry, strict=False)
            for iface in interfaces:
                if iface.ip in network:
                    return str(iface.ip)
            return ""
        address = ipaddress.IPv4Address(entry)
        return str(address)
    except ValueError:
        return ""


def parse_named_endpoint(stage: Path, hints: str) -> tuple[dict[str, str], str] | None:
    interfaces = parse_ipv4_interfaces(stage)
    fallback: tuple[dict[str, str], str] | None = None
    for line in hints.splitlines():
        if not line.startswith("named.listen "):
            continue
        source = line.split(" ", 1)[1].split(":", 1)[0]
        for entry, port in parse_bind_entries(line):
            host = bind_entry_host(entry, interfaces)
            if not host:
                continue
            candidate = ({"host": host, "port": port}, source)
            if port == "53":
                return candidate
            if fallback is None:
                fallback = candidate
    if fallback is not None:
        return fallback
    return socket_endpoint(hints, "named", "53", "127.0.0.1")


def parse_process_user_hint(hints: str, process: str) -> tuple[dict[str, str], str] | None:
    hint = parse_process_hint(hints, process)
    if hint is not None:
        return {"user": hint["user"]}, f"{process} process owner"
    return None


def parse_process_hint(hints: str, process: str) -> dict[str, str] | None:
    pattern = re.compile(rf"^process {re.escape(process)} user ([^\s]+) exe (.+)$")
    for line in hints.splitlines():
        match = pattern.match(line.strip())
        if match:
            return {"user": match.group(1), "exe": match.group(2)}
    return None


def service_process_override(stage: Path, name: str) -> str:
    if name != "cloudflared" or read_text(stage / "init").strip() != "openrc":
        return ""
    hint = parse_process_hint(read_text(stage / "service_endpoint_hints"), "cloudflared")
    if hint is None or not hint["exe"].endswith(" (deleted)"):
        return ""
    cmd = r"(^|/)cloudflared(?:\s|$).*\stunnel\s+run(?:\s|$)"
    return f"""processes:
  main:
    exe: ""
    cmd: {yaml_quote(cmd)}
    user: "${{user}}"
"""


def socket_endpoint(hints: str, process: str, default_port: str, default_host: str) -> tuple[dict[str, str], str] | None:
    fallback: tuple[dict[str, str], str] | None = None
    for line in hints.splitlines():
        if not line.startswith("socket ") or f'("{process}"' not in line:
            continue
        endpoint = socket_line_endpoint(line, default_host, default_port)
        if not endpoint:
            continue
        host, port = endpoint
        if port != default_port:
            continue
        candidate = ({"host": host, "port": port}, "listening socket")
        if host not in {"127.0.0.1", "::1"}:
            return candidate
        if fallback is None:
            fallback = candidate
    return fallback


def socket_line_endpoint(line: str, default_host: str, default_port: str) -> tuple[str, str] | None:
    fields = line.split()
    for field in fields:
        if ":" not in field or field.endswith(":*"):
            continue
        endpoint = parse_endpoint(field, default_host, default_port)
        if endpoint:
            return endpoint
    return None


def service_variable_overrides(stage: Path, name: str) -> tuple[dict[str, str], str]:
    hints = read_text(stage / "service_endpoint_hints")
    if not hints.strip():
        return {}, ""
    overrides: dict[str, str] = {}
    sources: list[str] = []
    endpoint: tuple[dict[str, str], str] | None
    if name == "cloudflared":
        endpoint = parse_cloudflared_endpoint(hints)
    elif name == "mysqld_exporter":
        endpoint = parse_mysqld_exporter_endpoint(hints)
    elif name == "named":
        endpoint = parse_named_endpoint(stage, hints)
    else:
        endpoint = None
    if endpoint is not None:
        endpoint_overrides, endpoint_source = endpoint
        overrides.update(endpoint_overrides)
        sources.append(endpoint_source)
    if name in {"cloudflared", "named"}:
        process_user = parse_process_user_hint(hints, name)
        if process_user is not None:
            user_overrides, user_source = process_user
            overrides.update(user_overrides)
            sources.append(user_source)
    return overrides, ", ".join(dict.fromkeys(source for source in sources if source))


def substitute_variables(value: object, variables: dict[str, str]) -> str:
    return re.sub(r"\$\{([^}]+)\}", lambda match: variables.get(match.group(1), ""), str(value))


def effective_service_variables(doc: dict, values: dict[str, str], overrides: dict[str, str]) -> dict[str, str]:
    variables = {key: str(value) for key, value in doc.get("variables", {}).items() if isinstance(key, str)}
    for key, value in values.items():
        if value or key not in variables:
            variables[key] = value
    variables.update(overrides)
    return {key: substitute_variables(value, variables) for key, value in variables.items()}


def profile_process_names(doc: dict) -> set[str]:
    names = {str(doc.get("name") or "")}
    for key in ("aliases", "apps"):
        value = doc.get(key)
        if isinstance(value, list):
            names.update(str(item) for item in value)
    service = doc.get("service")
    if isinstance(service, str):
        names.add(service)
    elif isinstance(service, dict):
        for value in service.values():
            if isinstance(value, str):
                names.add(value)
            elif isinstance(value, list):
                names.update(str(item) for item in value)
    return {name.removesuffix(".service").split("@", 1)[0] for name in names if name}


def socket_listeners(hints: str) -> list[dict[str, object]]:
    listeners: list[dict[str, object]] = []
    for line in hints.splitlines():
        if not line.startswith("socket "):
            continue
        fields = line.split()
        if len(fields) < 2 or fields[1] not in {TCP_PROTOCOL, UDP_PROTOCOL}:
            continue
        endpoint = socket_line_endpoint(line, "0.0.0.0", "")
        if endpoint is None:
            continue
        host, port = endpoint
        if not port:
            continue
        processes = set(re.findall(r'\(\("([^"]+)"', line))
        if not processes:
            continue
        listeners.append({"protocol": fields[1], "host": host, "port": port, "processes": processes})
    return listeners


def endpoint_target(check_type: str, check: dict, variables: dict[str, str]) -> tuple[list[tuple[set[str], str, str]], str]:
    host = substitute_variables(check.get("host", variables.get("host", "127.0.0.1")), variables)
    if check_type == "http":
        url = substitute_variables(check.get("url", ""), variables)
        try:
            parsed = urlsplit(url)
            port = str(parsed.port or (443 if parsed.scheme == "https" else 80))
        except ValueError:
            return [], "invalid HTTP URL"
        if parsed.scheme not in {"http", "https"} or not parsed.hostname:
            return [], "HTTP URL has no usable local endpoint"
        return [({TCP_PROTOCOL}, normalize_connect_host(parsed.hostname, "127.0.0.1"), port)], ""
    if check_type == "dns":
        port = substitute_variables(check.get("port", variables.get("port", "53")), variables)
        if not port.isdigit():
            return [], "invalid DNS port"
        return [({TCP_PROTOCOL, UDP_PROTOCOL}, host, port)], ""
    if check_type == "tcp":
        port = substitute_variables(check.get("port", variables.get("port", "")), variables)
        if not port.isdigit():
            return [], "invalid TCP port"
        return [({TCP_PROTOCOL}, host, port)], ""
    values = substitute_variables(check.get("ports", ""), variables)
    ports = [item.strip() for item in values.split(",") if item.strip()]
    if any("-" in item for item in ports):
        return [], "ports range cannot be proven from one listening endpoint"
    if not ports or any(not item.isdigit() for item in ports):
        return [], "invalid ports list"
    return [({TCP_PROTOCOL}, host, port) for port in ports], ""


def listener_matches(target: tuple[set[str], str, str], listener: dict[str, object], processes: set[str]) -> bool:
    protocols, host, port = target
    listener_host = str(listener["host"])
    listener_processes = set(listener["processes"])
    host_matches = listener_host in WILDCARD_LISTEN_HOSTS or listener_host == host
    return str(listener["protocol"]) in protocols and str(listener["port"]) == port and host_matches and bool(listener_processes & processes)


def endpoint_watch_overrides(stage: Path, doc: dict, variables: dict[str, str]) -> tuple[set[str], list[dict[str, object]]]:
    hints = read_text(stage / "service_endpoint_hints")
    listeners = socket_listeners(hints)
    processes = profile_process_names(doc)
    disabled: set[str] = set()
    report: list[dict[str, object]] = []
    watches = doc.get("watches", {})
    if not isinstance(watches, dict):
        return disabled, report
    for watch_name, raw_watch in watches.items():
        if not isinstance(raw_watch, dict):
            continue
        check = raw_watch.get("check")
        if not isinstance(check, dict):
            continue
        check_type = str(check.get("type") or "")
        if check_type not in ENDPOINT_CHECK_TYPES:
            continue
        targets, reason = endpoint_target(check_type, check, variables)
        active = bool(targets) and all(any(listener_matches(target, listener, processes) for listener in listeners) for target in targets)
        item: dict[str, object] = {"watch": str(watch_name), "type": check_type, "active": active}
        if active:
            item["source"] = "associated listening socket"
        else:
            disabled.add(str(watch_name))
            item["reason"] = reason or "no associated listening socket for configured endpoint"
        report.append(item)
    return disabled, report
def docker_container_name(entry: dict) -> str:
    names = entry.get("Names")
    if isinstance(names, list):
        for raw in names:
            name = str(raw).strip().lstrip("/")
            if name:
                return name
    if isinstance(names, str):
        for raw in names.split(","):
            name = raw.strip().lstrip("/")
            if name:
                return name
    raw_id = str(entry.get("Id") or entry.get("ID") or "").strip()
    return raw_id[:12] if raw_id else ""


def parse_docker_containers(stage: Path) -> tuple[list[dict[str, str]], list[dict[str, str]]]:
    entries = read_json_list(stage / "docker_containers.json")
    if not entries:
        entries = []
        for line in read_text(stage / "docker_containers.jsonl").splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                data = json.loads(line)
            except json.JSONDecodeError:
                continue
            if isinstance(data, dict):
                entries.append(data)
    containers: list[dict[str, str]] = []
    skipped: list[dict[str, str]] = []
    for entry in entries:
        name = docker_container_name(entry)
        if not name:
            continue
        state = str(entry.get("State") or "").strip().lower()
        item = {
            "name": f"docker-{slug(name)}",
            "display_name": name,
            "container": name,
            "status": state or str(entry.get("Status") or "").strip(),
            "socket": "/run/docker.sock",
        }
        if state == "running":
            containers.append(item)
        else:
            skipped.append(item | {"reason": "container is not running"})
    return containers, skipped


def parse_libvirt_domains(stage: Path) -> tuple[list[dict[str, str]], list[dict[str, str]]]:
    domains: list[dict[str, str]] = []
    skipped: list[dict[str, str]] = []
    for line in read_text(stage / "libvirt_domains.tsv").splitlines():
        fields = line.split("\t")
        if len(fields) < 4:
            continue
        socket, uri, domain, state = (field.strip() for field in fields[:4])
        if not socket or not uri or not domain:
            continue
        item = {
            "name": f"vm-{slug(domain)}",
            "display_name": domain,
            "domain": domain,
            "status": state.lower(),
            "uri": uri,
            "socket": socket,
        }
        if state.lower() == "running":
            domains.append(item)
        else:
            skipped.append(item | {"reason": "domain is not running"})
    return domains, skipped


def has_active_swap(stage: Path) -> bool:
    for line in read_text(stage / "proc_swaps").splitlines():
        line = line.strip()
        if line and not line.startswith("Filename"):
            return True
    return False


def tar_config(config_root: Path, tar_path: Path) -> None:
    def root_owned(info: tarfile.TarInfo) -> tarfile.TarInfo:
        info.uid = 0
        info.gid = 0
        info.uname = "root"
        info.gname = "root"
        return info

    with tarfile.open(tar_path, "w:gz") as tf:
        etc_sermo = config_root / "etc/sermo"
        for path in sorted(etc_sermo.rglob("*")):
            if path.is_file():
                tf.add(path, arcname=str(path.relative_to(config_root)), filter=root_owned)


def generate_for_host(host_slug: str, stage: Path, configs_dir: Path, options: GenerationOptions) -> dict:
    root = configs_dir / host_slug / "root"
    if root.exists():
        shutil.rmtree(root)
    ensure_dirs(root)
    backend = read_text(stage / "init").strip()
    config_backend = backend if backend in {"systemd", "openrc"} else "auto"
    write_file(root, "etc/sermo/sermo.yml", base_config(options, config_backend))

    report = {
        "host": host_slug,
        "init": config_backend,
        "services": {"enabled": [], "skipped": []},
        "containers": {"enabled": [], "skipped": []},
        "virtual_machines": {"enabled": [], "skipped": []},
        "watches": {},
        "filesystems": [],
        "raid_arrays": [],
        "lvm_volumes": [],
        "mount_units": [],
        "nfs_endpoints": [],
        "skipped_watches": [],
        "config_tar": str(configs_dir / host_slug / "sermo-config.tgz"),
    }

    generated_service_names: set[str] = set()
    services, skipped_services = parse_services(stage, options)
    catalog_docs = load_catalog_services(options.catalog_services_dir)
    for svc in services:
        name = svc["name"]
        generated_service_names.add(name)
        variable_overrides, variables_source = service_variable_overrides(stage, name)
        doc, values = values_for_service(name, stage, catalog_docs)
        effective_variables = effective_service_variables(doc or {}, values, variable_overrides)
        disabled_endpoint_watches, endpoint_checks = endpoint_watch_overrides(stage, doc or {}, effective_variables)
        body = f"""name: {name}
uses: {name}
monitor: enabled
dry_run: true
"""
        if variable_overrides:
            body += "variables:\n"
            for key, value in sorted(variable_overrides.items()):
                body += f"  {key}: {yaml_quote(value)}\n"
        process_override = service_process_override(stage, name)
        if process_override:
            body += process_override
        if disabled_endpoint_watches:
            body += "watches:\n"
            for watch_name in sorted(disabled_endpoint_watches):
                body += f"  {watch_name}:\n    enabled: false\n"
        write_file(root, f"etc/sermo/services/{slug(name)}.yml", body)
        enabled = {"name": name, "status": svc.get("status", "")}
        if variable_overrides:
            enabled["variables"] = variable_overrides
            enabled["variables_source"] = variables_source
        if process_override:
            enabled["process_selector_source"] = "cloudflared deleted exe fallback"
        if endpoint_checks:
            enabled["endpoint_checks"] = endpoint_checks
        report["services"]["enabled"].append(enabled)
    report["services"]["skipped"] = skipped_services

    containers, skipped_containers = parse_docker_containers(stage)
    for container in containers:
        name = container["name"]
        if name in generated_service_names:
            skipped = dict(container)
            skipped["reason"] = "generated service name already exists"
            skipped_containers.append(skipped)
            continue
        generated_service_names.add(name)
        write_file(
            root,
            f"etc/sermo/services/{slug(name)}.yml",
            controlled_docker_service(name, container["display_name"], container["container"], container["socket"]),
        )
        report["containers"]["enabled"].append(container)
    report["containers"]["skipped"] = skipped_containers

    virtual_machines, skipped_virtual_machines = parse_libvirt_domains(stage)
    for vm in virtual_machines:
        name = vm["name"]
        if name in generated_service_names:
            skipped = dict(vm)
            skipped["reason"] = "generated service name already exists"
            skipped_virtual_machines.append(skipped)
            continue
        generated_service_names.add(name)
        write_file(
            root,
            f"etc/sermo/services/{slug(name)}.yml",
            controlled_vm_service(name, vm["display_name"], vm["domain"], vm["uri"], vm["socket"]),
        )
        report["virtual_machines"]["enabled"].append(vm)
    report["virtual_machines"]["skipped"] = skipped_virtual_machines

    def add_watch(folder: str, name: str, body: str) -> None:
        write_file(root, f"etc/sermo/{folder}/{slug(name)}.yml", body)
        report["watches"].setdefault(folder, 0)
        report["watches"][folder] += 1

    def skip(kind: str, reason: str) -> None:
        report["skipped_watches"].append({"kind": kind, "reason": reason})

    nfs_routes = parse_nfs_routes(read_text(stage / "nfs_routes"))
    nfs_endpoint_reports: dict[str, dict] = {}

    def add_nfs_endpoint(fstab: dict[str, str]) -> None:
        if fstab["fstype"] not in NFS_FILESYSTEMS:
            return
        host = nfs_server_from_source(fstab["source"])
        if not host:
            skip("nfs_endpoint", f"cannot parse NFS server from {fstab['source']} for {fstab['target']}")
            return
        endpoint = nfs_endpoint_reports.get(host)
        if endpoint:
            if fstab["target"] not in endpoint["paths"]:
                endpoint["paths"].append(fstab["target"])
            return
        endpoint_name = f"nfs-{slug(host)}"
        route = nfs_routes.get(host, {})
        check_lines = [
            "type: nfs",
            f"host: {yaml_quote(host)}",
            f"timeout: {NFS_ENDPOINT_TIMEOUT}",
        ]
        if route.get("interface"):
            check_lines.append(f"interface: {yaml_quote(route['interface'])}")
        add_watch(
            "networks",
            endpoint_name,
            simple_watch(
                endpoint_name,
                "network",
                "1m",
                check_lines,
                cycles=3,
                display_name=f"NFS {host}",
            ),
        )
        endpoint = {"name": endpoint_name, "host": host, "paths": [fstab["target"]]}
        if route.get("address"):
            endpoint["address"] = route["address"]
        if route.get("interface"):
            endpoint["interface"] = route["interface"]
        nfs_endpoint_reports[host] = endpoint
        report["nfs_endpoints"].append(endpoint)

    features = parse_features(stage)
    disks, usb_targets = block_inventory(stage)

    findmnt = read_json(stage / "findmnt.json")
    mounts = flatten_findmnt(findmnt.get("filesystems", []))
    seen_targets: set[str] = set()
    storage_count = 0
    mount_count = 0
    fstab_entries = parse_fstab(read_text(stage / "fstab"))
    fstab_targets = {entry["target"]: entry for entry in fstab_entries}
    mount_watch_targets: set[str] = set()

    def add_mount_watch(fstab: dict[str, str]) -> None:
        nonlocal mount_count
        target = fstab["target"]
        if target in mount_watch_targets or not target.startswith("/"):
            return
        if target != ROOT_MOUNT_TARGET and any(target == prefix or target.startswith(prefix + "/") for prefix in SKIP_MOUNT_PREFIXES):
            return
        name = f"mount-{slug(target)}"
        body = (
            simple_watch(
                name,
                "storage",
                "1m",
                [
                    "type: storage",
                    f"path: {yaml_quote(target)}",
                    "mounted: true",
                ],
                cycles=3,
            )
            + mount_unit_block()
        )
        add_watch("mounts", name, body)
        mount_count += 1
        mount_watch_targets.add(target)
        report["mount_units"].append({
            "name": name,
            "path": target,
            "source": fstab["source"],
            "fstype": fstab["fstype"],
            "folder": "mounts",
        })

    for mount in mounts:
        target = mount.get("target") or ""
        if target in seen_targets or not mount_is_local_storage(mount):
            continue
        seen_targets.add(target)
        name = "storage-root" if target == ROOT_MOUNT_TARGET else f"storage-{slug(target)}"
        body = simple_watch(
            name,
            "storage",
            "1m",
            [
                "type: storage",
                f"path: {yaml_quote(target)}",
                "mounted: true",
                f'free_pct: {{ op: "<", value: "{options.storage_free_pct}" }}',
            ],
            cycles=3,
            then_lines=[f"expand: {{ by: {options.expand_by} }}", "notify: [none]"],
            policy=True,
        )
        fstab = fstab_targets.get(target)
        if fstab and target != ROOT_MOUNT_TARGET:
            body += mount_unit_block()
            mount_count += 1
            mount_watch_targets.add(target)
            report["mount_units"].append({
                "name": name,
                "path": target,
                "source": fstab["source"],
                "fstype": fstab["fstype"],
                "folder": "storages",
            })
        add_watch("storages", name, body)
        report["filesystems"].append({"name": name, "path": target, "fstype": (mount.get("fstype") or "").lower()})
        storage_count += 1

    for mount in mounts:
        target = mount.get("target") or ""
        if target in mount_watch_targets or not target.startswith("/"):
            continue
        if target != ROOT_MOUNT_TARGET and any(target == prefix or target.startswith(prefix + "/") for prefix in SKIP_MOUNT_PREFIXES):
            continue
        fstab = fstab_targets.get(target)
        fstype = (mount.get("fstype") or "").lower()
        if not fstab or (fstype not in NETWORK_FS and fstab["fstype"] not in NETWORK_FS and target not in usb_targets):
            continue
        add_mount_watch(fstab)

        add_nfs_endpoint(fstab)

    for fstab in fstab_entries:
        if fstab["fstype"] in NETWORK_FS:
            add_mount_watch(fstab)
        add_nfs_endpoint(fstab)

    if storage_count == 0:
        skip("storage", "no local mounted storage filesystem discovered")
    if mount_count == 0:
        skip("mount", "no mounted fstab-backed target discovered")

    generic_watches = [
        ("watch-load", "system", "30s", ["type: load", "per_cpu: true", 'load5: { op: ">", value: 2 }']),
        ("watch-memory", "system", "30s", ["type: memory", 'available_pct: { op: "<", value: "10%" }']),
        ("watch-fds", "system", "30s", ["type: fds", 'used_pct: { op: ">=", value: "80%" }']),
        ("watch-pids", "system", "30s", ["type: pids", 'used_pct: { op: ">=", value: "80%" }']),
        ("watch-entropy", "system", "30s", ["type: entropy", 'avail: { op: "<", value: 256 }']),
        ("watch-zombies", "system", "30s", ["type: zombies", 'count: { op: ">", value: 0 }']),
        ("watch-oom", "system", "30s", ["type: oom"]),
    ]
    for name, category, interval, check_lines in generic_watches:
        add_watch("watches", name, simple_watch(name, category, interval, check_lines))

    add_watch(
        "watches",
        "watch-clock-drift",
        simple_watch(
            "watch-clock-drift",
            "system",
            "5m",
            [
                "type: clock",
                "servers:",
                "  - time.cloudflare.com",
                "  - pool.ntp.org",
                "max_offset: 3s",
                "max_stratum: 4",
                "max_root_dispersion: 250ms",
                "timeout: 3s",
            ],
            cycles=2,
        ),
    )

    if features.get("pressure") == "1":
        for resource in ["cpu", "memory", "io"]:
            name = f"watch-pressure-{resource}"
            add_watch(
                "watches",
                name,
                simple_watch(name, "system", "30s", ["type: pressure", f"resource: {resource}", 'some_avg60: { op: ">", value: 20 }']),
            )
    else:
        skip("pressure", "/proc/pressure is not available")

    if has_active_swap(stage):
        add_watch(
            "watches",
            "watch-swap",
            metric_watch(
                "watch-swap",
                "system",
                "30s",
                ["type: swap"],
                [
                    ("usage", ['used_pct: { op: ">", value: 80 }', "for: { cycles: 10 }", "then: { notify: [none] }"]),
                    ("io", ['delta: { op: ">", value: 1000 }', "for: { cycles: 10 }", "then: { notify: [none] }"]),
                ],
            ),
        )
    else:
        skip("swap", "no active swap device")

    if features.get("conntrack") == "1":
        add_watch("watches", "watch-conntrack", simple_watch("watch-conntrack", "network", "30s", ["type: conntrack", 'used_pct: { op: ">=", value: "80%" }']))
    else:
        skip("conntrack", "nf_conntrack counters not exposed")

    if features.get("nft") == "1" or features.get("iptables") == "1":
        add_watch("watches", "watch-firewall-rules", simple_watch("watch-firewall-rules", "network", "1m", ["type: firewall_rules", "backend: auto", "min_rules: 1"], cycles=3))
    else:
        skip("firewall_rules", "nft/iptables not installed")

    if " autofs " in read_text(stage / "proc_mounts"):
        add_watch("watches", "watch-autofs", simple_watch("watch-autofs", "storage", "1m", ["type: autofs"], cycles=3))
    else:
        skip("autofs", "no autofs mountpoint discovered")

    raid_arrays = parse_md_arrays(stage)
    if raid_arrays:
        for array in raid_arrays:
            name = f"raid-{slug(array)}"
            add_watch(
                "watches",
                name,
                simple_watch(
                    name,
                    "storage",
                    "1m",
                    ["type: raid", f"array: {yaml_quote(array)}", "sysfs_changes: true"],
                    cycles=0,
                ),
            )
        report["raid_arrays"] = raid_arrays
    else:
        skip("raid", "no md raid array discovered")

    skip("lvm", "LVM space watches disabled by configuration")

    if features.get("edac") == "1":
        add_watch("watches", "watch-edac", simple_watch("watch-edac", "hardware", "1m", ["type: edac", 'ce: { op: ">", value: 100 }'], cycles=3))
    else:
        skip("edac", "EDAC controllers not exposed")

    if read_text(stage / "hwmon_temp_inputs").strip():
        add_watch("watches", "watch-sensors", simple_watch("watch-sensors", "hardware", "1m", ["type: sensors", 'temp: { op: ">", value: 85 }'], cycles=3))
    else:
        skip("sensors", "no hwmon temperature input discovered")

    for disk in disks:
        disk_name = disk["name"]
        disk_path = disk["path"]
        add_watch(
            "watches",
            f"diskio-{disk_name}",
            simple_watch(f"diskio-{disk_name}", "storage", "30s", ["type: diskio", f"device: {yaml_quote(disk_name)}", 'util_pct: { op: ">=", value: "80%" }']),
        )
        if features.get("smartctl") == "1":
            if disk_name.startswith(SMARTLESS_DISK_PREFIXES):
                skip(f"smart-{slug(disk_name)}", "network block device without SMART data")
            else:
                add_watch(
                    "watches",
                    f"smart-{slug(disk_name)}",
                    simple_watch(f"smart-{slug(disk_name)}", "storage", options.smart_interval, ["type: smart", f"device: {yaml_quote(disk_path)}"], cycles=1),
                )
        if features.get("hdparm") == "1" and (disk_path.startswith(("/dev/sd", "/dev/hd")) or disk.get("tran") in {"ata", "sata", "scsi", "usb"}):
            add_watch(
                "watches",
                f"hdparm-{slug(disk_name)}",
                simple_watch(
                    f"hdparm-{slug(disk_name)}",
                    "storage",
                    options.hdparm_interval,
                    ["type: hdparm", f"device: {yaml_quote(disk_path)}", "timeout: 30s", 'read: { op: "<", value: 100 }'],
                    cycles=2,
                ),
            )
    if not disks:
        skip("diskio/smart/hdparm", "no writable whole-disk block device discovered")
    if disks and features.get("smartctl") != "1":
        skip("smart", "smartctl not installed")
    if disks and features.get("hdparm") != "1":
        skip("hdparm", "hdparm not installed")

    interfaces = parse_interfaces(stage)
    addressed_interfaces = interfaces_with_addresses(stage)
    for iface in interfaces:
        safe = slug(iface)
        # Net/ICMP metric expectations are alert conditions: use the unhealthy
        # value so healthy links and assigned addresses do not fire continuously.
        add_watch(
            "networks",
            f"net-{safe}",
            metric_watch(
                f"net-{safe}",
                "network",
                "30s",
                ["type: net", f"interface: {yaml_quote(iface)}"],
                [
                    ("state", ["expect: down", "for: { cycles: 3 }", "then: { notify: [none] }"]),
                    ("errors", ['delta: { op: ">", value: 100 }', "for: { cycles: 3 }", "then: { notify: [none] }"]),
                ] + (
                    [("address", ["expect: absent", "for: { cycles: 3 }", "then: { notify: [none] }"])]
                    if iface in addressed_interfaces
                    else []
                ) + [("speed", ["on: change", "then: { notify: [none] }"])],
            ),
        )
    if not interfaces:
        skip("net", "no non-loopback UP interface discovered")

    routes = parse_default_routes(stage)
    for route in routes:
        safe = slug(f"{route['family']}-{route['iface']}")
        add_watch("networks", f"route-{safe}", simple_watch(f"route-{safe}", "network", "30s", ["type: route", f"family: {route['family']}", f"interface: {yaml_quote(route['iface'])}"], cycles=3))
        if route["family"] == "ipv4" and route.get("via"):
            add_watch(
                "networks",
                f"icmp-gw-{slug(route['iface'])}",
                metric_watch(
                    f"icmp-gw-{slug(route['iface'])}",
                    "network",
                    "30s",
                    ["type: icmp", f"host: {yaml_quote(route['via'])}", "count: 3"],
                    [
                        ("state", ["expect: down", "for: { cycles: 3 }", "then: { notify: [none] }"]),
                        ("latency", ['threshold: { op: ">", value: 100 }', "for: { cycles: 3 }", "then: { notify: [none] }"]),
                    ],
                ),
            )
    if not routes:
        skip("route/icmp", "no default route discovered")

    certs = [line.strip() for line in read_text(stage / "certs").splitlines() if line.strip()]
    for path in certs:
        name = f"cert-{slug(Path(path).name)}"
        add_watch("watches", name, simple_watch(name, "security", "6h", ["type: cert", f"path: {yaml_quote(path)}", "expires_in_days: 14", "on_algorithm_change: true"], cycles=1))
    if not certs:
        skip("cert", "no immediate certificate file under /etc/ssl")

    geoip_directory = read_text(stage / "geoip_directory").strip()
    if geoip_directory == GEOIP_DATABASE_DIRECTORY:
        add_watch(
            "watches",
            "geoip-database-freshness",
            simple_watch(
                "geoip-database-freshness",
                "network",
                "6h",
                [
                    "type: file",
                    "paths:",
                    f"  - {yaml_quote(GEOIP_DATABASE_DIRECTORY)}",
                    "recursive: true",
                    f"older_than: {GEOIP_DATABASE_OLDER_THAN}",
                    'summary: "GeoIP ${value} is older than ${older_than} in ${number_files} files"',
                ],
                cycles=0,
            ),
        )
    else:
        skip("geoip", f"{GEOIP_DATABASE_DIRECTORY} is not present")

    tar_path = configs_dir / host_slug / "sermo-config.tgz"
    tar_config(root, tar_path)
    return report


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--stage-root", required=True, help="Directory containing <host>/out inventory directories.")
    parser.add_argument("--configs-root", required=True, help="Output directory for generated config trees and tarballs.")
    parser.add_argument("--report", required=True, help="JSON report path to write.")
    parser.add_argument("--web-port", type=int, default=9797)
    parser.add_argument("--web-password", default="sermo-remote-admin")
    parser.add_argument("--storage-free-pct", default="5%")
    parser.add_argument("--expand-by", default="5G")
    parser.add_argument("--smart-interval", default="24h")
    parser.add_argument("--hdparm-interval", default="6h")
    parser.add_argument(
        "--catalog-services-dir",
        default=str(Path(__file__).resolve().parents[2] / "catalog/services"),
        help="Catalog services directory used to map catalog profiles to active init units.",
    )
    parser.add_argument(
        "--include-inactive-installed-services",
        action="store_true",
        help="Generate every installed catalog service, even when its init unit is not active.",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    stage_root = Path(args.stage_root)
    configs_root = Path(args.configs_root)
    configs_root.mkdir(parents=True, exist_ok=True)
    options = GenerationOptions(
        web_port=args.web_port,
        web_password=args.web_password,
        storage_free_pct=args.storage_free_pct,
        expand_by=args.expand_by,
        smart_interval=args.smart_interval,
        hdparm_interval=args.hdparm_interval,
        active_services_only=not args.include_inactive_installed_services,
        catalog_services_dir=Path(args.catalog_services_dir),
    )

    reports = []
    for host_dir in sorted(path for path in stage_root.iterdir() if path.is_dir()):
        stage = host_dir / "out"
        if not stage.is_dir():
            continue
        reports.append(generate_for_host(host_dir.name, stage, configs_root, options))

    report = {"hosts": reports}
    Path(args.report).write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"hosts": len(reports), "report": args.report}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
