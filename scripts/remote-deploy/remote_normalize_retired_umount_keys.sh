#!/usr/bin/env bash
set -euo pipefail

run_id="${1:-}"
payload="${2:-}"

if [ -z "$run_id" ] || [ -z "$payload" ]; then
	echo "usage: $0 RUN_ID PAYLOAD_TGZ" >&2
	exit 64
fi

work="/tmp/sermo-normalize-${run_id}"
out="${work}/out"
rendered="${work}/rendered"
backup="${work}/etc-sermo-before.tgz"
mkdir -p "$out" "$rendered"

if [ "$(id -u)" != "0" ]; then
	echo "remote normalization must run as root" >&2
	exit 10
fi
if [ ! -d /etc/sermo ]; then
	echo "/etc/sermo is missing" >&2
	exit 20
fi

while IFS= read -r member; do
	case "$member" in
		sermoctl) ;;
		*)
			echo "payload has unexpected member: $member" >&2
			exit 21
			;;
	esac
done < <(tar -tzf "$payload")

tar --no-same-owner -C "$work" -xzf "$payload"
if [ ! -x "${work}/sermoctl" ]; then
	echo "payload does not contain sermoctl" >&2
	exit 22
fi

changed=0
while IFS= read -r -d '' file; do
	relative="${file#/etc/sermo/}"
	target="${rendered}/${relative}"
	mkdir -p "$(dirname "$target")"
	cp --attributes-only --preserve=mode,ownership,timestamps "$file" "$target"
	if awk '
	function indent(line, prefix) {
		prefix = line
		sub(/[^[:space:]].*$/, "", prefix)
		return length(prefix)
	}
	function fail(line) {
		print FILENAME ": retired mount.umount key must be false: " line > "/dev/stderr"
		invalid = 1
	}
	{
		line = $0
		if (line ~ /^[[:space:]]*$/ || line ~ /^[[:space:]]*#/) {
			print line
			next
		}
		level = indent(line)
		if (umount_level >= 0 && level <= umount_level) {
			umount_level = -1
		}
		if (mount_level >= 0 && level <= mount_level) {
			mount_level = -1
			umount_level = -1
		}
		if (line ~ /^[[:space:]]*mount:[[:space:]]*$/) {
			mount_level = level
			print line
			next
		}
		if (mount_level >= 0 && line ~ /^[[:space:]]*umount:[[:space:]]*$/) {
			umount_level = level
			print line
			next
		}
		if (umount_level >= 0 && line ~ /^[[:space:]]*allow_(lazy|sigkill):/) {
			value = line
			sub(/^[^:]*:[[:space:]]*/, "", value)
			if (value != "false") {
				fail(line)
				print line
				next
			}
			changed = 1
			next
		}
		print line
	}
	END {
		if (invalid) {
			exit 2
		}
		if (changed) {
			exit 42
		}
	}
	' "$file" >"$target"; then
		continue
	else
		awk_status=$?
	fi
	if [ "$awk_status" -ne 42 ]; then
		exit "$awk_status"
	fi
	changed=$((changed + 1))
	cleaned="${target}.cleaned"
	cp --attributes-only --preserve=mode,ownership,timestamps "$target" "$cleaned"
	awk '
	function indent(line, prefix) {
		prefix = line
		sub(/[^[:space:]].*$/, "", prefix)
		return length(prefix)
	}
	function flush_pending() {
		if (pending != "") {
			print pending
			printf "%s", pending_gap
			pending = ""
			pending_gap = ""
		}
	}
	{
		line = $0
		if (pending != "") {
			if (line ~ /^[[:space:]]*$/ || line ~ /^[[:space:]]*#/) {
				pending_gap = pending_gap line ORS
				next
			}
			if (indent(line) > pending_level) {
				flush_pending()
			} else {
				pending = ""
				pending_gap = ""
			}
		}
		if (line ~ /^[[:space:]]*umount:[[:space:]]*$/) {
			pending = line
			pending_level = indent(line)
			next
		}
		print line
	}
	' "$target" >"$cleaned"
	mv "$cleaned" "$target"
done < <(find /etc/sermo -type f \( -name '*.yml' -o -name '*.yaml' \) -print0)

printf '%s\n' "$changed" >"${out}/changed_files"
if [ "$changed" -eq 0 ]; then
	exit 0
fi

tar -C / -czf "$backup" etc/sermo
while IFS= read -r -d '' file; do
	relative="${file#"${rendered}"/}"
	cp --preserve=mode,ownership,timestamps "$file" "/etc/sermo/${relative}"
done < <(find "$rendered" -type f -print0)

if "${work}/sermoctl" --config /etc/sermo/sermo.yml config validate >"${out}/config_validate.out" 2>"${out}/config_validate.err"; then
	printf '0\n' >"${out}/config_validate.rc"
	exit 0
fi

printf '1\n' >"${out}/config_validate.rc"
tar --no-same-owner -C / -xzf "$backup"
exit 30
