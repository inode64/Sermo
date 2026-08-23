#!/usr/bin/env python3
"""Deploy per-customer Sermo dashboard credentials from a CSV inventory."""

from __future__ import annotations

import argparse
import csv
import io
import ipaddress
import subprocess
import sys
import tempfile
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from pathlib import Path

CONNECT_TIMEOUT_SECONDS = 10
COMMAND_TIMEOUT_SECONDS = 30
WEB_CONFIG_COMMAND_TIMEOUT_SECONDS = 120
DEFAULT_JOBS = 8
DEFAULT_SSH_USER = "root"
CREDENTIALS_TARGET = "/etc/sermo/credentials.env"
SERMO_CONFIG_TARGET = "/etc/sermo/sermo.yml"
REMOTE_STAGE_PREFIX = f"{tempfile.gettempdir()}/sermo-credentials-"
SPECIAL_OPTIZA_CLIENTS = frozenset({"amizalsa", "bertolin", "euromeca", "maberauto", "realexport"})
REQUIRED_CREDENTIALS = ("inode64", "optiza")
SSH_OPTIONS = (
    "-o",
    "BatchMode=yes",
    "-o",
    f"ConnectTimeout={CONNECT_TIMEOUT_SECONDS}",
    "-o",
    "ServerAliveInterval=5",
    "-o",
    "ServerAliveCountMax=2",
    "-o",
    "LogLevel=ERROR",
)


@dataclass(frozen=True)
class Host:
    """One customer server resolved from the CSV inventory."""

    client: str
    ip: str


@dataclass(frozen=True)
class Result:
    """A deployment outcome that never includes secret material."""

    host: Host
    phase: str
    status: str
    detail: str


def parse_passwords(path: Path) -> dict[str, str]:
    """Read ``cliente contraseña`` entries while rejecting ambiguous secrets."""
    passwords: dict[str, str] = {}
    for line_number, raw_line in enumerate(path.read_text(encoding="utf-8").splitlines(), start=1):
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        fields = line.split(None, 1)
        if len(fields) != 2:
            raise ValueError(f"{path}:{line_number}: expected client and password")
        client, password = fields
        if not client.replace("-", "").replace("_", "").isalnum():
            raise ValueError(f"{path}:{line_number}: invalid client name")
        if password != password.strip() or password.startswith(("#", "$")):
            raise ValueError(f"{path}:{line_number}: password cannot be represented safely")
        if client in passwords:
            raise ValueError(f"{path}:{line_number}: duplicate client password for {client}")
        passwords[client] = password
    return passwords


def normalize_ip(raw_ip: str, row_number: int) -> str:
    """Return IPv4 after removing the inventory's quote punctuation."""
    candidate = raw_ip.strip(" \t\r\n,'\"")
    try:
        address = ipaddress.IPv4Address(candidate)
    except ipaddress.AddressValueError as error:
        raise ValueError(f"inventory row {row_number}: invalid ip_vpn") from error
    return str(address)


def parse_inventory(path: Path) -> list[Host]:
    """Read unique client/IP pairs from the authoritative CSV inventory."""
    try:
        content = path.read_text(encoding="utf-8-sig")
    except UnicodeDecodeError:
        content = path.read_text(encoding="latin-1")
    reader = csv.DictReader(io.StringIO(content, newline=""))
    required_columns = {"cliente", "ip_vpn"}
    if reader.fieldnames is None or not required_columns.issubset(reader.fieldnames):
        raise ValueError("inventory must contain cliente and ip_vpn columns")
    hosts_by_ip: dict[str, Host] = {}
    for row_number, row in enumerate(reader, start=2):
        client = (row.get("cliente") or "").strip()
        raw_ip = row.get("ip_vpn") or ""
        if not client:
            raise ValueError(f"inventory row {row_number}: empty cliente")
        ip = normalize_ip(raw_ip, row_number)
        host = Host(client=client, ip=ip)
        existing = hosts_by_ip.get(ip)
        if existing is not None and existing != host:
            raise ValueError(f"inventory assigns {ip} to more than one client")
        hosts_by_ip[ip] = host
    return list(hosts_by_ip.values())


def credential_clients(client: str) -> tuple[str, ...]:
    """Return the required credential labels for one customer's server."""
    labels = [client, REQUIRED_CREDENTIALS[0]]
    if client in SPECIAL_OPTIZA_CLIENTS:
        labels.append(REQUIRED_CREDENTIALS[1])
    return tuple(dict.fromkeys(labels))


def validate_credentials(hosts: list[Host], passwords: dict[str, str]) -> None:
    """Ensure every needed secret exists before any remote mutation."""
    required = {label for host in hosts for label in credential_clients(host.client)}
    missing = sorted(required - passwords.keys())
    if missing:
        raise ValueError(f"password file lacks required client entries: {', '.join(missing)}")


def select_hosts(hosts: list[Host], requested_ips: list[str], excluded_ips: list[str] | None = None) -> list[Host]:
    """Restrict a deployment to explicitly requested and excluded inventory IPs."""
    try:
        wanted = {str(ipaddress.IPv4Address(ip)) for ip in requested_ips}
        excluded = {str(ipaddress.IPv4Address(ip)) for ip in excluded_ips or []}
    except ipaddress.AddressValueError as error:
        raise ValueError("--ip-vpn must be an IPv4 address") from error
    available = {host.ip for host in hosts}
    missing = sorted((wanted | excluded) - available)
    if missing:
        raise ValueError(f"requested IPs are absent from the inventory: {', '.join(missing)}")
    if wanted & excluded:
        raise ValueError("an IP cannot be both selected and excluded")
    if not wanted:
        wanted = available
    return [host for host in hosts if host.ip in wanted - excluded]


def run(
    command: list[str],
    *,
    input_text: str | None = None,
    timeout_seconds: int = COMMAND_TIMEOUT_SECONDS,
) -> subprocess.CompletedProcess[str]:
    """Run a bounded command without a shell or any credential arguments."""
    return subprocess.run(
        command,
        input=input_text,
        text=True,
        capture_output=True,
        check=False,
        timeout=timeout_seconds,
    )


def ssh_command(host: Host, remote_command: str) -> list[str]:
    """Build the root SSH command for one validated inventory address."""
    return ["ssh", *SSH_OPTIONS, f"{DEFAULT_SSH_USER}@{host.ip}", remote_command]


def detail(process: subprocess.CompletedProcess[str]) -> str:
    """Return short diagnostics, which are designed not to contain secrets."""
    output = " ".join((process.stderr or process.stdout).split())
    return output[:240] or f"exit {process.returncode}"


def success_detail(process: subprocess.CompletedProcess[str]) -> str:
    """Return one report-safe success line without command output formatting."""
    return " ".join(process.stdout.split())[:240] or "ok"


def preflight(host: Host, configure_web: bool) -> Result:
    """Confirm root access and a safe credentials target without changing it."""
    remote_command = (
        "set -eu; "
        '[ "$(id -u)" = "0" ]; '
        "if [ -e /etc/sermo ] && [ ! -d /etc/sermo ]; then exit 12; fi; "
        f"if [ -L {CREDENTIALS_TARGET} ]; then exit 13; fi; "
        f"if [ -e {CREDENTIALS_TARGET} ] && [ ! -f {CREDENTIALS_TARGET} ]; then exit 14; fi; "
        "printf preflight-ok"
    )
    if configure_web:
        remote_command = (
            remote_command.removesuffix("printf preflight-ok")
            + f"if [ ! -f {SERMO_CONFIG_TARGET} ] || [ -L {SERMO_CONFIG_TARGET} ]; then exit 15; fi; "
            + "printf preflight-ok"
        )
    try:
        process = run(ssh_command(host, remote_command))
    except subprocess.TimeoutExpired:
        return Result(host, "preflight", "failed", "SSH command timed out")
    if process.returncode != 0:
        return Result(host, "preflight", "failed", detail(process))
    return Result(host, "preflight", "ok", "root access and target verified")


def create_stage(host: Host) -> str:
    """Create a root-only remote staging directory and return its exact path."""
    process = run(ssh_command(host, f"umask 077; mktemp -d {REMOTE_STAGE_PREFIX}XXXXXXXX"))
    if process.returncode != 0:
        raise RuntimeError(detail(process))
    stage = process.stdout.strip()
    if not stage.startswith(REMOTE_STAGE_PREFIX) or len(stage) <= len(REMOTE_STAGE_PREFIX):
        raise RuntimeError("remote staging path was invalid")
    return stage


def cleanup_stage(host: Host, stage: str) -> None:
    """Remove only this run's uploaded secret and helper from the remote host."""
    if not stage.startswith(REMOTE_STAGE_PREFIX):
        return
    remote_command = (
        f"rm -f -- {stage}/credentials.new {stage}/{host.ip}.credentials "
        f"{stage}/remote_apply_credentials.sh {stage}/remote_configure_web_credentials.sh "
        f"{stage}/sermo.yml.before {stage}/protected-paths.before {stage}/protected-paths.after; "
        f"rmdir -- {stage} 2>/dev/null || true"
    )
    try:
        run(ssh_command(host, remote_command))
    except subprocess.TimeoutExpired:
        return


def cleanup_orphaned_stage(host: Host) -> Result:
    """Remove a failed old upload only when it belongs exactly to this host."""
    remote_command = (
        "set -eu; "
        f"for stage in {REMOTE_STAGE_PREFIX}*; do "
        '[ -d "$stage" ] || continue; '
        f'legacy="$stage/{host.ip}.credentials"; '
        '[ -f "$legacy" ] && [ ! -L "$legacy" ] || continue; '
        'rm -f -- "$legacy" "$stage/remote_apply_credentials.sh"; '
        'rmdir -- "$stage" 2>/dev/null || true; '
        "done; printf cleanup-ok"
    )
    try:
        process = run(ssh_command(host, remote_command))
    except subprocess.TimeoutExpired:
        return Result(host, "cleanup", "failed", "SSH command timed out")
    if process.returncode != 0:
        return Result(host, "cleanup", "failed", detail(process))
    return Result(host, "cleanup", "ok", "orphaned staging checked")


def apply(host: Host, values: list[str], helper: Path, temporary_root: Path) -> Result:
    """Upload and atomically merge the required credentials for one host."""
    stage = ""
    artifact = temporary_root / f"{host.ip}.credentials"
    try:
        artifact.write_text("\n".join(values) + "\n", encoding="utf-8")
        artifact.chmod(0o600)
        stage = create_stage(host)
        credential_upload = run(
            [
                "scp",
                *SSH_OPTIONS,
                str(artifact),
                f"{DEFAULT_SSH_USER}@{host.ip}:{stage}/credentials.new",
            ],
        )
        if credential_upload.returncode != 0:
            return Result(host, "upload", "failed", detail(credential_upload))
        helper_upload = run(
            [
                "scp",
                *SSH_OPTIONS,
                str(helper),
                f"{DEFAULT_SSH_USER}@{host.ip}:{stage}/remote_apply_credentials.sh",
            ],
        )
        if helper_upload.returncode != 0:
            return Result(host, "upload", "failed", detail(helper_upload))
        remote_helper = f"{stage}/remote_apply_credentials.sh"
        remote_source = f"{stage}/credentials.new"
        process = run(ssh_command(host, f"bash {remote_helper} {remote_source}"))
        if process.returncode != 0:
            return Result(host, "apply", "failed", detail(process))
        return Result(host, "apply", "ok", success_detail(process))
    except (OSError, RuntimeError, subprocess.TimeoutExpired) as error:
        return Result(host, "apply", "failed", str(error)[:240])
    finally:
        artifact.unlink(missing_ok=True)
        if stage:
            cleanup_stage(host, stage)


def configure_web(host: Host, helper: Path) -> Result:
    """Point a host's dashboard configuration at its protected credential file."""
    stage = ""
    try:
        stage = create_stage(host)
        upload = run(
            [
                "scp",
                *SSH_OPTIONS,
                str(helper),
                f"{DEFAULT_SSH_USER}@{host.ip}:{stage}/remote_configure_web_credentials.sh",
            ],
        )
        if upload.returncode != 0:
            return Result(host, "configure", "failed", detail(upload))
        remote_helper = f"{stage}/remote_configure_web_credentials.sh"
        process = run(
            ssh_command(host, f"bash {remote_helper} {stage}"),
            timeout_seconds=WEB_CONFIG_COMMAND_TIMEOUT_SECONDS,
        )
        if process.returncode != 0:
            return Result(host, "configure", "failed", detail(process))
        return Result(host, "configure", "ok", success_detail(process))
    except (OSError, RuntimeError, subprocess.TimeoutExpired) as error:
        return Result(host, "configure", "failed", str(error)[:240])
    finally:
        if stage:
            cleanup_stage(host, stage)


def write_report(results: list[Result], run_root: Path) -> Path:
    """Write a secret-free report for the fleet deployment."""
    report = run_root / "report.tsv"
    rows = ["ip_vpn\tcliente\tfase\testado\tdetalle"]
    rows.extend(
        f"{item.host.ip}\t{item.host.client}\t{item.phase}\t{item.status}\t{item.detail}"
        for item in results
    )
    report.write_text("\n".join(rows) + "\n", encoding="utf-8")
    report.chmod(0o600)
    return report


def arguments() -> argparse.Namespace:
    """Parse the explicit inventory and password-source paths."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", type=Path, required=True, help="CSV with cliente and ip_vpn")
    parser.add_argument("--passwords", type=Path, required=True, help="cliente contraseña source")
    parser.add_argument("--ip-vpn", action="append", default=[], help="limit deployment to one inventory IPv4")
    parser.add_argument("--exclude-ip-vpn", action="append", default=[], help="exclude one inventory IPv4")
    parser.add_argument("--run-root", type=Path, help="secret-free local report directory")
    parser.add_argument("--jobs", type=int, default=DEFAULT_JOBS, help=f"parallel hosts (default: {DEFAULT_JOBS})")
    parser.add_argument("--cleanup-orphaned-stages", action="store_true", help="remove only failed old staged uploads")
    parser.add_argument("--configure-web", action="store_true", help="configure web.password_file and restart active sermod")
    parser.add_argument("--dry-run", action="store_true", help="validate sources without connecting")
    return parser.parse_args()


def main() -> int:
    """Validate mappings, then preflight and deploy to every inventory host."""
    args = arguments()
    if args.jobs < 1:
        raise ValueError("--jobs must be positive")
    helper_name = "remote_configure_web_credentials.sh" if args.configure_web else "remote_apply_credentials.sh"
    helper = Path(__file__).with_name(helper_name)
    if not helper.is_file():
        raise ValueError(f"missing remote helper: {helper}")
    hosts = parse_inventory(args.inventory)
    hosts = select_hosts(hosts, args.ip_vpn, args.exclude_ip_vpn)
    passwords = parse_passwords(args.passwords)
    validate_credentials(hosts, passwords)
    if args.dry_run:
        for host in hosts:
            labels = ",".join(credential_clients(host.client))
            print(f"{host.ip}\t{host.client}\t{labels}")
        return 0

    if args.run_root is None:
        run_root = Path(tempfile.mkdtemp(prefix="sermo-credentials-"))
    else:
        run_root = args.run_root
        run_root.mkdir(mode=0o700, parents=True, exist_ok=True)
        run_root.chmod(0o700)
    results: list[Result] = []
    with tempfile.TemporaryDirectory(prefix="sermo-credentials-private-") as temporary_name:
        temporary_root = Path(temporary_name)
        temporary_root.chmod(0o700)
        with ThreadPoolExecutor(max_workers=args.jobs) as executor:
            futures = {executor.submit(preflight, host, args.configure_web): host for host in hosts}
            for future in as_completed(futures):
                result = future.result()
                results.append(result)
                print(f"{result.host.ip}\t{result.host.client}\t{result.phase}\t{result.status}")
        ready_hosts = [item.host for item in results if item.phase == "preflight" and item.status == "ok"]
        with ThreadPoolExecutor(max_workers=args.jobs) as executor:
            futures = {executor.submit(cleanup_orphaned_stage, host): host for host in ready_hosts}
            for future in as_completed(futures):
                result = future.result()
                results.append(result)
                print(f"{result.host.ip}\t{result.host.client}\t{result.phase}\t{result.status}")
        if args.cleanup_orphaned_stages:
            report = write_report(sorted(results, key=lambda item: (item.host.ip, item.phase)), run_root)
            failed = [item for item in results if item.status != "ok"]
            print(f"report={report}")
            return 1 if failed else 0
        ready_hosts = [item.host for item in results if item.phase == "cleanup" and item.status == "ok"]
        with ThreadPoolExecutor(max_workers=args.jobs) as executor:
            if args.configure_web:
                futures = {executor.submit(configure_web, host, helper): host for host in ready_hosts}
            else:
                futures = {
                    executor.submit(
                        apply,
                        host,
                        [passwords[label] for label in credential_clients(host.client)],
                        helper,
                        temporary_root,
                    ): host
                    for host in ready_hosts
                }
            for future in as_completed(futures):
                result = future.result()
                results.append(result)
                print(f"{result.host.ip}\t{result.host.client}\t{result.phase}\t{result.status}")
    report = write_report(sorted(results, key=lambda item: (item.host.ip, item.phase)), run_root)
    failed = [item for item in results if item.status != "ok"]
    print(f"report={report}")
    return 1 if failed else 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError) as error:
        print(f"deploy_credentials: {error}", file=sys.stderr)
        raise SystemExit(64) from error
