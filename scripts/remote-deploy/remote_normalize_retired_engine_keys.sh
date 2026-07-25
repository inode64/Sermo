#!/usr/bin/env bash
# Remove only the retired global engine key `engine.max_parallel_operations`
# from an already installed /etc/sermo, so a host configured before commit
# c8eb23b3 ("agent: remove global operation semaphore") validates against the
# current binary. Companion to remote_normalize_retired_umount_keys.sh.
#
# It snapshots /etc/sermo, edits only that key inside a top-level `engine:`
# block, validates with the candidate sermoctl from the payload and restores
# the previous /etc/sermo when validation fails.
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
	BEGIN { engine_level = -1 }
	{
		line = $0
		if (line ~ /^[[:space:]]*$/ || line ~ /^[[:space:]]*#/) {
			print line
			next
		}
		level = indent(line)
		if (engine_level >= 0 && level <= engine_level) {
			engine_level = -1
		}
		if (line ~ /^engine:[[:space:]]*$/) {
			engine_level = level
			print line
			next
		}
		if (engine_level >= 0 && line ~ /^[[:space:]]*max_parallel_operations:/) {
			changed = 1
			next
		}
		print line
	}
	END {
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
