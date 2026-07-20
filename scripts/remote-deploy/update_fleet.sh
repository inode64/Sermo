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

web_password="${SERMO_WEB_PASSWORD:-sermo-remote-admin}"
ssh_opts="${SERMO_SSH_OPTS:-}"
ssh_user="root"
with_config=0
skip_build=0
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
  --run-root DIR    local working directory (default: mktemp under /tmp)
  --ssh-user USER   SSH user, must reach root on the target (default: root)
  --skip-build      reuse existing bin/sermoctl and bin/sermod
  --dry-run         print the per-host plan without contacting any host
  -h, --help        show this help

environment:
  SERMO_WEB_PASSWORD          admin password for Web UI verification and for
                              regenerated configs (default: sermo-remote-admin)
  SERMO_SSH_OPTS              extra options for every ssh/scp invocation
  SERMO_READY_WAIT_SECONDS    forwarded to the remote scripts
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
		--dry-run) dry_run=1; shift ;;
		-h | --help) usage; exit 0 ;;
		-*) die "unknown option: $1" ;;
		*) hosts+=("$1"); shift ;;
	esac
done

[ "${#hosts[@]}" -gt 0 ] || { usage >&2; exit 64; }
case "$web_password" in
	*"'"*) die "SERMO_WEB_PASSWORD must not contain single quotes" ;;
esac
for host in "${hosts[@]}"; do
	case "$host" in
		*[![:alnum:]._-]*) die "invalid host name: $host" ;;
	esac
done

run_id="upd-$(date +%Y%m%d-%H%M%S)"
remote_dir="/tmp/sermo-${run_id}"
payload_name="sermo-install-payload.tgz"

if [ "$dry_run" = "1" ]; then
	echo "dry-run: no host will be contacted"
	echo "run id: ${run_id}"
	echo "plan per host (${ssh_user}@HOST):"
	echo "  1. preflight: ssh reachable, ${remote_dir} creatable, /tmp space"
	echo "  2. upload payload + remote scripts to ${remote_dir}"
	echo "  3. run remote_update_payload.sh ${run_id} ${remote_dir}/${payload_name}"
	if [ "$with_config" = "1" ]; then
		echo "  4. run remote_collect_inventory.sh ${run_id} (read-only)"
		echo "  5. regenerate config locally (generate_install_config.py)"
		echo "  6. back up /etc/sermo to /etc/sermo.backup.${run_id}"
		echo "  7. apply config with remote_apply.sh ${run_id}"
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

if [ -z "$run_root" ]; then
	run_root="$(mktemp -d "/tmp/sermo-fleet-${run_id}.XXXX")" || die "mktemp failed"
else
	mkdir -p "$run_root" || die "cannot create run root: $run_root"
fi
report="${run_root}/report.tsv"
printf 'host\tphase\tstatus\tdetail\n' >"$report"

echo "run root: ${run_root}"
"${script_dir}/prepare_payload.sh" "$run_root" "$repo" >/dev/null || die "payload preparation failed"
payload_local="${run_root}/sermo-install-payload.tgz"

run_ssh() {
	host="$1"
	shift
	# SERMO_SSH_OPTS is intentionally word-split; remote commands deliberately
	# interpolate run_id/paths client-side.
	# shellcheck disable=SC2086,SC2029
	ssh $ssh_opts "${ssh_user}@${host}" "$@"
}

run_scp() {
	# shellcheck disable=SC2086
	scp $ssh_opts -q "$@"
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
		"${script_dir}/remote_apply.sh" \
		"${ssh_user}@${host}:${remote_dir}/"; then
		echo "  upload failed; skipping" >&2
		record "$host" "upload" "failed" "scp payload/scripts"
		failures=$((failures + 1))
		continue
	fi

	if ! run_ssh "$host" "env SERMO_WEB_PASSWORD='${web_password}' SERMO_READY_WAIT_SECONDS='${SERMO_READY_WAIT_SECONDS:-240}' bash ${remote_dir}/remote_update_payload.sh ${run_id} ${remote_dir}/${payload_name}"; then
		echo "  binary update failed; collecting artifacts and skipping" >&2
		record "$host" "update" "failed" "remote_update_payload.sh non-zero"
		fetch_failure_artifacts "$host" "/tmp/sermo-update-${run_id}/out.tar.gz"
		failures=$((failures + 1))
		continue
	fi
	record "$host" "update" "ok" "binaries and catalog refreshed"

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
		if ! "${script_dir}/generate_install_config.py" \
			--stage-root "$stage_root" \
			--configs-root "${run_root}/configs" \
			--report "${host_dir}/config-report.json" \
			--web-password "$web_password" >/dev/null; then
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
			|| ! run_ssh "$host" "cp -a /etc/sermo /etc/sermo.backup.${run_id} && env SERMO_WEB_PASSWORD='${web_password}' SERMO_READY_WAIT_SECONDS='${SERMO_READY_WAIT_SECONDS:-240}' bash ${remote_dir}/remote_apply.sh ${run_id} ${remote_dir}/sermo-config.tgz"; then
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
