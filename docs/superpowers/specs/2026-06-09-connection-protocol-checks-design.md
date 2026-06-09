# Connection-protocol checks (MySQL/MariaDB first) — design

Date: 2026-06-09. Status: approved, implementation pending. Not committed (user reviews).

## Goal

Add a check that connects to a service over its wire protocol with user+password,
starting with **MySQL/MariaDB**, behind an **extensible protocol registry** so
future protocols (postgres, redis, …) are added by implementing one interface —
no changes to the check dispatch, validation, or docs machinery.

Locked decisions: type = protocol name (`type: mysql`, alias `mariadb`); probe =
connect + authenticate + ping (database/query optional later; expose version).

## Package `internal/conn` (leaf; no deps on checks/config)

```go
type Config struct {
    Host, User, Password, Database, TLS string
    Port   int
    Params map[string]string
}
type Result struct {
    Version string
    Extra   map[string]string
}
type Protocol interface {
    Name() string
    DefaultPort() int
    Probe(ctx context.Context, cfg Config) (Result, error)
}
func Register(p Protocol, aliases ...string)
func Lookup(name string) (Protocol, bool)
func Names() []string   // canonical names, sorted
```

- Registry is a package-level map; `Register` adds the canonical name plus any
  aliases (mysql registers alias "mariadb").
- `conn/mysql.go`: `mysqlProtocol` implements `Protocol`. `Probe` opens
  `sql.Open("mysql", dsn)`, `PingContext` (connect + auth), then
  `SELECT VERSION()` for `Result.Version`; closes the handle. DSN built by a
  testable `buildDSN(cfg) string`. Blank-imports `github.com/go-sql-driver/mysql`
  (pure Go; serves MySQL and MariaDB). `DefaultPort() == 3306`.
- `TLS` maps to the driver's `?tls=` param: `""`/`false` → plaintext, `true`,
  `skip-verify`. Default plaintext.

## Package `checks`

```go
type connCheck struct {
    base
    proto conn.Protocol
    cfg   conn.Config
    probe func(context.Context, conn.Config) (conn.Result, error) // = proto.Probe; injectable for tests
}
```
`Run`: `withTimeout`, call `probe`. On error → `result(false, "<proto> <host>:<port>: <err>")`.
On success → `result(true, "<proto> <host>:<port> ok (<version>)")` with
`Data{protocol, host, port, version}`.

`buildCheck` default branch: `if proto, ok := conn.Lookup(typ); ok { return
buildConnCheck(b, proto, entry) }` before the `unsupported type` error.
`buildConnCheck` parses host (default `127.0.0.1`), port (default
`proto.DefaultPort()`), **user (required → warning if missing)**, password,
database, tls; sets `probe = proto.Probe`.

## Validation (`config/validate.go`)

`validateCheckSection`: when `typ` is not in `knownCheckTypes` but
`conn.Lookup(typ)` succeeds, treat it as valid and call `validateConnFields`
(user required; port numeric if present; tls ∈ {true,false,skip-verify} if
present). So a newly registered protocol validates automatically. (config may
import the leaf `conn` package without a cycle.)

## Config example

```yaml
checks:
  db:
    type: mysql            # or mariadb
    host: 127.0.0.1        # default 127.0.0.1
    port: 3306             # default 3306
    user: monitor          # required
    password: "${env:DB_PASS}"   # resolved at config load
    database: ""           # optional
    tls: false             # optional: false | true | skip-verify
    timeout: 5s            # optional (engine.default_timeout)
```
Passes when it connects, authenticates and `PingContext` succeeds.

## Extensibility (later phases)

Add `conn/<proto>.go` implementing `Protocol` + `conn.Register(...)`. Nothing
else changes: dispatch, validation and the generic `connCheck` are protocol-
agnostic. Only the docs type-table gets a new row.

## go.mod

Add `github.com/go-sql-driver/mysql` (pure Go) via `go get`; blank import in
`conn/mysql.go`. Consistent with the repo's cgo-free dependency set.

## Testing

- `conn`: `buildDSN` table (with/without password, database, tls params);
  registry Register/Lookup/alias/Names. Real connections are not unit-tested.
- `checks`: `connCheck.Run` with an injected fake probe (success → OK + version
  in Data; error → fail); `buildCheck` `type: mysql` (host/port defaults, user
  required → warning, unknown protocol → warning).
- `config`: `mysql` check validates (with/without user; invalid tls).

## Docs

- `docs/rules.md`: add a `mysql` / `mariadb` row to the check-types table + a
  short "Database connection" subsection.
- `docs/configuration.md`: a MySQL/MariaDB example using `${env:...}` for the
  password.

## Out of scope (v1)

Optional `query` execution, connection pooling/reuse, non-MySQL protocols (the
registry is ready for them), and wizard integration.
