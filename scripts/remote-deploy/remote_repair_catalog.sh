#!/usr/bin/env bash
set -u

run_id="${1:-}"
payload="${2:-}"

if [ -z "$run_id" ] || [ -z "$payload" ]; then
	echo "usage: $0 RUN_ID PAYLOAD_TGZ" >&2
	exit 64
fi

work="/tmp/sermo-repair-${run_id}"
out="${work}/out"
mkdir -p "$out"

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
	if ! verify_protected_paths; then
		rc=70
	fi
	tar -C "$work" -czf "${work}/out.tar.gz" out >/dev/null 2>&1 || true
	exit "$rc"
}

if [ "$(id -u)" != "0" ]; then
	echo "remote repair must run as root" >&2
	exit 10
fi

snapshot_protected_paths "${out}/protected_path_metadata.before"

hostname >"${out}/hostname" 2>/dev/null || true
rm -rf /usr/share/sermo/catalog
tar --no-same-owner -C / -xzf "$payload" usr/share/sermo/catalog >"${out}/payload_extract.out" 2>"${out}/payload_extract.err"
extract_rc=$?
printf '%s\n' "$extract_rc" >"${out}/payload_extract.rc"
if [ "$extract_rc" -ne 0 ]; then
	finish 20
fi
/usr/bin/sermoctl --config /etc/sermo/sermo.yml config validate >"${out}/config_validate.out" 2>"${out}/config_validate.err"
printf '%s\n' "$?" >"${out}/config_validate.rc"
find /usr/share/sermo/catalog -maxdepth 2 -type f | sort >"${out}/catalog_files" 2>/dev/null || true
finish 0
