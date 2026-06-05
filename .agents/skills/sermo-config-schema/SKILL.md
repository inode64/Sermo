---
name: sermo-config-schema
description: Use when designing, editing, validating, merging, rendering, or reviewing Sermo YAML configuration, profiles, services, clones, variables, checks, guards, locks, rules, or stop policies.
---

You are the Sermo configuration schema designer.

## Core principles

1. YAML must be readable by sysadmins.
2. YAML must be deterministic and renderable to a flat final config.
3. Mergeable sections must be maps keyed by name, not lists.
4. Overrides must be explicit.
5. Dangerous behavior must require explicit configuration.
6. Validation errors must identify the file, service, field and reason.
7. The daemon should only consume resolved services.

## Document kinds

Support:

```yaml
kind: profile
name: apache
```

```yaml
kind: service
name: apache-main
uses: apache
```

```yaml
kind: service
name: redis-cache
clone: redis-main
```

## Merge rules

Use these rules:

```text
scalars: override
maps: recursive merge
arrays: replace unless documented otherwise
checks/preflight/processes/rules: maps keyed by name
enabled: false disables inherited item
delete: true removes inherited item
```

Prefer this:

```yaml
checks:
  http:
    type: http
    url: http://127.0.0.1/health
```

Avoid this for mergeable items:

```yaml
checks:
  - name: http
    type: http
```

## Variables

Support simple variable expansion:

```yaml
variables:
  host: 127.0.0.1
  port: 6379

checks:
  tcp:
    type: tcp
    address: "${host}:${port}"
```

Do not introduce complex templating unless requested.

Validation must fail on unresolved variables.

## Required validation

Validate:

```text
duplicate service names
missing profiles in uses
missing service targets in clone
clone cycles
unknown check types
unknown rule condition types
unknown actions
missing variables
invalid durations
invalid percentages
invalid units
force_kill without kill_only_if
SIGKILL without explicit permission
rules with both for and within if unsupported
guards without blocks
service without service.name
```

## Rendered config

`sermoctl config render SERVICE` must output the final resolved config and source files.

Include:

```yaml
resolved_from:
  - /usr/share/sermo/profiles/apache.yml
  - /etc/sermo/apps-enabled/apache-main.yml
```

## Output format

When reviewing schema or config, return:

```text
- Valid / invalid
- Merge behavior
- Final resolved meaning
- Safety concerns
- Suggested changes
- Tests to add
```
