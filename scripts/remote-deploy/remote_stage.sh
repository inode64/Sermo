#!/usr/bin/env bash
set -u

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck disable=SC1091 # uploaded alongside this script by install_fleet.sh
. "${script_dir}/remote_inventory_common.sh"

# Machine-stable tool output. libvirt (and other gettext-aware tools) translate
# their output, so a Spanish host reports a running domain as "ejecutando" and
# every consumer that matches the English word silently treats it as stopped.
# Pin the locale for everything this collector parses.
export LC_ALL=C
export LANG=C
export LANGUAGE=

run_id="${1:-}"
payload="${2:-}"

if [ -z "$run_id" ] || [ -z "$payload" ]; then
	echo "usage: $0 RUN_ID PAYLOAD_TGZ" >&2
	exit 64
fi

work="/tmp/sermo-install-${run_id}"
out="${work}/out"
mkdir -p "$out"

log() {
	printf '%s\n' "$*" | tee -a "${out}/stage.log" >/dev/null
}

capture() {
	name="$1"
	shift
	"$@" >"${out}/${name}.out" 2>"${out}/${name}.err"
	printf '%s\n' "$?" >"${out}/${name}.rc"
}

resolve_address() {
	# First address for a host name, IPv4 preferred. The generator matches these
	# against the host's own staged addresses, and both the NFS sources and the
	# Gluster thin arbiter are usually named only inside the storage network.
	local resolved
	resolved="$(getent ahostsv4 "$1" 2>/dev/null | awk 'NR == 1 { print $1 }')"
	if [ -z "$resolved" ]; then
		resolved="$(getent ahostsv6 "$1" 2>/dev/null | awk 'NR == 1 { print $1 }')"
	fi
	printf '%s' "$resolved"
}

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
		log "protected path metadata changed"
		rc=70
	fi
	tar -C "$work" -czf "${work}/out.tar.gz" out >/dev/null 2>&1 || true
	exit "$rc"
}

if [ "$(id -u)" != "0" ]; then
	echo "remote installer must run as root" >&2
	exit 10
fi

snapshot_protected_paths "${out}/protected_path_metadata.before"

hostname -f >"${out}/hostname_fqdn" 2>/dev/null || hostname >"${out}/hostname_fqdn" 2>/dev/null || true
hostname >"${out}/hostname" 2>/dev/null || true
uname -a >"${out}/uname" 2>/dev/null || true
if [ -r /etc/os-release ]; then
	cp /etc/os-release "${out}/os-release" || true
fi

init="unknown"
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
	init="systemd"
elif command -v rc-service >/dev/null 2>&1; then
	init="openrc"
fi
printf '%s\n' "$init" >"${out}/init"
config_backend="$init"
case "$config_backend" in
	systemd | openrc) ;;
	*) config_backend="auto" ;;
esac

backup=""
if [ -e /etc/sermo ]; then
	backup="/etc/sermo.backup.${run_id}"
	if [ -e "$backup" ]; then
		backup="/etc/sermo.backup.${run_id}.$(date +%s)"
	fi
	mv /etc/sermo "$backup"
fi
printf '%s\n' "$backup" >"${out}/backup_path"

payload_members="usr/bin/sermoctl usr/bin/sermod usr/share/sermo/catalog etc/sermo/templates/default-alert.yml"
: >"${out}/payload_skipped_members"
if [ "$init" = "systemd" ]; then
	if [ -d /etc/systemd/system ]; then
		payload_members="${payload_members} etc/systemd/system/sermod.service"
	else
		printf '%s\n' "etc/systemd/system/sermod.service: /etc/systemd/system missing" >>"${out}/payload_skipped_members"
	fi
	if [ -d /usr/lib/tmpfiles.d ]; then
		payload_members="${payload_members} usr/lib/tmpfiles.d/sermo.conf"
	else
		printf '%s\n' "usr/lib/tmpfiles.d/sermo.conf: /usr/lib/tmpfiles.d missing" >>"${out}/payload_skipped_members"
	fi
elif [ "$init" = "openrc" ]; then
	if [ -d /etc/init.d ]; then
		payload_members="${payload_members} etc/init.d/sermod"
	else
		printf '%s\n' "etc/init.d/sermod: /etc/init.d missing" >>"${out}/payload_skipped_members"
	fi
fi
printf '%s\n' "$payload_members" >"${out}/payload_members"

rm -rf /usr/share/sermo/catalog
read -r -a _payload_members <<< "$payload_members"
tar --no-same-owner -C / -xzf "$payload" "${_payload_members[@]}" >"${out}/payload_extract.out" 2>"${out}/payload_extract.err"
extract_rc=$?
printf '%s\n' "$extract_rc" >"${out}/payload_extract.rc"
if [ "$extract_rc" -ne 0 ]; then
	log "payload extraction failed"
	finish 20
fi

mkdir -p /etc/sermo/services /etc/sermo/apps /etc/sermo/notifiers /etc/sermo/watches /etc/sermo/networks /etc/sermo/storages /etc/sermo/mounts /etc/sermo/templates
mkdir -p /etc/sermo/services.local /etc/sermo/apps.local /etc/sermo/notifiers.local /etc/sermo/watches.local /etc/sermo/networks.local /etc/sermo/storages.local /etc/sermo/mounts.local /etc/sermo/templates.local
mkdir -p /run/sermo /var/lib/sermo

if [ -n "$backup" ] && [ -f "${backup}/credentials.env" ]; then
	install -o 0 -g 0 -m 0600 "${backup}/credentials.env" /etc/sermo/credentials.env
	printf 'yes\n' >"${out}/credentials_preserved"
else
	printf 'no\n' >"${out}/credentials_preserved"
fi

# A reinstall moves the whole tree aside, so unlike remote_apply.sh it has to
# restore the per-host layer explicitly. `templates` is restored for the same
# reason: an operator's notification template is not generated content either.
local_preserved=no
if [ -n "$backup" ]; then
	for dir in /etc/sermo/services.local /etc/sermo/apps.local /etc/sermo/notifiers.local /etc/sermo/watches.local /etc/sermo/networks.local /etc/sermo/storages.local /etc/sermo/mounts.local /etc/sermo/templates.local /etc/sermo/templates; do
		src="${backup}/$(basename "$dir")"
		[ -d "$src" ] || continue
		if find "$src" -mindepth 1 -print -quit | grep -q .; then
			cp -a "${src}/." "$dir/" && local_preserved=yes
		fi
	done
fi
printf '%s\n' "$local_preserved" >"${out}/local_overrides_preserved"

cat >/etc/sermo/sermo.yml <<YAML
engine:
  backend: ${config_backend}
  interval: 30s
  max_parallel_checks: 8
  default_timeout: 10s
  operation_timeout: 90s
  startup_delay: 0
  user_lookup: auto
  user_lookup_timeout: 250ms

paths:
  services:
    - /etc/sermo/services
  apps:
    - /etc/sermo/apps
  notifiers:
    - /etc/sermo/notifiers
  watches:
    - /etc/sermo/watches
    - /etc/sermo/networks
    - /etc/sermo/storages
    - /etc/sermo/mounts
  runtime: /run/sermo
  state: /var/lib/sermo
  templates: /etc/sermo/templates

defaults:
  dry_run: true
  stop_policy:
    graceful_timeout: 30s
    term_timeout: 15s
    kill_timeout: 5s
    force_kill: auto
  policy:
    cooldown: 5m

web:
  address: 0.0.0.0
  port: 9797
  password: "sermo-remote-admin"
YAML

if command -v systemd-tmpfiles >/dev/null 2>&1 && [ -f /usr/lib/tmpfiles.d/sermo.conf ]; then
	capture systemd_tmpfiles systemd-tmpfiles --create /usr/lib/tmpfiles.d/sermo.conf
fi

if command -v ss >/dev/null 2>&1; then
	ss -ltnp 'sport = :9797' >"${out}/port9797_before" 2>&1 || true
elif command -v netstat >/dev/null 2>&1; then
	netstat -ltnp >"${out}/port9797_before" 2>&1 || true
fi

if [ "$init" = "systemd" ]; then
	systemctl daemon-reload >"${out}/systemctl_daemon_reload.out" 2>"${out}/systemctl_daemon_reload.err" || true
	systemctl list-units --type=service --state=active --no-legend --plain --no-pager >"${out}/active_units" 2>/dev/null || true
	# A failed unit is installed, enabled and broken: exactly what monitoring is
	# for. OpenRC's crashed state comes from openrc_status_all below.
	systemctl list-units --type=service --state=failed --no-legend --plain --no-pager >"${out}/failed_units" 2>/dev/null || true
	systemctl list-unit-files --type=service --no-legend --no-pager >"${out}/unit_files" 2>/dev/null || true
	systemctl status sermod --no-pager >"${out}/sermod_status_before" 2>&1 || true
elif [ "$init" = "openrc" ]; then
	rc-status --servicelist >"${out}/openrc_services" 2>/dev/null || true
	rc-status --all >"${out}/openrc_status_all" 2>/dev/null || true
	rc-service sermod status >"${out}/sermod_status_before" 2>&1 || true
fi

capture sermoctl_version /usr/bin/sermoctl --version
capture sermod_version /usr/bin/sermod --version
capture config_validate_base env SERMO_BACKEND="$config_backend" SERMO_INIT="$config_backend" /usr/bin/sermoctl --config /etc/sermo/sermo.yml config validate
capture services_json env SERMO_BACKEND="$config_backend" SERMO_INIT="$config_backend" /usr/bin/sermoctl --config /etc/sermo/sermo.yml --json services
capture services_all_json env SERMO_BACKEND="$config_backend" SERMO_INIT="$config_backend" /usr/bin/sermoctl --config /etc/sermo/sermo.yml --json services all

# Gluster's local CLI carries the authenticated management RPC. Its XML status
# commands are read-only and let the generator build an explicit expected
# cluster topology without scraping locale-dependent terminal tables.
if command -v gluster >/dev/null 2>&1; then
	capture gluster_peer_status gluster --mode=script --xml peer status
	capture gluster_volume_info gluster --mode=script --xml volume info
	capture gluster_volume_status gluster --mode=script --xml volume status
	# The thin arbiter of a replica 2 volume is a daemon of its own, and neither
	# `volume status` nor `volume info --xml` reports it — the declaration exists
	# only in the text output. Its host is resolved here because that name is
	# usually internal to the storage network and does not resolve from the
	# workstation that generates the configuration. Keep in step across the
	# collectors.
	capture gluster_volume_info_text gluster --mode=script volume info
	: >"${out}/gluster_thin_arbiters"
	awk '
		/^[[:space:]]*Volume Name:[[:space:]]/ { volume = $3 }
		/^[[:space:]]*Thin-arbiter-path:[[:space:]]/ { print volume "\t" $2 }
	' "${out}/gluster_volume_info_text.out" >"${out}/gluster_thin_arbiters.raw" 2>/dev/null || true
	while IFS="$(printf '\t')" read -r volume arbiter; do
		if [ -z "$volume" ] || [ -z "$arbiter" ]; then continue; fi
		arbiter_host="${arbiter%%:*}"
		arbiter_path="${arbiter#*:}"
		arbiter_address="$(resolve_address "$arbiter_host")"
		printf '%s\t%s\t%s\t%s\n' "$volume" "$arbiter_host" "$arbiter_path" "$arbiter_address" >>"${out}/gluster_thin_arbiters"
	done <"${out}/gluster_thin_arbiters.raw"
	rm -f "${out}/gluster_thin_arbiters.raw"
fi

findmnt -R -J >"${out}/findmnt.json" 2>/dev/null || true
findmnt -R -P >"${out}/findmnt.pairs" 2>/dev/null || true
mount >"${out}/mount" 2>/dev/null || true
if [ -r /proc/mounts ]; then
	cp /proc/mounts "${out}/proc_mounts" || true
fi
if [ -r /proc/swaps ]; then
	cp /proc/swaps "${out}/proc_swaps" || true
fi
if [ -r /proc/mdstat ]; then
	cp /proc/mdstat "${out}/proc_mdstat" || true
fi
if command -v lvs >/dev/null 2>&1; then
	lvs --reportformat json --units b --nosuffix -o vg_name,lv_name,lv_attr,lv_health_status,vg_free,vg_size,data_percent,metadata_percent >"${out}/lvs.json" 2>"${out}/lvs.err" || true
fi
if [ -r /etc/fstab ]; then
	cp /etc/fstab "${out}/fstab" || true
	: >"${out}/nfs_routes"
	while IFS= read -r source; do
		case "$source" in
			\[*\]:/*)
				host="${source#\[}"
				host="${host%%\]:/*}"
				;;
			*:/*) host="${source%%:/*}" ;;
			*) continue ;;
		esac
		address="$(resolve_address "$host")"
		iface=""
		if [ -n "$address" ]; then
			iface="$(ip route get "$address" 2>/dev/null | awk '{ for (i = 1; i < NF; i++) if ($i == "dev") { print $(i + 1); exit } }')"
		fi
		printf '%s\t%s\t%s\n' "$host" "$address" "$iface" >>"${out}/nfs_routes"
	done < <(awk '$1 !~ /^#/ && ($3 == "nfs" || $3 == "nfs4") { print $1 }' /etc/fstab)
fi

# Execution-policy evidence is deliberately limited to real identity data: PID,
# real UID/user and resolved executable state. Command lines often contain
# credentials, so they stay out of the deployment archive; the daemon evaluates
# an optional policy cmd constraint locally and never publishes it either.
{
	for pid in /proc/[0-9]*; do
		[ -r "${pid}/status" ] || continue
		uid="$(awk '/^Uid:/ { print $2; exit }' "${pid}/status" 2>/dev/null || true)"
		[ -n "$uid" ] || continue
		user="$(getent passwd "$uid" 2>/dev/null | awk -F: 'NR == 1 { print $1 }')"
		[ -n "$user" ] || user="$uid"
		raw_exe="$(readlink "${pid}/exe" 2>/dev/null || true)"
		exe=""
		exe_previous=""
		case "$raw_exe" in
			*" (deleted)")
				state="deleted"
				exe_previous="${raw_exe% (deleted)}"
				;;
			/*)
				exe="$(readlink -f "${pid}/exe" 2>/dev/null || true)"
				if [ -n "$exe" ]; then state="resolved"; else state="unresolved"; fi
				;;
			*) state="unresolved" ;;
		esac
		printf '%s\t%s\t%s\t%s\t%s\t%s\n' "${pid#/proc/}" "$uid" "$user" "$state" "$exe" "$exe_previous"
	done
} | sort -t "$(printf '\t')" -k3,3 -k1,1n >"${out}/process_policy.tsv"

# Exim hints-database compatibility, one line per hints file Sermo's tidy
# watches query. A valid SQLite header is insufficient: the watch also requires
# Exim's tblblob table. Query sqlite_schema read-only so the generated watch is
# enabled only when the exact prerequisite exists.
: >"${out}/exim_hints"
for hints_db in /var/spool/exim/db/callout /var/spool/exim/db/retry; do
	if [ ! -f "$hints_db" ]; then
		printf '%s\t%s\n' "$hints_db" "absent" >>"${out}/exim_hints"
		continue
	fi
	# "SQLite format 3" is the 15-byte magic every SQLite file opens with. Read
	# it as hex: Bash command substitutions discard NUL bytes from other database
	# formats and would otherwise warn while the collector is only observing.
	if ! magic_hex="$(LC_ALL=C od -An -N 15 -tx1 "$hints_db" 2>/dev/null)"; then
		printf '%s\t%s\n' "$hints_db" "unknown" >>"${out}/exim_hints"
	elif [ "${magic_hex//[[:space:]]/}" = "53514c69746520666f726d61742033" ]; then
		if ! command -v sqlite3 >/dev/null 2>&1; then
			backend="unknown"
		elif ! schema_result="$(sqlite3 -readonly "$hints_db" \
			"SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = 'tblblob' LIMIT 1;" 2>/dev/null)"; then
			backend="unknown"
		elif [ "$schema_result" = "1" ]; then
			backend="sqlite"
		else
			backend="sqlite-no-tblblob"
		fi
		printf '%s\t%s\n' "$hints_db" "$backend" >>"${out}/exim_hints"
	else
		printf '%s\t%s\n' "$hints_db" "other" >>"${out}/exim_hints"
	fi
done

lsblk -J -O >"${out}/lsblk.json" 2>/dev/null || true
lsblk -P -o NAME,KNAME,PATH,TYPE,FSTYPE,MOUNTPOINTS,RM,RO,TRAN,MODEL,SERIAL,SIZE,PKNAME >"${out}/lsblk.pairs" 2>/dev/null || true

ip -o link show >"${out}/ip_link" 2>/dev/null || true
ip -o -4 addr show scope global >"${out}/ip_addr4" 2>/dev/null || true
ip -o -6 addr show scope global >"${out}/ip_addr6" 2>/dev/null || true
ip -o -4 route show >"${out}/ip_route4" 2>/dev/null || true
ip -o -6 route show >"${out}/ip_route6" 2>/dev/null || true

{
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
	emit_epmd_process_hints

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
		# Endpoint selection remains restricted by active catalog service and
		# process ownership in generate_install_config.py.
		ss -H -ltnup 2>/dev/null | sed 's#^#socket #'
	fi
} >"${out}/service_endpoint_hints" 2>/dev/null || true

# One line per OpenVPN instance config: <name> <client|server> <port-or-dash>.
# generate_install_config.py uses this to drop the local endpoint probe on
# client instances, which dial out and never listen locally.
{
	for conf in /etc/openvpn/*.conf; do
		[ -f "$conf" ] || continue
		inst="$(basename "$conf" .conf)"
		mode="server"
		if grep -Eq '^[[:space:]]*client([[:space:]]|$)' "$conf"; then
			mode="client"
		fi
		port="$(grep -E '^[[:space:]]*l?port[[:space:]]+[0-9]+' "$conf" | head -n1 | grep -oE '[0-9]+' | head -n1)"
		printf '%s\t%s\t%s\n' "$inst" "$mode" "${port:--}"
	done
} >"${out}/openvpn_instances" 2>/dev/null || true

# Newest ClamAV signature database, when one exists: sermo watches its age so
# stale signatures alert no matter how updates run (freshclam daemon, cron,
# fangfrisch).
{
	for db in /var/lib/clamav/daily.cld /var/lib/clamav/daily.cvd; do
		if [ -f "$db" ]; then
			printf '%s\n' "$db"
			break
		fi
	done
} >"${out}/clamav_database" 2>/dev/null || true

{
	command -v hdparm >/dev/null 2>&1 && echo "hdparm=1" || echo "hdparm=0"
	command -v smartctl >/dev/null 2>&1 && echo "smartctl=1" || echo "smartctl=0"
	command -v sensors >/dev/null 2>&1 && echo "sensors=1" || echo "sensors=0"
	command -v mdadm >/dev/null 2>&1 && echo "mdadm=1" || echo "mdadm=0"
	command -v curl >/dev/null 2>&1 && echo "curl=1" || echo "curl=0"
	command -v wget >/dev/null 2>&1 && echo "wget=1" || echo "wget=0"
	[ -r /proc/pressure/memory ] && echo "pressure=1" || echo "pressure=0"
	[ -r /proc/sys/fs/file-nr ] && echo "fds=1" || echo "fds=0"
	[ -r /proc/sys/kernel/pid_max ] && echo "pids=1" || echo "pids=0"
	[ -r /proc/sys/fs/inotify/max_user_instances ] && echo "inotify=1" || echo "inotify=0"
	[ -r /proc/sys/kernel/random/entropy_avail ] && echo "entropy=1" || echo "entropy=0"
	[ -r /proc/net/stat/nf_conntrack ] || [ -r /proc/sys/net/netfilter/nf_conntrack_count ] && echo "conntrack=1" || echo "conntrack=0"
	[ -d /sys/class/hwmon ] && echo "hwmon=1" || echo "hwmon=0"
	edac=0
	for controller in /sys/devices/system/edac/mc/mc[0-9]*; do
		[ -d "$controller" ] || continue
		edac=1
		break
	done
	echo "edac=${edac}"
	[ -r /proc/mdstat ] && echo "mdstat=1" || echo "mdstat=0"
	command -v tmux >/dev/null 2>&1 && echo "tmux=1" || echo "tmux=0"
	command -v screen >/dev/null 2>&1 && echo "screen=1" || echo "screen=0"
} >"${out}/features"

{
	tmux_binary="$(command -v tmux 2>/dev/null || true)"
	if [ -n "$tmux_binary" ]; then
		tmux_binary="$(readlink -f "$tmux_binary" 2>/dev/null || true)"
		find /tmp -mindepth 2 -maxdepth 2 -type s -path '/tmp/tmux-*/*' -print 2>/dev/null \
			| while IFS= read -r socket; do
				uid="$(stat -c '%u' -- "$socket" 2>/dev/null || true)"
				[ -n "$uid" ] || continue
				[ "$(basename "$(dirname "$socket")")" = "tmux-${uid}" ] || continue
				user="$(getent passwd "$uid" 2>/dev/null | awk -F: 'NR == 1 { print $1 }')"
				[ -n "$user" ] || continue
				printf 'tmux\t%s\t%s\t%s\n' "$user" "$tmux_binary" "$socket"
			done
	fi
	screen_binary="$(command -v screen 2>/dev/null || true)"
	if [ -n "$screen_binary" ]; then
		screen_binary="$(readlink -f "$screen_binary" 2>/dev/null || true)"
		for screen_root in /run/screen /var/run/screen /tmp/screen; do
			[ -d "$screen_root" ] || continue
			find "$screen_root" -mindepth 2 -maxdepth 2 -type s -print 2>/dev/null \
				| while IFS= read -r socket; do
					uid="$(stat -c '%u' -- "$socket" 2>/dev/null || true)"
					[ -n "$uid" ] || continue
					user="$(getent passwd "$uid" 2>/dev/null | awk -F: 'NR == 1 { print $1 }')"
					[ -n "$user" ] || continue
					printf 'screen\t%s\t%s\t\n' "$user" "$screen_binary"
				done
		done
	fi
} | sort -u >"${out}/terminal_sessions.tsv"

if [ -d /etc/ssl ]; then
	find /etc/ssl -maxdepth 1 -type f \( -name '*.crt' -o -name '*.cer' -o -name '*.pem' \) -print >"${out}/certs" 2>/dev/null || true
else
	: >"${out}/certs"
fi

if [ -d /usr/share/GeoIP ]; then
	printf '%s\n' /usr/share/GeoIP >"${out}/geoip_directory"
else
	: >"${out}/geoip_directory"
fi

if [ -d /sys/class/hwmon ]; then
	find /sys/class/hwmon -maxdepth 2 -type f -name 'temp*_input' -print >"${out}/hwmon_temp_inputs" 2>/dev/null || true
else
	: >"${out}/hwmon_temp_inputs"
fi

: >"${out}/docker_containers.json"
: >"${out}/docker_containers.jsonl"
if [ -S /run/docker.sock ]; then
	if command -v curl >/dev/null 2>&1; then
		curl -fsS --max-time 10 --unix-socket /run/docker.sock "http://localhost/containers/json?all=1" >"${out}/docker_containers.json" 2>"${out}/docker_containers.err" || true
	elif command -v docker >/dev/null 2>&1; then
		docker --host unix:///run/docker.sock ps -a --format '{{json .}}' >"${out}/docker_containers.jsonl" 2>"${out}/docker_containers.err" || true
	fi
fi

# Exit code and restart policy of every container that is not running, so the
# generator can tell a failed service container from a one-off `docker run`
# leftover. The container list API reports neither: its HostConfig carries only
# NetworkMode. Enumerating and inspecting without a JSON parser needs the docker
# CLI, so without it this stays empty and every non-running container is left
# out, as before. Keep this block in step across the collectors.
: >"${out}/docker_stopped.tsv"
if [ -S /run/docker.sock ] && command -v docker >/dev/null 2>&1; then
	docker --host unix:///run/docker.sock ps -aq --filter status=exited --filter status=dead \
		2>>"${out}/docker_containers.err" \
		| while IFS= read -r container_id; do
			[ -n "$container_id" ] || continue
			docker --host unix:///run/docker.sock inspect \
				--format '{{.Name}}{{"\t"}}{{.State.Status}}{{"\t"}}{{.State.ExitCode}}{{"\t"}}{{.HostConfig.RestartPolicy.Name}}' \
				"$container_id" 2>>"${out}/docker_containers.err" || true
		done >"${out}/docker_stopped.tsv"
fi

: >"${out}/libvirt_domains.tsv"
if command -v virsh >/dev/null 2>&1; then
	libvirt_socket=""
	if [ -S /run/libvirt/libvirt-sock ]; then
		libvirt_socket="/run/libvirt/libvirt-sock"
	elif [ -S /run/libvirt/virtqemud-sock ]; then
		libvirt_socket="/run/libvirt/virtqemud-sock"
	fi
	if [ -n "$libvirt_socket" ]; then
		virsh -q -c qemu:///system list --all --name >"${out}/libvirt_domain_names" 2>"${out}/libvirt_domains.err" || true
		while IFS= read -r domain; do
			[ -n "$domain" ] || continue
			state="$(virsh -q -c qemu:///system domstate "$domain" 2>/dev/null | head -n 1 || true)"
			printf '%s\t%s\t%s\t%s\n' "$libvirt_socket" "qemu:///system" "$domain" "$state"
		done <"${out}/libvirt_domain_names" >"${out}/libvirt_domains.tsv"
	fi
fi

: >"${out}/libvirt_networks.tsv"
if command -v virsh >/dev/null 2>&1; then
	# Two sessions: network RPC prefers the modular network daemon's socket,
	# and the guest-attachment guard needs a domain-API socket (virtqemud or
	# monolithic libvirtd) — on modular hosts they are different daemons.
	network_socket=""
	if [ -S /run/libvirt/virtnetworkd-sock ]; then
		network_socket="/run/libvirt/virtnetworkd-sock"
	elif [ -S /run/libvirt/libvirt-sock ]; then
		network_socket="/run/libvirt/libvirt-sock"
	fi
	network_guard_socket=""
	if [ -S /run/libvirt/libvirt-sock ]; then
		network_guard_socket="/run/libvirt/libvirt-sock"
	elif [ -S /run/libvirt/virtqemud-sock ]; then
		network_guard_socket="/run/libvirt/virtqemud-sock"
	fi
	if [ -n "$network_socket" ]; then
		virsh -q -c qemu:///system net-list --all --name >"${out}/libvirt_network_names" 2>"${out}/libvirt_networks.err" || true
		while IFS= read -r network; do
			[ -n "$network" ] || continue
			network_xml="$(virsh -q -c qemu:///system net-dumpxml "$network" 2>/dev/null || true)"
			network_state="inactive"
			virsh -q -c qemu:///system net-info "$network" 2>/dev/null | grep -q '^Active:.*yes' && network_state="active"
			network_bridge="$(printf '%s' "$network_xml" | sed -n "s/.*<bridge name='\([^']*\)'.*/\1/p" | head -n 1)"
			network_has_ip="no"
			printf '%s' "$network_xml" | grep -q '<ip ' && network_has_ip="yes"
			# The exe of the network's live dnsmasq pair, so the generated
			# service can attribute it; a replaced binary reports its old path.
			network_dnsmasq=""
			for network_pid in $(pgrep -f "dnsmasq.*--conf-file=/var/lib/libvirt/dnsmasq/${network}.conf" 2>/dev/null); do
				network_exe="$(readlink "/proc/${network_pid}/exe" 2>/dev/null || true)"
				network_exe="${network_exe% (deleted)}"
				if [ -n "$network_exe" ]; then
					network_dnsmasq="$network_exe"
					break
				fi
			done
			printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$network_socket" "network:///system" "$network" "$network_state" "$network_bridge" "$network_has_ip" "$network_dnsmasq" "$network_guard_socket"
		done <"${out}/libvirt_network_names" >"${out}/libvirt_networks.tsv"
	fi
fi

log "stage complete"
finish 0
