#!/usr/bin/env bash
# Local orchestrator: update Sermo on remote hosts over SSH, with an optional
# per-host configuration regeneration pass.
#
# Per host it uploads the locally built payload and runs
# remote_update_payload.sh (binaries + catalog, validate, restart, verify).
# With --with-config it then runs remote_collect_inventory.sh (read-only),
# regenerates that host's configuration locally with generate_install_config.py,
# backs up /etc/sermo to /etc/sermo.backup.<run-id> and applies the generated
# tree with remote_apply.sh.
#
# Unreachable or unhealthy hosts are recorded and skipped, never forced
# through (see README.md "Fleet install and update failure handling").
set -u

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "${script_dir}/../.." && pwd)"

ssh_opts="${SERMO_SSH_OPTS:-}"
remote_command_timeout_seconds="${SERMO_REMOTE_COMMAND_TIMEOUT_SECONDS:-1500}"
ready_wait_seconds="${SERMO_READY_WAIT_SECONDS:-600}"
credentials_file="/etc/sermo/credentials.env"
ssh_user="root"
with_config=0
include_inactive_installed_services=0
only_services=""
skip_build=0
reuse_candidate=0
normalize_retired_watch_types=0
dry_run=0
run_root=""
hosts=()

usage() {
	cat <<'USAGE'
usage: update_fleet.sh [options] HOST [HOST...]

Update Sermo (binaries + packaged catalog) on remote hosts after a local
build, optionally regenerating and applying each host's configuration.

options:
  --hosts FILE      read additional hosts from FILE (one per line, # comments)
  --with-config     after the binary update: collect read-only inventory,
                    regenerate the host configuration locally and apply it
                    (backs up /etc/sermo to /etc/sermo.backup.<run-id> first)
  --include-inactive-installed-services
                    with --with-config, also generate installed catalog
                    services whose init units are currently inactive
  --only-services LIST
                    with --with-config, generate only these canonical catalog
                    service names (comma-separated)
  --run-root DIR    local working directory (default: mktemp under /tmp)
  --ssh-user USER   SSH user, must reach root on the target (default: root)
  --skip-build      reuse existing bin/sermoctl and bin/sermod; the staged
                    candidate validator is always rebuilt for this run
  --reuse-candidate reuse the existing bin/sermoctl-candidate; requires
                    SERMO_RUN_ID so the candidate's compiled-in staging
                    catalog path matches this run
  --normalize-retired-watch-types
                    before the binary update, remove retired-check-type watch
                    files (autofs, entropy) from /etc/sermo with
                    remote_normalize_retired_watch_types.sh; the host is
                    restored and skipped when normalization fails
  --dry-run         print the per-host plan without contacting any host
  -h, --help        show this help

environment:
  SERMO_RUN_ID                run identifier override (default: upd-<timestamp>);
                              parallel instances on disjoint host sets share one
                              run id and pre-built payload via --skip-build
                              --reuse-candidate
  SERMO_SSH_OPTS              extra options for every ssh/scp invocation
  SERMO_REMOTE_COMMAND_TIMEOUT_SECONDS
                              maximum duration of one remote SSH command
                              (default: 1500)
  SERMO_READY_WAIT_SECONDS    Web UI readiness limit per remote phase
                              (default: 600)
USAGE
}

die() {
	echo "update_fleet: $*" >&2
	exit 64
}

while [ $# -gt 0 ]; do
	case "$1" in
		--hosts)
			[ $# -ge 2 ] || die "--hosts requires a file"
			[ -r "$2" ] || die "hosts file not readable: $2"
			while IFS= read -r line; do
				line="${line%%#*}"
				line="$(printf '%s' "$line" | tr -d '[:space:]')"
				[ -n "$line" ] && hosts+=("$line")
			done <"$2"
			shift 2
			;;
		--with-config) with_config=1; shift ;;
		--include-inactive-installed-services) include_inactive_installed_services=1; shift ;;
		--only-services)
			[ $# -ge 2 ] || die "--only-services requires a comma-separated list"
			only_services="$2"
			shift 2
			;;
		--run-root)
			[ $# -ge 2 ] || die "--run-root requires a directory"
			run_root="$2"
			shift 2
			;;
		--ssh-user)
			[ $# -ge 2 ] || die "--ssh-user requires a value"
			ssh_user="$2"
			shift 2
			;;
		--skip-build) skip_build=1; shift ;;
		--reuse-candidate) reuse_candidate=1; shift ;;
		--normalize-retired-watch-types) normalize_retired_watch_types=1; shift ;;
		--dry-run) dry_run=1; shift ;;
		-h | --help) usage; exit 0 ;;
		-*) die "unknown option: $1" ;;
		*) hosts+=("$1"); shift ;;
	esac
done

[ "${#hosts[@]}" -gt 0 ] || { usage >&2; exit 64; }
case "$remote_command_timeout_seconds" in
	'' | *[!0-9]*) die "SERMO_REMOTE_COMMAND_TIMEOUT_SECONDS must be a positive integer" ;;
	0) die "SERMO_REMOTE_COMMAND_TIMEOUT_SECONDS must be greater than zero" ;;
esac
case "$ready_wait_seconds" in
	'' | *[!0-9]*) die "SERMO_READY_WAIT_SECONDS must be a positive integer" ;;
	0) die "SERMO_READY_WAIT_SECONDS must be greater than zero" ;;
esac
for host in "${hosts[@]}"; do
	case "$host" in
		*[![:alnum:]._-]*) die "invalid host name: $host" ;;
	esac
done

run_id="${SERMO_RUN_ID:-upd-$(date +%Y%m%d-%H%M%S)}"
case "$run_id" in
	*[!a-zA-Z0-9._-]* | '') die "SERMO_RUN_ID must be non-empty and use only [a-zA-Z0-9._-]" ;;
esac
if [ "$reuse_candidate" = "1" ] && [ -z "${SERMO_RUN_ID:-}" ]; then
	die "--reuse-candidate requires SERMO_RUN_ID (the candidate is compiled for one run id)"
fi
remote_dir="/tmp/sermo-${run_id}"
payload_name="sermo-install-payload.tgz"

if [ "$dry_run" = "1" ]; then
	echo "dry-run: no host will be contacted"
	echo "run id: ${run_id}"
	echo "plan per host (${ssh_user}@HOST):"
	echo "  1. preflight: ssh reachable, ${remote_dir} creatable, /tmp space"
	echo "  2. upload payload + remote scripts to ${remote_dir}"
	if [ "$normalize_retired_watch_types" = "1" ]; then
		echo "  2b. run remote_normalize_retired_watch_types.sh ${run_id} (removes retired autofs/entropy watch files)"
	fi
	echo "  3. run remote_update_payload.sh ${run_id} ${remote_dir}/${payload_name}"
	if [ "$with_config" = "1" ]; then
		echo "  4. run remote_collect_inventory.sh ${run_id} (read-only)"
		echo "  5. regenerate config locally (generate_install_config.py)"
		echo "  6. back up /etc/sermo to /etc/sermo.backup.${run_id}"
		echo "  7. apply config with remote_apply.sh ${run_id}"
		if [ "$include_inactive_installed_services" = "1" ]; then
			echo "     including installed catalog services with inactive init units"
		fi
	fi
	echo "hosts:"
	printf '  %s\n' "${hosts[@]}"
	exit 0
fi

if [ "$skip_build" = "0" ]; then
	echo "building sermo (GOAMD64=v1 SERMO_DATADIR=/usr/share/sermo make build)"
	(cd "$repo" && GOAMD64=v1 SERMO_DATADIR=/usr/share/sermo make build) || die "local build failed"
fi
if [ ! -x "${repo}/bin/sermoctl" ] || [ ! -x "${repo}/bin/sermod" ]; then
	die "missing bin/sermoctl or bin/sermod (run without --skip-build)"
fi

candidate_datadir="/tmp/sermo-update-${run_id}/stage/usr/share/sermo"
candidate_sermoctl="${repo}/bin/sermoctl-candidate"
if [ "$reuse_candidate" = "1" ]; then
	echo "reusing staged candidate validator for run id ${run_id}"
else
	echo "building staged candidate validator (SERMO_DATADIR=${candidate_datadir})"
	(cd "$repo" && GOAMD64=v1 SERMO_DATADIR="$candidate_datadir" make build-candidate-sermoctl) \
		|| die "candidate validator build failed"
fi
[ -x "$candidate_sermoctl" ] || die "missing candidate validator: ${candidate_sermoctl}"

if [ -z "$run_root" ]; then
	run_root="$(mktemp -d "/tmp/sermo-fleet-${run_id}.XXXX")" || die "mktemp failed"
else
	mkdir -p "$run_root" || die "cannot create run root: $run_root"
fi
report="${run_root}/report.tsv"
printf 'host\tphase\tstatus\tdetail\n' >"$report"

echo "run root: ${run_root}"
"${script_dir}/prepare_payload.sh" "$run_root" "$repo" "$candidate_sermoctl" >/dev/null \
	|| die "payload preparation failed"
payload_local="${run_root}/sermo-install-payload.tgz"

run_ssh() {
	host="$1"
	shift
	# SERMO_SSH_OPTS is intentionally word-split; remote commands deliberately
	# interpolate run_id/paths client-side. A local timeout also bounds read-only
	# inventory commands whose remote utility may otherwise block indefinitely.
	# shellcheck disable=SC2086,SC2029
	timeout --foreground "${remote_command_timeout_seconds}s" ssh $ssh_opts "${ssh_user}@${host}" "$@"
}

run_scp() {
	# shellcheck disable=SC2086
	timeout --foreground "${remote_command_timeout_seconds}s" scp $ssh_opts -q "$@"
}

record() {
	printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" >>"$report"
}

fetch_failure_artifacts() {
	host="$1"
	remote_out="$2"
	mkdir -p "${run_root}/hosts/${host}"
	run_scp "${ssh_user}@${host}:${remote_out}" "${run_root}/hosts/${host}/" 2>/dev/null || true
}

failures=0
processed=0

for host in "${hosts[@]}"; do
	processed=$((processed + 1))
	host_dir="${run_root}/hosts/${host}"
	mkdir -p "$host_dir"
	echo "=== ${host} (${processed}/${#hosts[@]}) ==="

	if ! run_ssh "$host" "df -Pk /tmp | awk 'NR == 2 { exit (\$4 < 204800) }' && mkdir -p ${remote_dir}"; then
		echo "  preflight failed (unreachable or < 200 MiB free in /tmp); skipping" >&2
		record "$host" "preflight" "skipped" "ssh unreachable or /tmp space below 200 MiB"
		failures=$((failures + 1))
		continue
	fi

	if ! run_scp "$payload_local" \
		"${script_dir}/remote_update_payload.sh" \
		"${script_dir}/remote_collect_inventory.sh" \
		"${script_dir}/remote_inventory_common.sh" \
		"${script_dir}/remote_normalize_retired_watch_types.sh" \
		"${script_dir}/remote_apply.sh" \
		"${ssh_user}@${host}:${remote_dir}/"; then
		echo "  upload failed; skipping" >&2
		record "$host" "upload" "failed" "scp payload/scripts"
		failures=$((failures + 1))
		continue
	fi

	if [ "$normalize_retired_watch_types" = "1" ]; then
		normalize_log="${host_dir}/normalize.log"
		if ! run_ssh "$host" "bash ${remote_dir}/remote_normalize_retired_watch_types.sh ${run_id} ${remote_dir}/${payload_name}" \
			>"$normalize_log" 2>&1; then
			echo "  retired-watch normalization failed; host left restored and skipped" >&2
			record "$host" "normalize" "failed" "remote_normalize_retired_watch_types.sh non-zero"
			fetch_failure_artifacts "$host" "/tmp/sermo-update-${run_id}/normalize-out"
			failures=$((failures + 1))
			continue
		fi
		record "$host" "normalize" "ok" "retired watch types removed or none present"
	fi

	update_log="${host_dir}/update.log"
	# tee keeps the remote progress live while retaining it for the report; the
	# pipeline's own status is tee's, so the update's status comes from
	# PIPESTATUS or a failing host would be recorded as a success.
	run_ssh "$host" "env SERMO_READY_WAIT_SECONDS='${ready_wait_seconds}' bash ${remote_dir}/remote_update_payload.sh ${run_id} ${remote_dir}/${payload_name}" \
		| tee "$update_log" | grep -v '^SUMMARY	'
	if [ "${PIPESTATUS[0]}" -ne 0 ]; then
		echo "  binary update failed; collecting artifacts and skipping" >&2
		record "$host" "update" "failed" "remote_update_payload.sh non-zero"
		fetch_failure_artifacts "$host" "/tmp/sermo-update-${run_id}/out.tar.gz"
		failures=$((failures + 1))
		continue
	fi
	# The remote updater reports what it actually did; carry its one machine
	# readable line into the report so a successful host is more than "ok".
	update_detail="$(sed -n 's/^SUMMARY\t//p' "$update_log" | tail -1)"
	record "$host" "update" "ok" "${update_detail:-binaries and catalog refreshed}"

	if [ "$with_config" = "1" ]; then
		if ! run_ssh "$host" "bash ${remote_dir}/remote_collect_inventory.sh ${run_id}" >/dev/null; then
			echo "  inventory collection failed; config not touched" >&2
			record "$host" "inventory" "failed" "remote_collect_inventory.sh non-zero"
			failures=$((failures + 1))
			continue
		fi
		stage_root="${run_root}/stage/${host}"
		mkdir -p "${stage_root}/${host}"
		if ! run_scp "${ssh_user}@${host}:/tmp/sermo-inventory-${run_id}/out.tar.gz" "${host_dir}/inventory.tar.gz" \
			|| ! tar -C "${stage_root}/${host}" -xzf "${host_dir}/inventory.tar.gz"; then
			echo "  inventory download failed; config not touched" >&2
			record "$host" "inventory" "failed" "fetch/extract out.tar.gz"
			failures=$((failures + 1))
			continue
		fi
		# Existing fleet members keep the credential file as the sole source of
		# Web UI credentials. Do not place a password in an SSH argv, generated
		# config archive, backup, report or local process list.
		if ! run_ssh "$host" "test -r ${credentials_file}" 2>/dev/null; then
			echo "  credentials file is missing; config not touched" >&2
			record "$host" "credentials" "failed" "missing ${credentials_file}"
			failures=$((failures + 1))
			continue
		fi
		cred_flag=(--web-password-file "$credentials_file")
		inactive_services_flag=()
		if [ "$include_inactive_installed_services" = "1" ]; then
			inactive_services_flag=(--include-inactive-installed-services)
		fi
		only_services_flag=()
		if [ -n "$only_services" ]; then
			only_services_flag=(--only-services "$only_services")
		fi
		if ! "${script_dir}/generate_install_config.py" \
			--stage-root "$stage_root" \
			--configs-root "${run_root}/configs" \
			--report "${host_dir}/config-report.json" \
			"${cred_flag[@]}" "${inactive_services_flag[@]}" "${only_services_flag[@]}" >/dev/null; then
			echo "  config generation failed; config not touched" >&2
			record "$host" "generate" "failed" "generate_install_config.py non-zero"
			failures=$((failures + 1))
			continue
		fi
		config_tgz="${run_root}/configs/${host}/sermo-config.tgz"
		if [ ! -f "$config_tgz" ]; then
			echo "  generator produced no config for ${host}; config not touched" >&2
			record "$host" "generate" "failed" "missing ${config_tgz}"
			failures=$((failures + 1))
			continue
		fi
		if ! run_scp "$config_tgz" "${ssh_user}@${host}:${remote_dir}/sermo-config.tgz" \
			|| ! run_ssh "$host" "cp -a /etc/sermo /etc/sermo.backup.${run_id} && env SERMO_READY_WAIT_SECONDS='${ready_wait_seconds}' bash ${remote_dir}/remote_apply.sh ${run_id} ${remote_dir}/sermo-config.tgz"; then
			echo "  config apply failed; /etc/sermo.backup.${run_id} kept on host" >&2
			record "$host" "apply" "failed" "remote_apply.sh non-zero; backup /etc/sermo.backup.${run_id}"
			fetch_failure_artifacts "$host" "/tmp/sermo-apply-${run_id}/out.tar.gz"
			failures=$((failures + 1))
			continue
		fi
		record "$host" "apply" "ok" "config regenerated; backup /etc/sermo.backup.${run_id}"
	fi

	run_ssh "$host" "rm -rf ${remote_dir} /tmp/sermo-inventory-${run_id} /tmp/sermo-apply-${run_id}" || true
	echo "  ok"
done

echo
echo "report: ${report}"
column -t -s "$(printf '\t')" "$report" 2>/dev/null || cat "$report"
if [ "$failures" -gt 0 ]; then
	echo "completed with ${failures} failed/skipped step(s); failed hosts keep their remote artifacts for diagnosis" >&2
	exit 1
fi
echo "all hosts updated"
