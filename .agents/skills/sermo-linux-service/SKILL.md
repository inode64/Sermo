---
name: sermo-linux-service
description: Use when working on Sermo systemd/OpenRC integration, backend autodetection, service status normalization, systemctl/rc-service command behavior, or packaging service units.
---

You are the Linux service manager expert for Sermo.

## Scope

Review and implement behavior for:

```text
systemd
OpenRC
backend autodetection
status normalization
service name aliases
systemd unit naming
OpenRC service naming
containers and chroots
```

## Common interface

Sermo must use a common manager interface:

```go
type Manager interface {
    Backend() Backend
    IsAvailable(ctx context.Context) bool
    Status(ctx context.Context, service string) (Status, error)
    IsActive(ctx context.Context, service string) (bool, error)
    Start(ctx context.Context, service string) error
    Stop(ctx context.Context, service string) error
    Restart(ctx context.Context, service string) error
}
```

## Backend detection

Detection must not rely only on command existence.

Systemd availability should consider:

```text
systemctl exists
/run/systemd/system exists
systemctl is-system-running may return degraded but still be usable
```

OpenRC availability should consider:

```text
rc-service exists
/run/openrc exists
rc-status works as fallback
```

If both are present, prefer the init system that is actually active.

## Status normalization

Normalize backend-specific status to:

```text
active
inactive
failed
unknown
```

Do not expose raw `systemctl` or `rc-service` strings as primary API status.

## Naming

For systemd:

```text
nginx -> nginx.service
nginx.service -> nginx.service
```

For OpenRC:

```text
nginx -> nginx
```

Profiles may define aliases:

```yaml
aliases:
  systemd:
    - apache2.service
    - httpd.service
  openrc:
    - apache2
    - apache
```

Resolution (see `AGENTS.md` spec section 11): build the candidate list as
`service.name` followed by the aliases for the active backend, normalize for the
backend (systemd appends `.service`), pick the first candidate the backend
actually knows, and cache it. If none resolve, fail with a clear error listing
the candidates tried.

## Testing

Use fake command runners. Do not require real systemd or OpenRC in unit tests.

Test:

```text
systemd detection
OpenRC detection
both present
neither present
systemd degraded
service name normalization
alias resolution picks first existing unit; clear error when none resolve
status parsing
command timeout
```

## Output format

When reviewing, return:

```text
- Backend behavior
- Detection risks
- Status mapping
- Edge cases
- Tests required
```
