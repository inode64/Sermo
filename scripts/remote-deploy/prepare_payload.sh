#!/usr/bin/env bash
set -eu

run_root="${1:?run root required}"
repo="${2:?repo required}"
payload_root="${run_root}/payload-root"

rm -rf "$payload_root"
mkdir -p \
	"${payload_root}/usr/bin" \
	"${payload_root}/usr/share/sermo" \
	"${payload_root}/etc/sermo/templates" \
	"${payload_root}/etc/systemd/system" \
	"${payload_root}/etc/init.d" \
	"${payload_root}/usr/lib/tmpfiles.d"

install -m 0755 "${repo}/bin/sermoctl" "${payload_root}/usr/bin/sermoctl"
install -m 0755 "${repo}/bin/sermod" "${payload_root}/usr/bin/sermod"
cp -a "${repo}/catalog" "${payload_root}/usr/share/sermo/catalog"
install -m 0644 "${repo}/templates/default-alert.yml" "${payload_root}/etc/sermo/templates/default-alert.yml"
install -m 0644 "${repo}/packaging/systemd/sermod.service" "${payload_root}/etc/systemd/system/sermod.service"
install -m 0755 "${repo}/packaging/openrc/sermod" "${payload_root}/etc/init.d/sermod"
install -m 0644 "${repo}/packaging/systemd/sermo.conf" "${payload_root}/usr/lib/tmpfiles.d/sermo.conf"

tar -C "$payload_root" -czf "${run_root}/sermo-install-payload.tgz" .
printf '%s\n' "${run_root}/sermo-install-payload.tgz"
