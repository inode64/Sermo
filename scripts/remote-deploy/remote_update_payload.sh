#!/usr/bin/env bash
set -u

run_id="${1:-}"
payload="${2:-}"
web_password="${SERMO_WEB_PASSWORD:-sermo-remote-admin}"
ready_wait_seconds="${SERMO_READY_WAIT_SECONDS:-240}"

if [ -z "$run_id" ] || [ -z "$payload" ]; then
	echo "usage: $0 RUN_ID PAYLOAD_TGZ" >&2
	exit 64
fi

case "$ready_wait_seconds" in
	'' | *[!0-9]*)
		ready_wait_seconds=240
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

finish() {
	rc="$1"
	date -Is >"${out}/finished_at" 2>/dev/null || true
	if ! verify_protected_paths; then
		rc=70
	fi
	tar -C "$work" -czf "${work}/out.tar.gz" out >/dev/null 2>&1 || true
	exit "$rc"
}

http_get() {
	url="$1"
	if command -v curl >/dev/null 2>&1; then
		curl -fsS -u "admin:${web_password}" "$url"
		return $?
	fi
	if command -v wget >/dev/null 2>&1; then
		wget -qO- --user=admin --password="$web_password" "$url"
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

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
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

rm -rf /usr/share/sermo/catalog
read -r -a _payload_members <<< "$payload_members"
tar --no-same-owner -C / -xzf "$payload" "${_payload_members[@]}" >"${out}/payload_extract.out" 2>"${out}/payload_extract.err"
extract_rc=$?
printf '%s\n' "$extract_rc" >"${out}/payload_extract.rc"
if [ "$extract_rc" -ne 0 ]; then
	finish 20
fi

capture sermoctl_version /usr/bin/sermoctl --version
capture sermod_version /usr/bin/sermod --version
capture config_validate env SERMO_BACKEND="$config_backend" SERMO_INIT="$config_backend" /usr/bin/sermoctl --config /etc/sermo/sermo.yml config validate
if [ "$(cat "${out}/config_validate.rc" 2>/dev/null || echo 1)" != "0" ]; then
	finish 30
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
	finish 40
fi

ready_rc=1
ready_waited=0
while [ "$ready_waited" -lt "$ready_wait_seconds" ]; do
	if http_get "http://127.0.0.1:9797/livez?verbose" >"${out}/livez.out" 2>"${out}/livez.err"; then
		ready_rc=0
		break
	fi
	ready_waited=$((ready_waited + 1))
	sleep 1
done
printf '%s\n' "$ready_rc" >"${out}/livez.rc"
printf '%s\n' "$ready_waited" >"${out}/livez_waited_seconds"

http_get "http://127.0.0.1:9797/readyz?verbose" >"${out}/readyz.out" 2>"${out}/readyz.err"
printf '%s\n' "$?" >"${out}/readyz.rc"
http_get "http://127.0.0.1:9797/api/status" >"${out}/api_status.out" 2>"${out}/api_status.err"
printf '%s\n' "$?" >"${out}/api_status.rc"

if command -v ss >/dev/null 2>&1; then
	ss -ltnp 'sport = :9797' >"${out}/port9797_after" 2>&1 || true
elif command -v netstat >/dev/null 2>&1; then
	netstat -ltnp >"${out}/port9797_after" 2>&1 || true
fi

finish "$ready_rc"
