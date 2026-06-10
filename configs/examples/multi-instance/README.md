# Multiple instances from one daemon

This example shows how to run **several instances of the same application** —
same binary, same checks and rules — where each instance only differs in its
listen port, pidfile and config file. The classic cases are two MariaDB servers
or several php-fpm pools on one host.

There is no special "instance" mechanism: it is the ordinary
[`uses` + `variables`](../../../docs/daemons.md) inheritance. The daemon
parametrizes everything that varies with `${...}` placeholders; each enabled
service `uses` the daemon and overrides only the variables that make it unique.

Files:

- `daemons/dbserver.yml` — the shared daemon. Note the `config` variable and
  how it is threaded into the commands (`--defaults-file=${config}`) alongside
  the already-parametrized `port` (tcp check) and `pidfile` (pidfile process).
- `db-inst1.yml`, `db-inst2.yml` — two instances. Each overrides `port`,
  `pidfile` and `config`, and sets its own systemd/openrc unit via `service:`.

These files live under `configs/examples/` and are **not** loaded by the default
`sermo.yml` (which only reads `apps-enabled`). To try them, point a `paths`
entry at this directory, or copy the pattern into your own tree.

## Why `uses` and not `clone`

Use `uses` when N instances should all derive from the *daemon*: each instance
is independent and only overrides variables. Reach for `clone` only when you
want one instance to copy *another concrete service* almost verbatim (see the
`redis-cache` / `redis-main` pair in the docs).
