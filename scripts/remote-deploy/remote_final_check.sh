#!/usr/bin/env bash
set -u

web_password="${SERMO_WEB_PASSWORD:-sermo-remote-admin}"
out="/tmp/sermo-final-check.out"
: >"$out"

line() {
	printf '%s=%s\n' "$1" "$2" | tee -a "$out" >/dev/null
}

line host "$(hostname 2>/dev/null || echo unknown)"
line fqdn "$(hostname -f 2>/dev/null || hostname 2>/dev/null || echo unknown)"

protected_metadata="/tmp/sermo-final-protected-path-metadata"
: >"$protected_metadata"
for path in / /etc /usr /usr/lib /etc/systemd /usr/lib/tmpfiles.d /etc/init.d /usr/share; do
	if [ -e "$path" ]; then
		stat -c '%n|%F|%a|%u|%g' "$path" >>"$protected_metadata" 2>/dev/null || printf '%s|stat-error\n' "$path" >>"$protected_metadata"
	else
		printf '%s|missing\n' "$path" >>"$protected_metadata"
	fi
done
line protected_path_metadata "$protected_metadata"
sed 's/^/protected_path: /' "$protected_metadata" >>"$out"

if /usr/bin/sermoctl --config /etc/sermo/sermo.yml config validate >/tmp/sermo-final-validate.out 2>/tmp/sermo-final-validate.err; then
	line config_validate ok
else
	line config_validate fail
	sed 's/^/validate_error: /' /tmp/sermo-final-validate.err >>"$out"
fi

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
	line init systemd
	line service_state "$(systemctl is-active sermod 2>/dev/null || true)"
elif command -v rc-service >/dev/null 2>&1; then
	line init openrc
	if rc-service sermod status >/tmp/sermo-final-service.out 2>&1; then
		line service_state active
	else
		line service_state inactive
	fi
else
	line init unknown
	line service_state unknown
fi

if command -v ss >/dev/null 2>&1; then
	ss -ltnp 'sport = :9797' >/tmp/sermo-final-ss.out 2>&1 || true
	if grep -Eq '(\*:9797|0\.0\.0\.0:9797|\[::\]:9797)' /tmp/sermo-final-ss.out; then
		line web_bind 0.0.0.0:9797
	else
		line web_bind "$(tr '\n' ' ' </tmp/sermo-final-ss.out)"
	fi
else
	line web_bind ss-unavailable
fi

if command -v curl >/dev/null 2>&1; then
	curl -fsS -u "admin:${web_password}" "http://127.0.0.1:9797/livez?verbose" >/tmp/sermo-final-livez.out 2>/tmp/sermo-final-livez.err
	line livez_rc "$?"
	sed 's/^/livez: /' /tmp/sermo-final-livez.out >>"$out"
	curl -sS -u "admin:${web_password}" "http://127.0.0.1:9797/readyz?verbose" >/tmp/sermo-final-readyz.out 2>/tmp/sermo-final-readyz.err
	line readyz_rc "$?"
	sed 's/^/readyz: /' /tmp/sermo-final-readyz.out >>"$out"
	curl -fsS -u "admin:${web_password}" "http://127.0.0.1:9797/" >/tmp/sermo-final-index.html 2>/tmp/sermo-final-index.err
	line html_rc "$?"
else
	line livez_rc curl-unavailable
	line readyz_rc curl-unavailable
	line html_rc curl-unavailable
fi

line service_files "$(find /etc/sermo/services -maxdepth 1 -type f 2>/dev/null | wc -l)"
line watch_files "$(find /etc/sermo/watches /etc/sermo/networks /etc/sermo/storages /etc/sermo/mounts -maxdepth 1 -type f 2>/dev/null | wc -l)"
cat "$out"
