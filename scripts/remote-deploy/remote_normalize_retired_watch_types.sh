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

remove_watch_file() {
	local file="$1" backup="$2" archive_root="$3" archive_member="$4" removed_files="$5"
	[ -f "$file" ] || return 0
	if [ ! -s "$removed_files" ]; then
		tar -C "$archive_root" -czf "$backup" "$archive_member"
	fi
	printf '%s\n' "$file" >>"$removed_files"
	rm -f "$file"
}

# remove_retired_watch_files removes retired documents and, when a base
# document is retired, its exact .local sibling. archive_root/archive_member
# keep the backup layout explicit so tests can exercise the same deletion path
# without touching /etc.
remove_retired_watch_files() {
	local config_root="$1" backup="$2" archive_root="$3" archive_member="$4" removed_files="$5"
	local dir base_layer local_layer file sibling
	: >"$removed_files"
	for dir in watches networks storages mounts; do
		base_layer="${config_root}/${dir}"
		local_layer="${config_root}/${dir}.local"
		if [ -d "$base_layer" ]; then
			while IFS= read -r -d '' file; do
				if matches_retired_type "$file"; then
					remove_watch_file "$file" "$backup" "$archive_root" "$archive_member" "$removed_files"
					sibling="${local_layer}/$(basename "$file")"
					remove_watch_file "$sibling" "$backup" "$archive_root" "$archive_member" "$removed_files"
				fi
			done < <(find "$base_layer" -maxdepth 1 -type f \( -name '*.yml' -o -name '*.yaml' \) -print0)
		fi
		[ -d "$local_layer" ] || continue
		while IFS= read -r -d '' file; do
			if matches_retired_type "$file"; then
				remove_watch_file "$file" "$backup" "$archive_root" "$archive_member" "$removed_files"
			fi
		done < <(find "$local_layer" -maxdepth 1 -type f \( -name '*.yml' -o -name '*.yaml' \) -print0)
	done
}

main() {
	local run_id="${1:-}" payload="${2:-}"
	local work out backup stage candidate removed
	if [ -z "$run_id" ] || [ -z "$payload" ]; then
		echo "usage: $0 RUN_ID PAYLOAD_TGZ" >&2
		return 64
	fi

	work="/tmp/sermo-update-${run_id}"
	out="${work}/normalize-out"
	backup="${work}/etc-sermo-before-watch-normalize.tgz"
	stage="${work}/stage"
	mkdir -p "$out" "$stage"

	if [ "$(id -u)" != "0" ]; then
		echo "remote normalization must run as root" >&2
		return 10
	fi
	if [ ! -d /etc/sermo ]; then
		echo "/etc/sermo is missing" >&2
		return 20
	fi

	# Stage only the candidate validator and its catalog; the full payload install
	# stays with remote_update_payload.sh.
	if ! tar --no-same-owner -C "$stage" -xzf "$payload" \
		candidate/sermoctl usr/share/sermo/catalog \
		>"${out}/stage.out" 2>"${out}/stage.err"; then
		echo "payload does not stage candidate/sermoctl and usr/share/sermo/catalog" >&2
		return 21
	fi
	candidate="${stage}/candidate/sermoctl"
	if [ ! -x "$candidate" ]; then
		echo "payload does not contain an executable candidate sermoctl" >&2
		return 22
	fi

	remove_retired_watch_files /etc/sermo "$backup" / etc/sermo "${out}/removed_files"
	removed="$(wc -l <"${out}/removed_files")"
	printf '%s\n' "$removed" >"${out}/removed_count"
	if [ "$removed" -eq 0 ]; then
		return 0
	fi

	if "$candidate" --config /etc/sermo/sermo.yml config validate \
		>"${out}/config_validate.out" 2>"${out}/config_validate.err"; then
		printf '0\n' >"${out}/config_validate.rc"
		return 0
	fi

	printf '1\n' >"${out}/config_validate.rc"
	tar --no-same-owner -C / -xzf "$backup"
	return 30
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
	main "$@"
fi
