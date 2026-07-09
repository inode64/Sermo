#!/usr/bin/env bash
set -u

out="${1:-/tmp/sermo-runtime-targets}"
mkdir -p "$out"

: >"${out}/docker_containers.json"
: >"${out}/docker_containers.jsonl"
: >"${out}/docker_containers.err"
if [ -S /run/docker.sock ]; then
	if command -v curl >/dev/null 2>&1; then
		curl -fsS --max-time 10 --unix-socket /run/docker.sock "http://localhost/containers/json?all=1" >"${out}/docker_containers.json" 2>"${out}/docker_containers.err" || true
	elif command -v docker >/dev/null 2>&1; then
		docker --host unix:///run/docker.sock ps -a --format '{{json .}}' >"${out}/docker_containers.jsonl" 2>"${out}/docker_containers.err" || true
	fi
fi

: >"${out}/libvirt_domains.tsv"
: >"${out}/libvirt_domains.err"
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

wc -l "${out}/docker_containers.jsonl" "${out}/libvirt_domains.tsv" 2>/dev/null || true
if [ -s "${out}/docker_containers.json" ]; then
	printf 'docker_containers_json_bytes %s\n' "$(wc -c <"${out}/docker_containers.json")"
fi
