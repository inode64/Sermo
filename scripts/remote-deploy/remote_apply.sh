#!/usr/bin/env bash
set -u

run_id="${1:-}"
config_tgz="${2:-}"
web_password="${SERMO_WEB_PASSWORD:-sermo-remote-admin}"

if [ -z "$run_id" ] || [ -z "$config_tgz" ]; then
	echo "usage: $0 RUN_ID CONFIG_TGZ" >&2
	exit 64
fi

work="/tmp/sermo-apply-${run_id}"
out="${work}/out"
mkdir -p "$out"

capture() {
	name="$1"
	shift
	"$@" >"${out}/${name}.out" 2>"${out}/${name}.err"
	printf '%s\n' "$?" >"${out}/${name}.rc"
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
	echo "remote apply must run as root" >&2
	exit 10
fi

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

rm -rf /etc/sermo/services /etc/sermo/apps /etc/sermo/notifiers /etc/sermo/watches /etc/sermo/networks /etc/sermo/storages /etc/sermo/mounts
mkdir -p /etc/sermo/services /etc/sermo/apps /etc/sermo/notifiers /etc/sermo/watches /etc/sermo/networks /etc/sermo/storages /etc/sermo/mounts /etc/sermo/templates
tar -C / -xzf "$config_tgz" >"${out}/config_extract.out" 2>"${out}/config_extract.err"
extract_rc=$?
printf '%s\n' "$extract_rc" >"${out}/config_extract.rc"
if [ "$extract_rc" -ne 0 ]; then
	tar -C "$work" -czf "${work}/out.tar.gz" out >/dev/null 2>&1 || true
	exit 20
fi

find /etc/sermo -maxdepth 3 -type f | sort >"${out}/config_files" 2>/dev/null || true
capture config_validate /usr/bin/sermoctl --config /etc/sermo/sermo.yml config validate
if [ "$(cat "${out}/config_validate.rc" 2>/dev/null || echo 1)" != "0" ]; then
	tar -C "$work" -czf "${work}/out.tar.gz" out >/dev/null 2>&1 || true
	exit 30
fi

if command -v ss >/dev/null 2>&1; then
	ss -ltnp 'sport = :9797' >"${out}/port9797_before_start" 2>&1 || true
elif command -v netstat >/dev/null 2>&1; then
	netstat -ltnp >"${out}/port9797_before_start" 2>&1 || true
fi

if [ "$init" = "systemd" ]; then
	systemctl daemon-reload >"${out}/systemctl_daemon_reload.out" 2>"${out}/systemctl_daemon_reload.err" || true
	capture systemctl_enable systemctl enable sermod
	if systemctl is-active --quiet sermod; then
		capture sermod_restart systemctl restart sermod
	else
		capture sermod_start systemctl start sermod
	fi
	capture sermod_is_active systemctl is-active sermod
	systemctl status sermod --no-pager >"${out}/sermod_status_after" 2>&1 || true
	journalctl -u sermod -n 200 --no-pager >"${out}/sermod_journal_tail" 2>&1 || true
elif [ "$init" = "openrc" ]; then
	capture rc_update_add rc-update add sermod default
	if rc-service sermod status >/dev/null 2>&1; then
		capture sermod_restart rc-service sermod restart
	else
		capture sermod_start rc-service sermod start
	fi
	capture sermod_is_active rc-service sermod status
	tail -n 200 /var/log/sermod.log >"${out}/sermod_log_tail" 2>&1 || true
else
	echo "unsupported init" >"${out}/sermod_start.err"
	echo 40 >"${out}/sermod_start.rc"
	tar -C "$work" -czf "${work}/out.tar.gz" out >/dev/null 2>&1 || true
	exit 40
fi

ready_rc=1
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 31 32 33 34 35 36 37 38 39 40 41 42 43 44 45 46 47 48 49 50 51 52 53 54 55 56 57 58 59 60; do
	if http_get "http://127.0.0.1:9797/livez?verbose" >"${out}/livez.out" 2>"${out}/livez.err"; then
		ready_rc=0
		break
	fi
	sleep 1
done
printf '%s\n' "$ready_rc" >"${out}/livez.rc"
http_get "http://127.0.0.1:9797/readyz?verbose" >"${out}/readyz.out" 2>"${out}/readyz.err"
printf '%s\n' "$?" >"${out}/readyz.rc"
http_get "http://127.0.0.1:9797/api/status" >"${out}/api_status.out" 2>"${out}/api_status.err"
printf '%s\n' "$?" >"${out}/api_status.rc"

if command -v ss >/dev/null 2>&1; then
	ss -ltnp 'sport = :9797' >"${out}/port9797_after" 2>&1 || true
elif command -v netstat >/dev/null 2>&1; then
	netstat -ltnp >"${out}/port9797_after" 2>&1 || true
fi

date -Is >"${out}/finished_at" 2>/dev/null || true
tar -C "$work" -czf "${work}/out.tar.gz" out >/dev/null 2>&1 || true
exit "$ready_rc"
