#!/usr/bin/env bash
set -euo pipefail

run_id="${1:-}"
payload="${2:-}"

if [ -z "$run_id" ] || [ -z "$payload" ]; then
	echo "usage: $0 RUN_ID PAYLOAD_TGZ" >&2
	exit 64
fi

work="/tmp/sermo-binary-catalog-${run_id}"
out="${work}/out"
stage="/usr/share/sermo/.catalog-stage-${run_id}"
previous="/usr/share/sermo/.catalog-previous-${run_id}"
mkdir -p "$out" "$stage"

protected_paths="/ /etc /usr /usr/lib /etc/systemd /usr/lib/tmpfiles.d /etc/init.d /usr/share"

capture() {
	name="$1"
	shift
	if "$@" >"${out}/${name}.out" 2>"${out}/${name}.err"; then
		printf '0\n' >"${out}/${name}.rc"
	else
		printf '%s\n' "$?" >"${out}/${name}.rc"
	fi
}

http_get() {
	url="$1"
	if command -v curl >/dev/null 2>&1; then
		curl -fsS -u "admin:${web_password}" "$url"
		return
	fi
	if command -v wget >/dev/null 2>&1; then
		wget -qO- --user=admin --password="$web_password" "$url"
		return
	fi
	return 127
}

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

snapshot_config() {
	dest="$1"
	(
		cd /etc/sermo
		find . -printf '%y|%m|%u|%g|%s|%p\n' | LC_ALL=C sort
		find . -type f -print0 | LC_ALL=C sort -z | xargs -0r sha256sum
	) >"$dest"
}

verify_snapshots() {
	snapshot_protected_paths "${out}/protected_paths.after"
	snapshot_config "${out}/config.after"
	diff -u "${out}/protected_paths.before" "${out}/protected_paths.after" >"${out}/protected_paths.diff"
	diff -u "${out}/config.before" "${out}/config.after" >"${out}/config.diff"
}

rollback() {
	if [ -f "${work}/sermoctl.previous" ]; then
		install -o 0 -g 0 -m 0755 "${work}/sermoctl.previous" /usr/bin/sermoctl
	fi
	if [ -f "${work}/sermod.previous" ]; then
		install -o 0 -g 0 -m 0755 "${work}/sermod.previous" /usr/bin/sermod
	fi
	if [ -d "$previous" ]; then
		rm -rf /usr/share/sermo/catalog
		mv "$previous" /usr/share/sermo/catalog
	fi
}

finish() {
	rc="$1"
	if ! verify_snapshots; then
		rc=70
	fi
	tar -C "$work" -czf "${work}/out.tar.gz" out >/dev/null 2>&1 || true
	exit "$rc"
}

if [ "$(id -u)" != "0" ]; then
	echo "remote update must run as root" >&2
	exit 10
fi
if [ ! -f /etc/sermo/sermo.yml ] || [ ! -d /usr/share/sermo/catalog ]; then
	echo "existing Sermo config or catalog is missing" >&2
	exit 20
fi
if [ ! -x /usr/bin/sermoctl ] || [ ! -x /usr/bin/sermod ]; then
	echo "existing Sermo binaries are missing" >&2
	exit 21
fi

web_password="$(awk '
	BEGIN { inside = 0 }
	/^web:[[:space:]]*$/ { inside = 1; next }
	/^[^[:space:]]/ { inside = 0 }
	inside && /^[[:space:]]*password:[[:space:]]*/ {
		value = $0
		sub(/^[[:space:]]*password:[[:space:]]*/, "", value)
		gsub(/^['\''"]|['\''"]$/, "", value)
		print value
		exit
	}
' /etc/sermo/sermo.yml)"
if [ -z "$web_password" ]; then
	echo "web.password is required to verify the configured authenticated Web UI" >&2
	exit 25
fi

snapshot_protected_paths "${out}/protected_paths.before"
snapshot_config "${out}/config.before"

while IFS= read -r member; do
	case "$member" in
		sermoctl | sermod | catalog | catalog/*) ;;
		*)
			echo "payload has unexpected member: $member" >&2
			finish 22
			;;
	esac
done < <(tar -tzf "$payload")

tar --no-same-owner -C "$work" -xzf "$payload"
if [ ! -x "${work}/sermoctl" ] || [ ! -x "${work}/sermod" ] || [ ! -d "${work}/catalog" ]; then
	finish 23
fi

cp /usr/bin/sermoctl "${work}/sermoctl.previous"
cp /usr/bin/sermod "${work}/sermod.previous"
rm -rf "$stage" "$previous"
mkdir -p "$stage"
if ! mv "${work}/catalog" "${stage}/catalog" ||
	! mv /usr/share/sermo/catalog "$previous" ||
	! mv "${stage}/catalog" /usr/share/sermo/catalog ||
	! rmdir "$stage" ||
	! install -o 0 -g 0 -m 0755 "${work}/sermoctl" /usr/bin/sermoctl ||
	! install -o 0 -g 0 -m 0755 "${work}/sermod" /usr/bin/sermod; then
	rollback
	finish 24
fi

init="unknown"
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
	init="systemd"
elif command -v rc-service >/dev/null 2>&1; then
	init="openrc"
fi
printf '%s\n' "$init" >"${out}/init"
capture config_validate /usr/bin/sermoctl --config /etc/sermo/sermo.yml config validate
if [ "$(cat "${out}/config_validate.rc")" != "0" ]; then
	rollback
	finish 30
fi
capture sermoctl_version /usr/bin/sermoctl --version
capture sermod_version /usr/bin/sermod --version

case "$init" in
	systemd)
		capture sermod_restart systemctl restart sermod
		capture sermod_status systemctl is-active sermod
		;;
	openrc)
		capture sermod_restart rc-service sermod restart
		capture sermod_status rc-service sermod status
		;;
	*)
		rollback
		finish 40
		;;
esac
if [ "$(cat "${out}/sermod_restart.rc")" != "0" ] || [ "$(cat "${out}/sermod_status.rc")" != "0" ]; then
	rollback
	case "$init" in
		systemd) capture rollback_restart systemctl restart sermod ;;
		openrc) capture rollback_restart rc-service sermod restart ;;
	esac
	finish 50
fi

ready_waited=0
while [ "$ready_waited" -lt 60 ]; do
	if http_get http://127.0.0.1:9797/livez?verbose >"${out}/livez.out" 2>"${out}/livez.err" &&
		http_get http://127.0.0.1:9797/readyz?verbose >"${out}/readyz.out" 2>"${out}/readyz.err"; then
		break
	fi
	ready_waited=$((ready_waited + 1))
	sleep 1
done
printf '%s\n' "$ready_waited" >"${out}/ready_waited_seconds"
if [ "$ready_waited" -eq 60 ]; then
	printf '1\n' >"${out}/livez.rc"
	printf '1\n' >"${out}/readyz.rc"
else
	printf '0\n' >"${out}/livez.rc"
	printf '0\n' >"${out}/readyz.rc"
fi
capture libraries /usr/bin/sermoctl --config /etc/sermo/sermo.yml libs
capture web_html http_get http://127.0.0.1:9797/
capture libraries_api http_get http://127.0.0.1:9797/api/libraries
if [ "$(cat "${out}/livez.rc")" != "0" ] || [ "$(cat "${out}/readyz.rc")" != "0" ] ||
	[ "$(cat "${out}/libraries.rc")" != "0" ] || [ "$(cat "${out}/web_html.rc")" != "0" ] ||
	[ "$(cat "${out}/libraries_api.rc")" != "0" ]; then
	rollback
	case "$init" in
		systemd) capture rollback_restart systemctl restart sermod ;;
		openrc) capture rollback_restart rc-service sermod restart ;;
	esac
	finish 60
fi

rm -rf "$previous"
finish 0
