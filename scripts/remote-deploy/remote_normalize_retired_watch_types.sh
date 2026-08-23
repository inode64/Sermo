#!/usr/bin/env bash
# Remove only host-watch files whose check.type was retired from Sermo
# (`autofs`, removed by 8bde4e5b, and `entropy`, removed by d2e88055) from an
# already installed /etc/sermo, so a host configured before those removals
# validates against the current binary. Companion to
# remote_normalize_retired_engine_keys.sh.
#
# It stages the update payload's candidate sermoctl and catalog under the same
# /tmp/sermo-update-<run-id>/stage path remote_update_payload.sh uses (the
# candidate's compiled-in catalog path), snapshots /etc/sermo, deletes only
# whole watch documents whose check block declares a retired type, validates
# with the candidate sermoctl and restores the previous /etc/sermo when
# validation fails.
set -euo pipefail

retired_types="autofs entropy"

run_id="${1:-}"
payload="${2:-}"

if [ -z "$run_id" ] || [ -z "$payload" ]; then
	echo "usage: $0 RUN_ID PAYLOAD_TGZ" >&2
	exit 64
fi

work="/tmp/sermo-update-${run_id}"
out="${work}/normalize-out"
backup="${work}/etc-sermo-before-watch-normalize.tgz"
stage="${work}/stage"
mkdir -p "$out" "$stage"

if [ "$(id -u)" != "0" ]; then
	echo "remote normalization must run as root" >&2
	exit 10
fi
if [ ! -d /etc/sermo ]; then
	echo "/etc/sermo is missing" >&2
	exit 20
fi

# Stage only the candidate validator and its catalog; the full payload install
# stays with remote_update_payload.sh.
if ! tar --no-same-owner -C "$stage" -xzf "$payload" \
	candidate/sermoctl usr/share/sermo/catalog \
	>"${out}/stage.out" 2>"${out}/stage.err"; then
	echo "payload does not stage candidate/sermoctl and usr/share/sermo/catalog" >&2
	exit 21
fi
candidate="${stage}/candidate/sermoctl"
if [ ! -x "$candidate" ]; then
	echo "payload does not contain an executable candidate sermoctl" >&2
	exit 22
fi

# A retired watch document is removed whole; its .local sibling override, when
# present, is removed with it so the override cannot survive as an orphan.
matches_retired_type() {
	awk -v types="$retired_types" '
	BEGIN {
		split(types, list, " ")
		in_check = 0
	}
	/^[[:space:]]*($|#)/ { next }
	{
		line = $0
		prefix = line
		sub(/[^[:space:]].*$/, "", prefix)
		level = length(prefix)
		if (in_check && level == 0) {
			in_check = 0
		}
		if (line ~ /^check:[[:space:]]*$/) {
			in_check = 1
			next
		}
		if (!in_check) {
			next
		}
		if (line !~ /^[[:space:]]+type:/) {
			next
		}
		value = line
		sub(/^[[:space:]]+type:[[:space:]]*/, "", value)
		gsub(/["'\''[:space:]]/, "", value)
		for (i in list) {
			if (value == list[i]) {
				exit 42
			}
		}
	}
	' "$1" && return 1
	[ "$?" -eq 42 ]
}

removed=0
: >"${out}/removed_files"
for dir in watches networks storages mounts; do
	for layer in "/etc/sermo/${dir}" "/etc/sermo/${dir}.local"; do
		[ -d "$layer" ] || continue
		while IFS= read -r -d '' file; do
			if matches_retired_type "$file"; then
				if [ "$removed" -eq 0 ]; then
					tar -C / -czf "$backup" etc/sermo
				fi
				printf '%s\n' "$file" >>"${out}/removed_files"
				rm -f "$file"
				removed=$((removed + 1))
			fi
		done < <(find "$layer" -maxdepth 1 -type f \( -name '*.yml' -o -name '*.yaml' \) -print0)
	done
done

printf '%s\n' "$removed" >"${out}/removed_count"
if [ "$removed" -eq 0 ]; then
	exit 0
fi

if "$candidate" --config /etc/sermo/sermo.yml config validate \
	>"${out}/config_validate.out" 2>"${out}/config_validate.err"; then
	printf '0\n' >"${out}/config_validate.rc"
	exit 0
fi

printf '1\n' >"${out}/config_validate.rc"
tar --no-same-owner -C / -xzf "$backup"
exit 30
