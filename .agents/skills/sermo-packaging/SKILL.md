---
name: sermo-packaging
description: Use when creating or reviewing Sermo Linux packaging, systemd units, OpenRC init scripts, Gentoo ebuilds, install paths, permissions, runtime directories, or service startup behavior.
---

You are the Linux packaging engineer for Sermo.

## Install paths

Use these defaults:

```text
/usr/bin/sermoctl
/usr/sbin/sermod
/etc/sermo/
/usr/share/sermo/profiles/
/run/sermo/
/var/lib/sermo/
/var/log/sermo/ only if not using system logs
```

## systemd unit

The systemd service should:

```text
run sermod in foreground
use a dedicated config path
restart on failure if appropriate
create runtime directory where possible
use least privilege where practical
log to journald/stdout
```

Do not let systemd restart loops hide Sermo's own safety behavior.

## OpenRC service

The OpenRC init script should:

```text
run sermod in foreground or supervise mode
use command_args for config path
support start/stop/status
create runtime directory
work on Gentoo/OpenRC
```

## Permissions

Think carefully about privilege.

Sermo may need root for service control and signaling. If future non-root access is added, it must go through explicit authorization and must not allow arbitrary command execution.

## Packaging artifacts

Potential artifacts:

```text
packaging/systemd/sermod.service
packaging/openrc/sermod
packaging/gentoo/app-admin/sermo/sermo-9999.ebuild
packaging/debian/
```

## Tests/checks

Review packaging with:

```text
systemd-analyze verify packaging/systemd/sermod.service
shellcheck packaging/openrc/sermod
go test ./...
```

Use the best effort when tools are not available.

## Output format

When working on packaging, return:

```text
- files added/changed
- install paths
- service behavior
- permission assumptions
- validation commands run
```
