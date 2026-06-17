# Sermo TODO — future improvements

Future work moved out of `AGENTS.md` so the instructions describe only what
exists. Nothing here is committed scope; pick items deliberately.

## Major features

- Distributed cluster mode
- Remote agents
- Remote API authentication
- Multi-tenant RBAC
- Plugin ABI
- Complex notification integrations (email, Slack, Teams + templates; additional sinks like file/syslog/generic webhook still pending)
- Metrics export (Prometheus, OpenMetrics) — also as an event sink besides
  log/slog (JSON file, syslog and webhook sinks are likewise pending)
- Server MCP or gRPC API
- PolicyKit integration
- Native systemd D-Bus backend (the command-based backend works today)

## Engine and config

- `exec` rule action: the `ActionExec` type is reserved in the rule model but
  not implemented — `then: {action: exec, command: [...], timeout: ...}` (array
  form, never a shell string).
- Variable-to-variable references (`variables.x: "${y}"`), with cycle
  detection. Today a variable value containing `${...}` is a validation error.

