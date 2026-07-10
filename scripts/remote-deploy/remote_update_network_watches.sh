#!/usr/bin/env bash
set -euo pipefail

run_id="${1:-}"
payload="${2:-}"

if [ -z "$run_id" ] || [ -z "$payload" ]; then
	echo "usage: $0 RUN_ID NETWORKS_TGZ" >&2
	exit 64
fi

work="/tmp/sermo-network-watches-${run_id}"
out="${work}/out"
stage="/etc/sermo/.networks-stage-${run_id}"
previous="/etc/sermo/.networks-previous-${run_id}"
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

rollback() {
	if [ -d "$previous" ]; then
		rm -rf /etc/sermo/networks
		mv "$previous" /etc/sermo/networks
	fi
}

finish() {
	rc="$1"
	snapshot_protected_paths "${out}/protected_paths.after"
	if ! diff -u "${out}/protected_paths.before" "${out}/protected_paths.after" >"${out}/protected_paths.diff"; then
		rc=70
	fi
	tar -C "$work" -czf "${work}/out.tar.gz" out >/dev/null 2>&1 || true
	exit "$rc"
}

if [ "$(id -u)" != "0" ]; then
	echo "remote update must run as root" >&2
	exit 10
fi
if [ ! -f /etc/sermo/sermo.yml ] || [ ! -d /etc/sermo/networks ] || [ ! -x /usr/bin/sermoctl ]; then
	echo "existing Sermo config, networks directory or sermoctl is missing" >&2
	exit 20
fi

snapshot_protected_paths "${out}/protected_paths.before"

while IFS= read -r member; do
	case "$member" in
		etc/sermo/networks | etc/sermo/networks/*) ;;
		*)
			echo "payload has unexpected member: $member" >&2
			finish 22
			;;
	esac
done < <(tar -tzf "$payload")

rm -rf "$stage" "$previous"
mkdir -p "$stage"
tar --no-same-owner -C "$stage" -xzf "$payload"
if [ ! -d "${stage}/etc/sermo/networks" ]; then
	echo "payload has no network watches" >&2
	finish 23
fi

mv /etc/sermo/networks "$previous"
mv "${stage}/etc/sermo/networks" /etc/sermo/networks
rmdir -p "${stage}/etc/sermo" 2>/dev/null || true

init="auto"
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
	init="systemd"
elif command -v rc-service >/dev/null 2>&1; then
	init="openrc"
fi
capture config_validate env SERMO_BACKEND="$init" SERMO_INIT="$init" /usr/bin/sermoctl --config /etc/sermo/sermo.yml config validate
if [ "$(cat "${out}/config_validate.rc")" != "0" ]; then
	rollback
	finish 40
fi

case "$init" in
	systemd) capture sermod_restart systemctl restart sermod ;;
	openrc) capture sermod_restart rc-service sermod restart ;;
	*)
		echo "unsupported init backend" >&2
		rollback
		finish 41
		;;
esac
if [ "$(cat "${out}/sermod_restart.rc")" != "0" ]; then
	rollback
	case "$init" in
		systemd) capture rollback_restart systemctl restart sermod ;;
		openrc) capture rollback_restart rc-service sermod restart ;;
	esac
	finish 50
fi

rm -rf "$previous"
finish 0
