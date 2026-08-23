#!/usr/bin/env bash
# Install additional dashboard credentials without exposing their values.
set -euo pipefail

source_file="${1:-}"
target="/etc/sermo/credentials.env"
protected_paths=(/ /etc /usr /usr/lib /etc/systemd /usr/lib/tmpfiles.d /etc/init.d /usr/share)
temporary_file=""

usage() {
	echo "usage: $0 STAGED_CREDENTIALS_FILE" >&2
	exit 64
}

snapshot_protected_paths() {
	local destination="$1"
	: >"$destination"
	for path in "${protected_paths[@]}"; do
		if [ -e "$path" ]; then
			stat -c '%n|%F|%a|%u|%g' "$path" >>"$destination" 2>/dev/null || printf '%s|stat-error\n' "$path" >>"$destination"
		else
			printf '%s|missing\n' "$path" >>"$destination"
		fi
	done
}

cleanup() {
	if [ -n "$temporary_file" ]; then
		rm -f -- "$temporary_file"
	fi
	rm -f -- "$source_file"
}

verify_staged_credentials() {
	local credential
	while IFS= read -r credential || [ -n "$credential" ]; do
		[ -n "$credential" ] || continue
		grep -Fxq -- "$credential" "$target"
	done <"$source_file"
}

[ "$#" -eq 1 ] || usage
[ "$(id -u)" = "0" ] || {
	echo "credential apply must run as root" >&2
	exit 10
}
[ -f "$source_file" ] && [ ! -L "$source_file" ] || {
	echo "staged credentials must be a regular file" >&2
	exit 11
}

before="$(mktemp /tmp/sermo-credentials-protected-before.XXXXXX)"
after="$(mktemp /tmp/sermo-credentials-protected-after.XXXXXX)"
trap 'cleanup; rm -f -- "$before" "$after"' EXIT
snapshot_protected_paths "$before"

if [ -e /etc/sermo ] && [ ! -d /etc/sermo ]; then
	echo "/etc/sermo exists but is not a directory" >&2
	exit 12
fi
if [ ! -d /etc/sermo ]; then
	mkdir /etc/sermo
fi
if [ -L "$target" ]; then
	echo "credentials target must not be a symlink" >&2
	exit 13
fi
if [ -e "$target" ] && [ ! -f "$target" ]; then
	echo "credentials target exists but is not a regular file" >&2
	exit 14
fi

had_existing=0
if [ -f "$target" ]; then
	had_existing=1
fi
temporary_file="$(mktemp /etc/sermo/.credentials.env.XXXXXX)"
if [ "$had_existing" = "1" ]; then
	awk 'NF && !seen[$0]++ { print }' "$target" "$source_file" >"$temporary_file"
else
	awk 'NF && !seen[$0]++ { print }' "$source_file" >"$temporary_file"
fi
[ -s "$temporary_file" ] || {
	echo "credentials would be empty" >&2
	exit 15
}

chown root:root "$temporary_file"
chmod 0600 "$temporary_file"
mv -f -- "$temporary_file" "$target"
temporary_file=""
verify_staged_credentials || {
	echo "a staged credential is missing after install" >&2
	exit 16
}
[ "$(stat -c '%a' "$target")" = "600" ] || {
	echo "credentials mode is not 0600" >&2
	exit 17
}
[ "$(stat -c '%U:%G' "$target")" = "root:root" ] || {
	echo "credentials owner is not root:root" >&2
	exit 18
}

snapshot_protected_paths "$after"
if ! diff -u "$before" "$after" >/dev/null; then
	echo "protected path metadata changed" >&2
	exit 70
fi

printf 'status=ok existing=%s credentials=%s\n' "$had_existing" "$(wc -l <"$target")"
