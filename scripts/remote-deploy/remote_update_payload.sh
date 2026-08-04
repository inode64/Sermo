#!/usr/bin/env bash
set -u

run_id="${1:-}"
payload="${2:-}"
web_password="${SERMO_WEB_PASSWORD:-sermo-remote-admin}"
ready_wait_seconds="${SERMO_READY_WAIT_SECONDS:-240}"
http_timeout_seconds="${SERMO_HTTP_TIMEOUT_SECONDS:-5}"
keep_remote_artifacts="${SERMO_KEEP_REMOTE_ARTIFACTS:-0}"

if [ -z "$run_id" ] || [ -z "$payload" ]; then
	echo "usage: $0 RUN_ID PAYLOAD_TGZ" >&2
	exit 64
fi

case "$run_id" in
	*[![:alnum:]._-]*)
		echo "RUN_ID may contain only letters, numbers, dot, underscore and hyphen" >&2
		exit 64
		;;
esac

if ! [[ "$payload" =~ ^/tmp/sermo-[[:alnum:]._-]+/[[:alnum:]._-]+\.tgz$ ]]; then
	echo "PAYLOAD_TGZ must be a direct .tgz file in a managed /tmp/sermo-* directory" >&2
	exit 64
fi

case "$ready_wait_seconds" in
	'' | *[!0-9]*)
		ready_wait_seconds=240
		;;
esac

case "$http_timeout_seconds" in
	'' | *[!0-9]*)
		http_timeout_seconds=5
		;;
esac

case "$keep_remote_artifacts" in
	0 | 1) ;;
	*)
		echo "SERMO_KEEP_REMOTE_ARTIFACTS must be 0 or 1" >&2
		exit 64
		;;
esac

work="/tmp/sermo-update-${run_id}"
out="${work}/out"
mkdir -p "$out"

capture() {
	name="$1"
	shift
	"$@" >"${out}/${name}.out" 2>"${out}/${name}.err"
	printf '%s\n' "$?" >"${out}/${name}.rc"
}

protected_paths="/ /etc /usr /usr/lib /etc/systemd /usr/lib/tmpfiles.d /etc/init.d /usr/share"

snapshot_protected_paths() {
	dest="$1"
	: >"$dest"
	for path in $protected_paths; do
		if [ -e "$path" ]; then
			stat -c '%n|%F|%a|%u|%g' "$path" >>"$dest" 2>/dev/null || printf '%s|stat-error\n' "$path" >>"$dest"
		else
			printf '%s|missing\n' "$path" >>"$dest"
		fi
	done
}

verify_protected_paths() {
	snapshot_protected_paths "${out}/protected_path_metadata.after"
	if diff -u "${out}/protected_path_metadata.before" "${out}/protected_path_metadata.after" >"${out}/protected_path_metadata.diff"; then
		printf '0\n' >"${out}/protected_path_metadata.rc"
		return 0
	fi
	printf '1\n' >"${out}/protected_path_metadata.rc"
	return 1
}

rollback() {
	if [ -f "${work}/sermoctl.previous" ]; then
		install -o 0 -g 0 -m 0755 "${work}/sermoctl.previous" /usr/bin/sermoctl
	fi
	if [ -f "${work}/sermod.previous" ]; then
		install -o 0 -g 0 -m 0755 "${work}/sermod.previous" /usr/bin/sermod
	fi
	if [ -d "${work}/catalog.previous" ]; then
		rm -rf /usr/share/sermo/catalog
		mv "${work}/catalog.previous" /usr/share/sermo/catalog
	fi
	case "$init" in
		systemd) capture rollback_restart systemctl restart sermod ;;
		openrc) capture rollback_restart rc-service sermod restart ;;
	esac
}

cleanup_success() {
	[ "$keep_remote_artifacts" = "0" ] || return 0

	case "$work" in
		/tmp/sermo-update-*) ;;
		*)
		echo "refusing to remove unexpected work path: $work" >&2
		return 1
		;;
	esac
	rm -f -- "$payload" || return 1
	rm -rf -- "$work" || return 1
}

finish() {
	rc="$1"
	date -Is >"${out}/finished_at" 2>/dev/null || true
	if ! verify_protected_paths; then
		rc=70
	fi
	if [ "$rc" -ne 0 ] || [ "$keep_remote_artifacts" = "1" ]; then
		tar -C "$work" -czf "${work}/out.tar.gz" out >/dev/null 2>&1 || true
	fi
	if [ "$rc" -eq 0 ] && ! cleanup_success; then
		echo "Sermo updated, but temporary artifact cleanup failed" >&2
		rc=71
	fi
	exit "$rc"
}

web_runtime_dir() {
	runtime_dir="/run/sermo"
	if [ -r /etc/sermo/sermo.yml ]; then
		configured_runtime="$(awk '
			/^paths:[[:space:]]*(#.*)?$/ { in_paths = 1; next }
			in_paths && /^[^[:space:]]/ { exit }
			in_paths && /^[[:space:]]+runtime:[[:space:]]*/ {
				sub(/^[[:space:]]*runtime:[[:space:]]*/, "")
				sub(/[[:space:]]+#.*$/, "")
				gsub(/^[[:space:]]+|[[:space:]]+$/, "")
				if (($0 ~ /^".*"$/) || ($0 ~ /^'"'"'.*'"'"'$/)) {
					sub(/^./, "")
					sub(/.$/, "")
				}
				print
				exit
			}' /etc/sermo/sermo.yml)"
		case "$configured_runtime" in
			/*) runtime_dir="$configured_runtime" ;;
		esac
	fi
	printf '%s\n' "$runtime_dir"
}

web_admin_password() {
	token_file="$(web_runtime_dir)/web.token"
	if [ -r "$token_file" ]; then
		IFS= read -r token <"$token_file" || true
		token="${token%$'\r'}"
		if [ -n "$token" ]; then
			printf '%s' "$token"
			return
		fi
	fi
	printf '%s' "$web_password"
}

http_get() {
	url="$1"
	admin_password="$(web_admin_password)"
	if command -v curl >/dev/null 2>&1; then
		curl --connect-timeout "$http_timeout_seconds" --max-time "$http_timeout_seconds" -fsS -u "admin:${admin_password}" "$url"
		return $?
	fi
	if command -v wget >/dev/null 2>&1; then
		wget -qO- -T "$http_timeout_seconds" --user=admin --password="$admin_password" "$url"
		return $?
	fi
	return 127
}

if [ "$(id -u)" != "0" ]; then
	echo "remote update must run as root" >&2
	exit 10
fi

snapshot_protected_paths "${out}/protected_path_metadata.before"

hostname -f >"${out}/hostname_fqdn" 2>/dev/null || hostname >"${out}/hostname_fqdn" 2>/dev/null || true
hostname >"${out}/hostname" 2>/dev/null || true
date -Is >"${out}/started_at" 2>/dev/null || true

if command -v systemctl >/dev/null 2>&1 \
	&& { [ -d /run/systemd/system ] || [ "$(cat /proc/1/comm 2>/dev/null)" = "systemd" ]; }; then
	init="systemd"
elif command -v rc-service >/dev/null 2>&1; then
	init="openrc"
else
	init="unknown"
fi
printf '%s\n' "$init" >"${out}/init"
config_backend="$init"
case "$config_backend" in
	systemd | openrc) ;;
	*) config_backend="" ;;
esac

payload_members="usr/bin/sermoctl usr/bin/sermod usr/share/sermo/catalog etc/sermo/templates/default-alert.yml"
: >"${out}/payload_skipped_members"
if [ "$init" = "systemd" ]; then
	if [ -d /etc/systemd/system ]; then
		payload_members="${payload_members} etc/systemd/system/sermod.service"
	else
		printf '%s\n' "etc/systemd/system/sermod.service: /etc/systemd/system missing" >>"${out}/payload_skipped_members"
	fi
	if [ -d /usr/lib/tmpfiles.d ]; then
		payload_members="${payload_members} usr/lib/tmpfiles.d/sermo.conf"
	else
		printf '%s\n' "usr/lib/tmpfiles.d/sermo.conf: /usr/lib/tmpfiles.d missing" >>"${out}/payload_skipped_members"
	fi
elif [ "$init" = "openrc" ]; then
	if [ -d /etc/init.d ]; then
		payload_members="${payload_members} etc/init.d/sermod"
	else
		printf '%s\n' "etc/init.d/sermod: /etc/init.d missing" >>"${out}/payload_skipped_members"
	fi
fi
printf '%s\n' "$payload_members" >"${out}/payload_members"

read -r -a _payload_members <<< "$payload_members"

# Stage the payload and validate the live configuration with the CANDIDATE
# sermoctl before anything on disk changes. A configuration the new binary
# rejects — a canonically retired key, for example — must abort the update with
# the host untouched, instead of leaving new binaries beside the still-running
# old process for the next restart to trip over.
stage="${work}/stage"
mkdir -p "$stage"
tar --no-same-owner -C "$stage" -xzf "$payload" "${_payload_members[@]}" >"${out}/payload_stage.out" 2>"${out}/payload_stage.err"
stage_rc=$?
printf '%s\n' "$stage_rc" >"${out}/payload_stage.rc"
if [ "$stage_rc" -ne 0 ]; then
	finish 20
fi

capture sermoctl_version "${stage}/usr/bin/sermoctl" --version
capture sermod_version "${stage}/usr/bin/sermod" --version
capture config_validate env SERMO_BACKEND="$config_backend" SERMO_INIT="$config_backend" "${stage}/usr/bin/sermoctl" --config /etc/sermo/sermo.yml config validate
if [ "$(cat "${out}/config_validate.rc" 2>/dev/null || echo 1)" != "0" ]; then
	finish 30
fi

# Keep the previous binaries and catalog so a failed restart or readiness check
# can put the host back exactly as it was.
cp -a /usr/bin/sermoctl "${work}/sermoctl.previous" 2>/dev/null || true
cp -a /usr/bin/sermod "${work}/sermod.previous" 2>/dev/null || true
if [ -d /usr/share/sermo/catalog ]; then
	rm -rf "${work}/catalog.previous"
	mv /usr/share/sermo/catalog "${work}/catalog.previous"
fi

tar --no-same-owner -C / -xzf "$payload" "${_payload_members[@]}" >"${out}/payload_extract.out" 2>"${out}/payload_extract.err"
extract_rc=$?
printf '%s\n' "$extract_rc" >"${out}/payload_extract.rc"
if [ "$extract_rc" -ne 0 ]; then
	rollback
	finish 20
fi

if [ "$init" = "systemd" ]; then
	systemctl daemon-reload >"${out}/systemctl_daemon_reload.out" 2>"${out}/systemctl_daemon_reload.err" || true
	capture sermod_restart systemctl restart sermod
	capture sermod_is_active systemctl is-active sermod
	systemctl status sermod --no-pager >"${out}/sermod_status_after" 2>&1 || true
	journalctl -u sermod -n 200 --no-pager >"${out}/sermod_journal_tail" 2>&1 || true
elif [ "$init" = "openrc" ]; then
	capture sermod_restart rc-service sermod restart
	capture sermod_is_active rc-service sermod status
	tail -n 200 /var/log/sermod.log >"${out}/sermod_log_tail" 2>&1 || true
else
	echo "unsupported init" >"${out}/sermod_restart.err"
	echo 40 >"${out}/sermod_restart.rc"
	rollback
	finish 40
fi

livez_rc=1
started_at="$(date +%s)"
deadline=$((started_at + ready_wait_seconds))
while [ "$(date +%s)" -lt "$deadline" ]; do
	if http_get "http://127.0.0.1:9797/livez?verbose" >"${out}/livez.out" 2>"${out}/livez.err"; then
		livez_rc=0
		break
	fi
	sleep 1
done
livez_waited=$(($(date +%s) - started_at))
printf '%s\n' "$livez_rc" >"${out}/livez.rc"
printf '%s\n' "$livez_waited" >"${out}/livez_waited_seconds"

readyz_rc=1
while [ "$(date +%s)" -lt "$deadline" ]; do
	if http_get "http://127.0.0.1:9797/readyz?verbose" >"${out}/readyz.out" 2>"${out}/readyz.err"; then
		readyz_rc=0
		break
	fi
	sleep 1
done
readyz_waited=$(($(date +%s) - started_at))
printf '%s\n' "$readyz_rc" >"${out}/readyz.rc"
printf '%s\n' "$readyz_waited" >"${out}/readyz_waited_seconds"

http_get "http://127.0.0.1:9797/" >"${out}/web_html.out" 2>"${out}/web_html.err"
printf '%s\n' "$?" >"${out}/web_html.rc"
http_get "http://127.0.0.1:9797/api/dashboard" >"${out}/api_dashboard.out" 2>"${out}/api_dashboard.err"
printf '%s\n' "$?" >"${out}/api_dashboard.rc"

if command -v ss >/dev/null 2>&1; then
	ss -ltnp 'sport = :9797' >"${out}/port9797_after" 2>&1 || true
elif command -v netstat >/dev/null 2>&1; then
	netstat -ltnp >"${out}/port9797_after" 2>&1 || true
fi

if [ "$livez_rc" -ne 0 ] || [ "$readyz_rc" -ne 0 ] \
	|| [ "$(cat "${out}/web_html.rc")" != "0" ] \
	|| [ "$(cat "${out}/api_dashboard.rc")" != "0" ]; then
	# The new binaries are on disk but the daemon or dashboard did not become
	# healthy: restore the previous binaries and catalog and restart, so the host
	# keeps a working Sermo instead of one that only fails at the next restart.
	rollback
	finish 50
fi

finish 0
