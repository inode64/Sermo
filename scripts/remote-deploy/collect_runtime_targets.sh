#!/usr/bin/env bash
set -u

# Machine-stable tool output. libvirt (and other gettext-aware tools) translate
# their output, so a Spanish host reports a running domain as "ejecutando" and
# every consumer that matches the English word silently treats it as stopped.
# Pin the locale for everything this collector parses.
export LC_ALL=C
export LANG=C
export LANGUAGE=

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
