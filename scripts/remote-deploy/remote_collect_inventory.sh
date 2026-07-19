#!/usr/bin/env bash
# Read-only host inventory for an already installed Sermo host. Collects the
# same evidence set remote_stage.sh gathers at install time (keep both scripts
# in step) but against the existing /etc/sermo, without touching binaries,
# catalog or configuration. Safe to run on the whole fleet in one pass.
set -u

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
	systemctl list-units --type=service --state=active --no-legend --no-pager >"${out}/active_units" 2>/dev/null || true
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
		address="$(getent ahostsv4 "$host" 2>/dev/null | awk 'NR == 1 { print $1 }')"
		if [ -z "$address" ]; then
			address="$(getent ahostsv6 "$host" 2>/dev/null | awk 'NR == 1 { print $1 }')"
		fi
		iface=""
		if [ -n "$address" ]; then
			iface="$(ip route get "$address" 2>/dev/null | awk '{ for (i = 1; i < NF; i++) if ($i == "dev") { print $(i + 1); exit } }')"
		fi
		printf '%s\t%s\t%s\n' "$host" "$address" "$iface" >>"${out}/nfs_routes"
	done < <(awk '$1 !~ /^#/ && ($3 == "nfs" || $3 == "nfs4") { print $1 }' /etc/fstab)
fi
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

{
	command -v hdparm >/dev/null 2>&1 && echo "hdparm=1" || echo "hdparm=0"
	command -v smartctl >/dev/null 2>&1 && echo "smartctl=1" || echo "smartctl=0"
	command -v sensors >/dev/null 2>&1 && echo "sensors=1" || echo "sensors=0"
	command -v mdadm >/dev/null 2>&1 && echo "mdadm=1" || echo "mdadm=0"
	command -v nft >/dev/null 2>&1 && echo "nft=1" || echo "nft=0"
	command -v iptables >/dev/null 2>&1 && echo "iptables=1" || echo "iptables=0"
	command -v curl >/dev/null 2>&1 && echo "curl=1" || echo "curl=0"
	command -v wget >/dev/null 2>&1 && echo "wget=1" || echo "wget=0"
	[ -r /proc/pressure/memory ] && echo "pressure=1" || echo "pressure=0"
	[ -r /proc/sys/fs/file-nr ] && echo "fds=1" || echo "fds=0"
	[ -r /proc/sys/kernel/pid_max ] && echo "pids=1" || echo "pids=0"
	[ -r /proc/sys/kernel/random/entropy_avail ] && echo "entropy=1" || echo "entropy=0"
	[ -r /proc/net/stat/nf_conntrack ] || [ -r /proc/sys/net/netfilter/nf_conntrack_count ] && echo "conntrack=1" || echo "conntrack=0"
	[ -d /sys/class/hwmon ] && echo "hwmon=1" || echo "hwmon=0"
	[ -d /sys/devices/system/edac/mc ] && echo "edac=1" || echo "edac=0"
	[ -r /proc/mdstat ] && echo "mdstat=1" || echo "mdstat=0"
} >"${out}/features"

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
