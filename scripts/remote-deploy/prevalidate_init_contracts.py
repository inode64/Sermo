#!/usr/bin/env python3
"""Read-only fleet validation of Sermo and init lifecycle contracts."""

from __future__ import annotations

import argparse
import re
import subprocess
import sys
import tempfile
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path

DEFAULT_ENV = Path(".env.ssh")
DEFAULT_JOBS = 8
DEFAULT_SSH_USER = "root"
DEFAULT_CONNECT_TIMEOUT_SECONDS = 8
DEFAULT_COMMAND_TIMEOUT_SECONDS = 90
MAX_DIAGNOSTIC_LENGTH = 160
HOST_PATTERN = re.compile(r"^[A-Za-z0-9._:-]+$")

REMOTE_PROBE = r"""set -u
export LC_ALL=C
config=/etc/sermo/sermo.yml
sermoctl=/usr/bin/sermoctl
if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
	init=systemd
elif command -v rc-service >/dev/null 2>&1; then
	init=openrc
else
	printf 'SUMMARY\tunsupported\tunknown\tinvalid\t0\n'
	exit 0
fi

daemon=inactive
case "$init" in
	systemd) daemon=$(systemctl is-active sermod 2>/dev/null || true) ;;
	openrc)
		if rc-service sermod status >/dev/null 2>&1; then daemon=active; fi
		if rc-status --crashed 2>/dev/null | grep -Eq '(^|[[:space:]])sermod([[:space:]]|$)'; then daemon=failed; fi
		;;
esac

config_state=invalid
if [ -x "$sermoctl" ] && [ -r "$config" ] && timeout 30s env SERMO_BACKEND="$init" SERMO_INIT="$init" "$sermoctl" --config "$config" config validate >/dev/null 2>&1; then
	config_state=valid
fi

service_count=0
if [ -d /etc/sermo/services ]; then
	find /etc/sermo/services -type f \( -name '*.yml' -o -name '*.yaml' \) -print 2>/dev/null | sort | while IFS= read -r file; do
		name=${file##*/}
		name=${name%.yml}
		name=${name%.yaml}
		configured_name=$(sed -n 's/^name:[[:space:]]*//p' "$file" | head -n 1 | tr -d "\"'")
		[ -n "$configured_name" ] && name=$configured_name
		json=$(timeout 20s env SERMO_BACKEND="$init" SERMO_INIT="$init" "$sermoctl" --config "$config" --json status "$name" 2>/dev/null || true)
		backend=$(printf '%s' "$json" | sed -n 's/.*"backend":"\([^"]*\)".*/\1/p')
		unit=$(printf '%s' "$json" | sed -n 's/.*"unit":"\([^"]*\)".*/\1/p')
		cli=$(printf '%s' "$json" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')
		[ -n "$backend" ] || backend=$init
		[ -n "$unit" ] || unit=$name
		[ -n "$cli" ] || cli=error
		direct=unknown
		loaded=unknown
		reload=unknown
		resident=unknown
		pidfile=unknown
		if [ "$backend" != "$init" ]; then
			direct=$cli
			loaded=external
			reload=external
			resident=external
			pidfile=external
		else case "$init" in
			systemd)
				loaded=$(systemctl show "$unit" --property=LoadState --value 2>/dev/null || true)
				direct=$(systemctl show "$unit" --property=ActiveState --value 2>/dev/null || true)
				reload=$(systemctl show "$unit" --property=CanReload --value 2>/dev/null || true)
				main_pid=$(systemctl show "$unit" --property=MainPID --value 2>/dev/null || true)
				pid_path=$(systemctl show "$unit" --property=PIDFile --value 2>/dev/null || true)
				resident=no
				case "$main_pid" in ''|0) ;; *) resident=yes ;; esac
				pidfile=no
				[ -n "$pid_path" ] && pidfile=yes
				;;
			openrc)
				if [ -x "/etc/init.d/$unit" ]; then loaded=loaded; else loaded=not-found; fi
				rc_output=$(rc-service "$unit" status 2>&1 || true)
				rc_state=$(printf '%s' "$rc_output" | tr '[:upper:]' '[:lower:]')
				case "$rc_state" in
					*crashed*) direct=failed ;;
					*stopped*|*"not started"*) direct=inactive ;;
					*started*) direct=active ;;
					*)
						if rc-status --crashed 2>/dev/null | grep -Eq "(^|[[:space:]])${unit}([[:space:]]|$)"; then
							direct=failed
						elif rc-status -a 2>/dev/null | grep -E "^[[:space:]]*${unit}[[:space:]]" | grep -Eq '\[[[:space:]]*started[[:space:]]*\]'; then
							direct=active
						else
							direct=inactive
						fi
						;;
				esac
				reload=no
				grep -Eq '^[[:space:]]*reload\(\)' "/etc/init.d/$unit" 2>/dev/null && reload=yes
				resident=unknown
				pidfile=no
				grep -Eq '(^|[^[:alnum:]_])pidfile=' "/etc/init.d/$unit" "/etc/conf.d/$unit" 2>/dev/null && pidfile=yes
				;;
		esac
		fi
		printf 'SERVICE\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$name" "$backend" "$unit" "$cli" "$direct" "$loaded" "$reload" "$resident" "$pidfile"
	done
	service_count=$(find /etc/sermo/services -type f \( -name '*.yml' -o -name '*.yaml' \) -print 2>/dev/null | wc -l | tr -d ' ')
fi
printf 'SUMMARY\t%s\t%s\t%s\t%s\n' "$init" "$daemon" "$config_state" "$service_count"
"""


@dataclass(frozen=True)
class ServiceContract:
    """Sanitized lifecycle evidence for one configured service."""

    name: str
    backend: str
    unit: str
    cli_status: str
    direct_status: str
    loaded: str
    can_reload: str
    resident: str
    pidfile: str


@dataclass(frozen=True)
class HostResult:
    """One host's read-only prevalidation outcome."""

    host: str
    status: str
    init: str
    daemon: str
    config: str
    configured: int
    checked: int
    mismatches: tuple[str, ...]


def load_hosts(path: Path) -> list[str]:
    """Load only host keys from .env.ssh, never its values."""
    hosts: list[str] = []
    seen: set[str] = set()
    for line_number, raw_line in enumerate(
        path.read_text(encoding="utf-8").splitlines(), start=1
    ):
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        if "=" in line:
            host, _sensitive_value = line.split("=", 1)
        else:
            fields = line.split(maxsplit=1)
            host = fields[0]
        host = host.strip()
        if not HOST_PATTERN.fullmatch(host):
            raise ValueError(f"{path}:{line_number}: invalid host entry")
        if host not in seen:
            hosts.append(host)
            seen.add(host)
    if not hosts:
        raise ValueError(f"{path}: no hosts configured")
    return hosts


def parse_remote_output(host: str, output: str) -> HostResult:
    """Parse the fixed, secret-free remote probe protocol."""
    init = daemon = config = "unknown"
    configured = 0
    services: list[ServiceContract] = []
    malformed = False
    for line in output.splitlines():
        fields = line.split("\t")
        if fields[0] == "SUMMARY" and len(fields) == 5:
            init, daemon, config = fields[1:4]
            try:
                configured = int(fields[4])
            except ValueError:
                malformed = True
        elif fields[0] == "SERVICE" and len(fields) == 10:
            services.append(ServiceContract(*fields[1:]))
        elif line:
            malformed = True

    mismatches: list[str] = []
    if malformed or init == "unknown":
        mismatches.append("invalid probe output")
    if init not in {"systemd", "openrc"}:
        mismatches.append(f"unsupported init {init}")
    if daemon != "active":
        mismatches.append(f"sermod {daemon}")
    if config != "valid":
        mismatches.append(f"config {config}")
    if configured != len(services):
        mismatches.append(f"configured {configured}, checked {len(services)}")
    for service in services:
        if service.cli_status == "error":
            mismatches.append(f"{service.name}: Sermo status error")
        if service.cli_status != service.direct_status:
            mismatches.append(
                f"{service.name}: Sermo={service.cli_status} init={service.direct_status}"
            )
        if service.loaded not in {"loaded", "masked", "external"}:
            mismatches.append(f"{service.name}: unit {service.loaded}")
    status = "ok" if not mismatches else "mismatch"
    return HostResult(
        host, status, init, daemon, config, configured, len(services), tuple(mismatches)
    )


def ssh_command(host: str, user: str, connect_timeout: int) -> list[str]:
    """Build a bounded, non-interactive SSH command for the read-only probe."""
    return [
        "ssh",
        "-o",
        "BatchMode=yes",
        "-o",
        f"ConnectTimeout={connect_timeout}",
        "-o",
        "ConnectionAttempts=1",
        "-o",
        "ServerAliveInterval=5",
        "-o",
        "ServerAliveCountMax=2",
        "-o",
        "LogLevel=ERROR",
        f"{user}@{host}",
        "sh -s",
    ]


def validate_host(
    host: str, user: str, connect_timeout: int, command_timeout: int
) -> HostResult:
    """Run the read-only probe on one host and return sanitized evidence."""
    try:
        process = subprocess.run(
            ssh_command(host, user, connect_timeout),
            input=REMOTE_PROBE,
            text=True,
            capture_output=True,
            check=False,
            timeout=command_timeout,
        )
    except subprocess.TimeoutExpired:
        return HostResult(
            host, "unreachable", "unknown", "unknown", "unknown", 0, 0, ("SSH timeout",)
        )
    if process.returncode != 0:
        diagnostic = " ".join(process.stderr.split())[:MAX_DIAGNOSTIC_LENGTH]
        return HostResult(
            host,
            "unreachable",
            "unknown",
            "unknown",
            "unknown",
            0,
            0,
            (diagnostic or f"SSH exit {process.returncode}",),
        )
    return parse_remote_output(host, process.stdout)


def write_report(path: Path, results: list[HostResult]) -> None:
    """Write one sanitized TSV row per host without remote command output."""
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as report:
        report.write(
            "host\tstatus\tinit\tsermod\tconfig\tconfigured\tchecked\tmismatches\n"
        )
        for result in results:
            detail = "; ".join(result.mismatches).replace("\t", " ").replace("\n", " ")
            report.write(
                f"{result.host}\t{result.status}\t{result.init}\t{result.daemon}\t{result.config}\t"
                f"{result.configured}\t{result.checked}\t{detail}\n",
            )


def parser() -> argparse.ArgumentParser:
    """Build the command-line parser."""
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument(
        "--env",
        type=Path,
        default=DEFAULT_ENV,
        help="host inventory (default: .env.ssh)",
    )
    cli.add_argument(
        "--host",
        action="append",
        default=[],
        help="validate only this configured host; repeatable",
    )
    cli.add_argument("--ssh-user", default=DEFAULT_SSH_USER)
    cli.add_argument("--jobs", type=int, default=DEFAULT_JOBS)
    cli.add_argument(
        "--connect-timeout", type=int, default=DEFAULT_CONNECT_TIMEOUT_SECONDS
    )
    cli.add_argument(
        "--command-timeout", type=int, default=DEFAULT_COMMAND_TIMEOUT_SECONDS
    )
    cli.add_argument("--report", type=Path)
    return cli


def main(argv: list[str] | None = None) -> int:
    """Validate every selected host, continuing after individual failures."""
    args = parser().parse_args(argv)
    try:
        hosts = load_hosts(args.env)
    except (OSError, ValueError) as error:
        print(f"prevalidate_init_contracts: {error}", file=sys.stderr)
        return 64
    if args.host:
        unknown = sorted(set(args.host) - set(hosts))
        if unknown:
            print(
                f"prevalidate_init_contracts: hosts absent from inventory: {', '.join(unknown)}",
                file=sys.stderr,
            )
            return 64
        selected = set(args.host)
        hosts = [host for host in hosts if host in selected]
    if args.jobs <= 0 or args.connect_timeout <= 0 or args.command_timeout <= 0:
        print(
            "prevalidate_init_contracts: jobs and timeouts must be positive",
            file=sys.stderr,
        )
        return 64

    results: list[HostResult] = []
    with ThreadPoolExecutor(max_workers=min(args.jobs, len(hosts))) as executor:
        pending = {
            executor.submit(
                validate_host,
                host,
                args.ssh_user,
                args.connect_timeout,
                args.command_timeout,
            ): host
            for host in hosts
        }
        for future in as_completed(pending):
            result = future.result()
            results.append(result)
            print(
                f"{result.host}: {result.status} ({result.init}, {result.checked}/{result.configured} services)"
            )
    results.sort(key=lambda result: hosts.index(result.host))
    report = (
        args.report
        or Path(tempfile.gettempdir())
        / f"sermo-init-prevalidation-{datetime.now(UTC):%Y%m%d-%H%M%S}.tsv"
    )
    write_report(report, results)
    print(f"report: {report}")
    return 0 if all(result.status == "ok" for result in results) else 1


if __name__ == "__main__":
    raise SystemExit(main())
