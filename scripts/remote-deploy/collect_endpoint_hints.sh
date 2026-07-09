#!/usr/bin/env bash
set -u

# Print sanitized endpoint hints for catalog services whose default probes are
# often bound to a host-specific address. The output is intentionally narrow:
# only listen addresses, process owners, cloudflared metrics endpoints and
# matching sockets.

emit_process_hint() {
	process="$1"
	for pid in /proc/[0-9]*; do
		[ -r "${pid}/comm" ] || continue
		[ "$(cat "${pid}/comm" 2>/dev/null)" = "$process" ] || continue
		uid="$(awk '/^Uid:/ { print $2; exit }' "${pid}/status" 2>/dev/null || true)"
		[ -n "$uid" ] || continue
		user="$(getent passwd "$uid" 2>/dev/null | cut -d: -f1 || true)"
		[ -n "$user" ] || user="$uid"
		exe="$(readlink -f "${pid}/exe" 2>/dev/null || true)"
		[ -n "$exe" ] || exe="unknown"
		printf 'process %s user %s exe %s\n' "$process" "$user" "$exe"
		return 0
	done
}

emit_process_hint cloudflared
emit_process_hint named
emit_process_hint mysqld_exporter

if [ -r /etc/cloudflared/config.yml ]; then
	grep -nE "^[[:space:]]*metrics[[:space:]]*:" /etc/cloudflared/config.yml 2>/dev/null \
		| sed 's#^#cloudflared.metrics /etc/cloudflared/config.yml:#' || true
fi

for path in \
	/etc/conf.d/mysqld_exporter \
	/etc/default/mysqld_exporter \
	/etc/default/prometheus-mysqld-exporter \
	/etc/sysconfig/mysqld_exporter \
	/etc/systemd/system/mysqld_exporter.service \
	/usr/lib/systemd/system/mysqld_exporter.service \
	/lib/systemd/system/mysqld_exporter.service; do
	if [ -r "$path" ]; then
		grep -Eho -- '--web\.listen-address(=|[[:space:]]+)[^[:space:]"]+' "$path" 2>/dev/null \
			| sed "s#^#mysqld_exporter.listen ${path}:#" || true
	fi
done

for pid in /proc/[0-9]*; do
	[ -r "${pid}/comm" ] || continue
	[ "$(cat "${pid}/comm" 2>/dev/null)" = "mysqld_exporter" ] || continue
	tr '\0' ' ' <"${pid}/cmdline" 2>/dev/null \
		| grep -Eho -- '--web\.listen-address(=|[[:space:]]+)[^[:space:]"]+' \
		| sed "s#^#mysqld_exporter.listen ${pid}/cmdline:#" || true
done

for path in /etc/named.conf /etc/bind/named.conf /etc/bind/named.conf.options /etc/bind/named.conf.local /etc/bind/named.conf.auth; do
	if [ -r "$path" ]; then
		grep -nE "^[[:space:]]*listen-on(-v6)?([[:space:]]+port[[:space:]]+[0-9]+)?[[:space:]]" "$path" 2>/dev/null \
			| sed "s#^#named.listen ${path}:#" || true
	fi
done

if command -v ss >/dev/null 2>&1; then
	ss -H -ltnup 2>/dev/null | grep -E 'users:\(\("(cloudflared|mysqld_exporter|named)"' \
		| sed 's#^#socket #'
fi
