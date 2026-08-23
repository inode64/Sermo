#!/usr/bin/env bash
# Point Sermo's dashboard at the protected credential file and restart it safely.
set -euo pipefail

stage_dir="${1:-}"
config="/etc/sermo/sermo.yml"
credentials="/etc/sermo/credentials.env"
sermoctl="/usr/bin/sermoctl"
restart_timeout_seconds="${SERMO_RESTART_TIMEOUT_SECONDS:-60}"
protected_paths=(/ /etc /usr /usr/lib /etc/systemd /usr/lib/tmpfiles.d /etc/init.d /usr/share)
candidate=""
backup=""
before=""
after=""

usage() {
	echo "usage: $0 STAGING_DIRECTORY" >&2
	exit 64
}

snapshot_protected_paths() {
	local destination="$1"
	: >"$destination"
	for path in "${protected_paths[@]}"; do
		if [ -e "$path" ]; then
			stat -c '%n|%F|%a|%u|%g' "$path" >>"$destination" 2>/dev/null || printf '%s|stat-error\n' "$path" >>"$destination"
		else
			printf '%s|missing\n' "$path" >>"$destination"
		fi
	done
}

cleanup() {
	[ -z "$candidate" ] || rm -f -- "$candidate"
	[ -z "$backup" ] || rm -f -- "$backup"
	[ -z "$before" ] || rm -f -- "$before"
	[ -z "$after" ] || rm -f -- "$after"
}

restore_config() {
	local restored
	restored="$(mktemp /etc/sermo/.sermo.yml.restore.XXXXXX)"
	cp --preserve=mode,ownership "$backup" "$restored"
	mv -f -- "$restored" "$config"
}

validate_config() {
	if [ "$init" = "unknown" ]; then
		timeout 30s "$sermoctl" --config "$config" config validate
	else
		timeout 30s env "SERMO_BACKEND=$init" "SERMO_INIT=$init" "$sermoctl" --config "$config" config validate
	fi
}

render_config() {
	awk '
	function indentation(line, copy) {
		copy = line
		sub(/[^ \t].*$/, "", copy)
		return copy
	}
	function append_password_file() {
		if (!written) {
			if (child_indent == "") child_indent = base_indent "  "
			print child_indent "password_file: /etc/sermo/credentials.env"
			written = 1
		}
	}
	BEGIN {
		in_web = 0
		found = 0
		written = 0
		invalid = 0
	}
	{
		line_indent = indentation($0)
		if (!in_web) {
			if ($0 ~ /^[ \t]*web:/) {
				if ($0 ~ /^[ \t]*web:[ \t]*(#.*)?$/) {
					in_web = 1
					found = 1
					base_indent = line_indent
					child_indent = ""
				} else {
					invalid = 1
				}
			}
			print
			next
		}
		if ($0 !~ /^[ \t]*(#.*)?$/ && length(line_indent) <= length(base_indent)) {
			append_password_file()
			in_web = 0
			print
			next
		}
		if ($0 ~ /^[ \t]*password(_file)?:[ \t]*/) next
		if (child_indent == "" && $0 !~ /^[ \t]*(#.*)?$/) child_indent = line_indent
		print
	}
	END {
		if (invalid) exit 65
		if (in_web) append_password_file()
		if (!found) {
			print ""
			print "web:"
			print "  password_file: /etc/sermo/credentials.env"
		}
	}
	' "$config" >"$candidate"
}

[ "$#" -eq 1 ] || usage
case "$stage_dir" in
	/tmp/sermo-credentials-*) ;;
	*)
		echo "invalid staging directory" >&2
		exit 65
		;;
esac
[ "$(id -u)" = "0" ] || {
	echo "web credential configuration must run as root" >&2
	exit 10
}
[ -f "$config" ] && [ ! -L "$config" ] || {
	echo "Sermo configuration must be a regular file" >&2
	exit 11
}
[ -f "$credentials" ] && [ ! -L "$credentials" ] || {
	echo "credentials must be a regular file" >&2
	exit 12
}
[ "$(stat -c '%a' "$credentials")" = "600" ] || {
	echo "credentials mode is not 0600" >&2
	exit 13
}
[ "$(stat -c '%U:%G' "$credentials")" = "root:root" ] || {
	echo "credentials owner is not root:root" >&2
	exit 14
}
[ -x "$sermoctl" ] || {
	echo "sermoctl is unavailable" >&2
	exit 15
}
command -v timeout >/dev/null 2>&1 || {
	echo "timeout is unavailable" >&2
	exit 16
}
case "$restart_timeout_seconds" in
	'' | *[!0-9]*) restart_timeout_seconds=60 ;;
esac

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
	init="systemd"
elif command -v rc-service >/dev/null 2>&1; then
	init="openrc"
else
	init="unknown"
fi

backup="${stage_dir}/sermo.yml.before"
before="${stage_dir}/protected-paths.before"
after="${stage_dir}/protected-paths.after"
trap cleanup EXIT
snapshot_protected_paths "$before"
cp --preserve=mode,ownership "$config" "$backup"
candidate="$(mktemp /etc/sermo/.sermo.yml.credentials.XXXXXX)"
render_config || {
	echo "web configuration is not a supported YAML mapping" >&2
	exit 20
}
chmod --reference="$config" "$candidate"
chown --reference="$config" "$candidate"
mv -f -- "$candidate" "$config"
candidate=""

if ! validate_config; then
	restore_config
	echo "configuration validation failed; original restored" >&2
	exit 21
fi

active=0
case "$init" in
	systemd)
		if timeout 15s systemctl is-active --quiet sermod; then active=1; fi
		;;
	openrc)
		if timeout 15s rc-service sermod status >/dev/null 2>&1; then active=1; fi
		;;
esac
if [ "$active" = "1" ]; then
	case "$init" in
		systemd) restart_command=(systemctl restart sermod) ;;
		openrc) restart_command=(rc-service sermod restart) ;;
		*)
			restore_config
			echo "unknown init backend; original restored" >&2
			exit 22
			;;
	esac
	if ! timeout "${restart_timeout_seconds}s" "${restart_command[@]}"; then
		restore_config
		timeout "${restart_timeout_seconds}s" "${restart_command[@]}" || true
		echo "sermod restart failed; original restored" >&2
		exit 23
	fi
	case "$init" in
		systemd) timeout 15s systemctl is-active --quiet sermod ;;
		openrc) timeout 15s rc-service sermod status >/dev/null ;;
	esac || {
		restore_config
		timeout "${restart_timeout_seconds}s" "${restart_command[@]}" || true
		echo "sermod did not become active; original restored" >&2
		exit 24
	}
	restart_state="restarted"
else
	restart_state="inactive-not-restarted"
fi

snapshot_protected_paths "$after"
if ! diff -u "$before" "$after" >/dev/null; then
	echo "protected path metadata changed" >&2
	exit 70
fi
printf 'status=ok init=%s service=%s\n' "$init" "$restart_state"
