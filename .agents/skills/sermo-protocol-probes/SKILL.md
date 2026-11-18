---
name: sermo-protocol-probes
description: Use when implementing or reviewing network probes in internal/conn, including client selection, interface binding, wire parsing and busy replies.
---

Design decisions for `internal/conn` probes.

- Every probe honors `cfg.Interface`. Stream probes dial through
  `BindDialer`; packet listeners use `bindListenConfig`. A client library is
  acceptable only if it is codec-only or accepts Sermo's dialer or connection;
  reject one that performs unhookable internal I/O.
- Built-ins and aliases are registered in `internal/conn/registry.go`.
  Registered probes enter the shared executor once and obtain the prepared
  target through the existing helpers. No package-init registration, no
  duplicated endpoint defaults.
- The transport exceptions stay local and documented at the call: chronyd's
  Unix datagram client and DHCP's per-datagram `IP_PKTINFO` path. A protocol
  rebuilds an endpoint only when its wire format genuinely selects a different
  target.
- A server that answers it cannot serve now (too many connections, loading,
  starting up, a 421 greeting) is a busy verdict, not an error: classify it in
  `internal/conn/busy.go` and return `busyResult(...)` with a nil error, or
  `AsBusy` for a driver error. New codes go there as constants, so probes,
  `sql` and `db_queries` share one vocabulary. What a busy result may trigger
  is in `sermo-rule-engine`.
- Variable length and count fields on the wire go through the `wire*` helpers
  in `internal/conn/wire.go`, which return an error instead of truncating. A
  conversion with an explicit local bound (FastCGI string lengths below 128)
  converts directly; a helper that accepts arbitrary buffers or ports keeps
  the check.
- HTTP goes through `internal/httpx`.
