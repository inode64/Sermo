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

if [ "$(id -u)" != "0" ]; then
	echo "remote repair must run as root" >&2
	exit 10
fi

hostname >"${out}/hostname" 2>/dev/null || true
rm -rf /usr/share/sermo/catalog
tar -C / -xzf "$payload" >"${out}/payload_extract.out" 2>"${out}/payload_extract.err"
printf '%s\n' "$?" >"${out}/payload_extract.rc"
/usr/bin/sermoctl --config /etc/sermo/sermo.yml config validate >"${out}/config_validate.out" 2>"${out}/config_validate.err"
printf '%s\n' "$?" >"${out}/config_validate.rc"
find /usr/share/sermo/catalog -maxdepth 2 -type f | sort >"${out}/catalog_files" 2>/dev/null || true
tar -C "$work" -czf "${work}/out.tar.gz" out >/dev/null 2>&1 || true
