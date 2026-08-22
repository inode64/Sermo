#!/usr/bin/env bash
# Read-only host inventory for an already installed Sermo host. Collects the
# same evidence set remote_stage.sh gathers at install time (keep both scripts
# in step) but against the existing /etc/sermo, without touching binaries,
# catalog or configuration. Safe to run on the whole fleet in one pass.
set -u

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck disable=SC1091 # uploaded alongside this script by update_fleet.sh
. "${script_dir}/remote_inventory_common.sh"

# Machine-stable tool output. libvirt (and other gettext-aware tools) translate
# their output, so a Spanish host reports a running domain as "ejecutando" and
# every consumer that matches the English word silently treats it as stopped.
# Pin the locale for everything this collector parses.
export LC_ALL=C
export LANG=C
export LANGUAGE=

run_id="${1:-}"

if [ -z "$run_id" ]; then
	echo "usage: $0 RUN_ID" >&2
	exit 64
fi

case "$run_id" in
	*[![:alnum:]._-]*)
		echo "RUN_ID may contain only letters, numbers, dot, underscore and hyphen" >&2
		exit 64
		;;
esac

if [ "$(id -u)" != "0" ]; then
	echo "remote inventory must run as root" >&2
	exit 10
fi

work="/tmp/sermo-inventory-${run_id}"
out="${work}/out"
mkdir -p "$out"

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

hostname -f >"${out}/hostname_fqdn" 2>/dev/null || hostname >"${out}/hostname_fqdn" 2>/dev/null || true
hostname >"${out}/hostname" 2>/dev/null || true
uname -a >"${out}/uname" 2>/dev/null || true
if [ -r /etc/os-release ]; then
	cp /etc/os-release "${out}/os-release" || true
fi
date -Is >"${out}/started_at" 2>/dev/null || true

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

if [ ! -f /etc/sermo/sermo.yml ]; then
	echo "/etc/sermo/sermo.yml missing; host is not an installed Sermo target" >&2
	exit 20
fi

if [ "$init" = "systemd" ]; then
	systemctl list-units --type=service --state=active --no-legend --plain --no-pager >"${out}/active_units" 2>/dev/null || true
	# Kept in step with remote_stage.sh: a failed unit must stay monitorable.
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

# Keep this collector in step with remote_stage.sh. These Gluster CLI calls are
# status-only and return stable XML for the generated cluster-health check.
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
# PostgreSQL cluster facts, one line per postmaster, so the generator can enable
# the replication watches only on nodes that actually replicate. Everything here
# is read from /proc and the data directory: no database connection, no
# credentials, consistent with the read-only contract of this script.
: >"${out}/postgres_clusters"
for pid in /proc/[0-9]*; do
	[ -r "${pid}/comm" ] || continue
	case "$(cat "${pid}/comm" 2>/dev/null)" in
		postgres | postmaster) ;;
		*) continue ;;
	esac
	# Only the postmaster: backends and auxiliary workers are its children.
	[ "$(awk '/^PPid:/ { print $2; exit }' "${pid}/status" 2>/dev/null)" = "1" ] || continue
	datadir="$(readlink -f "${pid}/cwd" 2>/dev/null || true)"
	if [ -z "$datadir" ] || [ ! -d "$datadir" ]; then
		continue
	fi
	if [ -f "${datadir}/standby.signal" ] || [ -f "${datadir}/recovery.conf" ]; then
		role="standby"
	else
		role="primary"
	fi
	# A slot exists on disk even with no consumer connected, which is exactly the
	# case that silently retains WAL until the filesystem fills.
	slots=0
	if [ -d "${datadir}/pg_replslot" ]; then
		slots="$(find "${datadir}/pg_replslot" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | wc -l)"
	fi
	# Walsenders are children of this postmaster, so the count stays correct when
	# a host runs more than one cluster. PostgreSQL puts the role in the process
	# title, which is what /proc exposes as the command line.
	postmaster_pid="${pid#/proc/}"
	walsenders=0
	for child in /proc/[0-9]*; do
		[ -r "${child}/status" ] || continue
		[ "$(awk '/^PPid:/ { print $2; exit }' "${child}/status" 2>/dev/null)" = "$postmaster_pid" ] || continue
		case "$(tr '\0' ' ' <"${child}/cmdline" 2>/dev/null)" in
			"postgres: walsender "*) walsenders=$((walsenders + 1)) ;;
		esac
	done
	printf '%s\t%s\t%s\t%s\n' "$datadir" "$role" "$slots" "$walsenders" >>"${out}/postgres_clusters"
done

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

date -Is >"${out}/finished_at" 2>/dev/null || true
tar -C "$work" -czf "${work}/out.tar.gz" out >/dev/null 2>&1 || {
	echo "failed to archive inventory output" >&2
	exit 30
}
printf '%s\n' "${work}/out.tar.gz"
