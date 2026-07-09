#!/usr/bin/env bash
set -u

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

if [ "$(id -u)" != "0" ]; then
	echo "remote installer must run as root" >&2
	exit 10
fi

hostname -f >"${out}/hostname_fqdn" 2>/dev/null || hostname >"${out}/hostname_fqdn" 2>/dev/null || true
hostname >"${out}/hostname" 2>/dev/null || true
uname -a >"${out}/uname" 2>/dev/null || true
[ -r /etc/os-release ] && cp /etc/os-release "${out}/os-release" || true

init="unknown"
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
	init="systemd"
elif command -v rc-service >/dev/null 2>&1; then
	init="openrc"
fi
printf '%s\n' "$init" >"${out}/init"

backup=""
if [ -e /etc/sermo ]; then
	backup="/etc/sermo.backup.${run_id}"
	if [ -e "$backup" ]; then
		backup="/etc/sermo.backup.${run_id}.$(date +%s)"
	fi
	mv /etc/sermo "$backup"
fi
printf '%s\n' "$backup" >"${out}/backup_path"

rm -rf /usr/share/sermo/catalog
tar -C / -xzf "$payload" >"${out}/payload_extract.out" 2>"${out}/payload_extract.err"
extract_rc=$?
printf '%s\n' "$extract_rc" >"${out}/payload_extract.rc"
if [ "$extract_rc" -ne 0 ]; then
	log "payload extraction failed"
	tar -C "$work" -czf "${work}/out.tar.gz" out >/dev/null 2>&1 || true
	exit 20
fi

mkdir -p /etc/sermo/services /etc/sermo/apps /etc/sermo/notifiers /etc/sermo/watches /etc/sermo/networks /etc/sermo/storages /etc/sermo/mounts /etc/sermo/templates
mkdir -p /run/sermo /var/lib/sermo
chmod 0700 /run/sermo /var/lib/sermo 2>/dev/null || true

cat >/etc/sermo/sermo.yml <<'YAML'
engine:
  backend: auto
  interval: 30s
  max_parallel_checks: 8
  max_parallel_operations: 2
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
    force_kill: false
  policy:
    cooldown: 5m

web:
  address: 0.0.0.0
  port: 9797
  password: "sermo-remote-admin"
YAML

if command -v systemd-tmpfiles >/dev/null 2>&1; then
	capture systemd_tmpfiles systemd-tmpfiles --create /usr/lib/tmpfiles.d/sermo.conf
fi

if command -v ss >/dev/null 2>&1; then
	ss -ltnp 'sport = :9797' >"${out}/port9797_before" 2>&1 || true
elif command -v netstat >/dev/null 2>&1; then
	netstat -ltnp >"${out}/port9797_before" 2>&1 || true
fi

if [ "$init" = "systemd" ]; then
	systemctl daemon-reload >"${out}/systemctl_daemon_reload.out" 2>"${out}/systemctl_daemon_reload.err" || true
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
capture config_validate_base /usr/bin/sermoctl --config /etc/sermo/sermo.yml config validate
capture services_json /usr/bin/sermoctl --config /etc/sermo/sermo.yml --json services
capture services_all_json /usr/bin/sermoctl --config /etc/sermo/sermo.yml --json services all

findmnt -R -J >"${out}/findmnt.json" 2>/dev/null || true
findmnt -R -P >"${out}/findmnt.pairs" 2>/dev/null || true
mount >"${out}/mount" 2>/dev/null || true
[ -r /proc/mounts ] && cp /proc/mounts "${out}/proc_mounts" || true
[ -r /proc/swaps ] && cp /proc/swaps "${out}/proc_swaps" || true
[ -r /proc/mdstat ] && cp /proc/mdstat "${out}/proc_mdstat" || true
[ -r /etc/fstab ] && cp /etc/fstab "${out}/fstab" || true
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
		ss -H -ltnup 2>/dev/null | grep -E 'users:\(\("(cloudflared|mysqld_exporter|named)"' \
			| sed 's#^#socket #'
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

tar -C "$work" -czf "${work}/out.tar.gz" out >/dev/null 2>&1 || true
log "stage complete"
