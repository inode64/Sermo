#!/usr/bin/env bash
# Shared, read-only inventory helpers. This file is uploaded next to every
# collector that sources it, so remote_stage.sh and remote_collect_inventory.sh
# stay self-contained as deployment payloads.

readonly inventory_epmd_process_name="epmd"

# emit_epmd_process_hints writes one ownership hint for every live EPMD process.
# RabbitMQ can own EPMD itself, so preserving every owner lets the local generator
# avoid creating a competing OpenRC epmd control target.
emit_epmd_process_hints() {
	local pid uid user exe
	for pid in /proc/[0-9]*; do
		[ -r "${pid}/comm" ] || continue
		[ "$(cat "${pid}/comm" 2>/dev/null)" = "$inventory_epmd_process_name" ] || continue
		uid="$(awk '/^Uid:/ { print $2; exit }' "${pid}/status" 2>/dev/null || true)"
		[ -n "$uid" ] || continue
		user="$(getent passwd "$uid" 2>/dev/null | cut -d: -f1 || true)"
		[ -n "$user" ] || user="$uid"
		exe="$(readlink -f "${pid}/exe" 2>/dev/null || true)"
		[ -n "$exe" ] || exe="unknown"
		printf 'process %s user %s exe %s\n' "$inventory_epmd_process_name" "$user" "$exe"
	done
}
