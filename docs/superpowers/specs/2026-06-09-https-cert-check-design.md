# HTTPS certificate inspection in the `http` check — design

Date: 2026-06-09

## Goal

Let the existing `http` check also inspect and validate the server's TLS
certificate when the target URL uses `https://`, reusing the certificate
machinery that already lives in `internal/checks/cert.go`. The check becomes
protocol-aware: for `http://` URLs the certificate options do not apply; for
`https://` URLs the check can assert certificate expiry, chain/hostname
validity, and detect changes, and exposes the certificate's fields in the
result data.

## Non-goals

- A new check type. The behaviour is folded into the existing `http` check.
- Replacing the standalone `cert` check (which still serves raw TLS endpoints
  and local files).
- Inspecting anything other than the leaf certificate presented on the request
  connection.

## Approach (chosen)

Extend the `http` check with optional `cert_*` configuration fields. When the
URL scheme is `https` and at least one `cert_*` key is present, the check
inspects the certificate observed on the HTTP response connection
(`resp.TLS.PeerCertificates`) and folds any certificate problem into the
check's pass/fail result.

Rejected alternatives:

- **New dedicated `https` check type** — duplicates the entire http-assertion
  surface plus certificate config. High duplication.
- **Leaving the checks fully separate** — does not satisfy the request to add
  certificate checking to the https check.

## Configuration

New optional keys on the `http` check (all default off / empty):

| Key                        | Type | Meaning                                                              |
| -------------------------- | ---- | ------------------------------------------------------------------- |
| `cert_expires_in_days`     | int  | Alert when the certificate expires within N days, is expired, or is not yet valid. |
| `cert_verify`              | bool | Verify the chain and hostname. Defaults to `true` when certificate inspection is active. |
| `cert_on_change`           | bool | Alert when the leaf fingerprint changes between cycles.             |
| `cert_on_issuer_change`    | bool | Alert when the issuer changes between cycles.                       |
| `cert_on_algorithm_change` | bool | Alert when the signature algorithm changes between cycles.          |

**Activation:** certificate inspection runs when *any* `cert_*` key is present.
With none set, the `http` check behaves exactly as today (shared verifying
client, no certificate inspection, request fails on an invalid certificate).

**Protocol awareness:** the scheme is parsed from `url`. If any `cert_*` key is
set on a non-`https` URL, `buildCheck` returns the warning
`http check: cert_* options require an https url` (consistent with how
`buildCheck` reports other configuration problems).

## Semantics

The `http` check is pass/fail — `OK == true` means healthy. The standalone
`cert` check is condition-style — `OK == true` means an alert fired. These are
opposite, so the certificate result is **not** surfaced with `cert`-check
semantics. Instead, a certificate problem becomes an **additional failure
reason** for the `http` check: the check passes only when the status / body /
JSON assertions pass *and* the certificate is healthy. A certificate problem
yields `OK == false` with a certificate-specific message.

A genuinely broken TLS handshake unrelated to certificate validity (connection
reset, protocol mismatch) still fails the request the same way it does today.

## Certificate retrieval

To report expiry on an already-expired (or otherwise invalid) certificate, the
TLS handshake must not abort before the certificate can be read. When
certificate inspection is active the check therefore uses a dedicated
`*http.Client` whose transport sets `InsecureSkipVerify` (a clone of
`http.DefaultTransport` with an overridden `TLSClientConfig`), built once at
check-construction time. The certificate is then read from
`resp.TLS.PeerCertificates[0]` and verified manually.

- `resp.TLS.PeerCertificates[0]` → `certSampleFromCert` (existing) → `CertSample`.
- Chain/hostname verification reuses the logic extracted from
  `defaultCertSampler`, with `PeerCertificates[1:]` as intermediates and the URL
  host as the `ServerName`. The result populates `CertSample.VerifyError`.

The shared client used by non-cert `http` checks is unchanged (verifying,
default transport), so existing behaviour is preserved.

## Reuse / refactor in `cert.go`

To keep one implementation shared by both checks:

1. **`verifyCertChain(leaf *x509.Certificate, peers []*x509.Certificate, serverName string) string`**
   — extracted from `defaultCertSampler`; returns the verification error string
   (empty when valid). `defaultCertSampler` calls it. The `http` check calls it
   with the certificates from `resp.TLS`.

2. **`certEvaluator`** — a small stateful helper holding the change-detection
   state (`primed`, `lastAlgo`, `lastIssuer`, `lastFP`) and an `evaluate`
   method that, given a `CertSample`, the configured options, and `now`,
   returns `(problems []string, daysLeft int, hasExpiry bool)`. It encapsulates
   the expiry checks (expired / not-yet-valid / expires-in-N-days), the
   `verify` problem, and the algorithm/issuer/fingerprint change checks.
   `certCheck.Run` is refactored to use it (removing the inline duplication);
   `httpCheck` holds one too.

3. **`certData(source, host, path string, s CertSample, daysLeft int, hasExpiry bool) map[string]any`**
   — refactored to take plain values instead of `*certCheck`, so both checks
   assemble the same certificate data map. `certCheck.Run` passes its own
   `source()/host/path`; the `http` check passes the URL host.

## State

Change detection needs per-cycle state, so `httpCheck` becomes a pointer
(`*httpCheck` with a pointer-receiver `Run`), matching `certCheck` and
`portsCheck`. `buildCheck` returns `&httpCheck{...}`. The embedded
`certEvaluator` carries the change-detection state.

## Result data

When certificate inspection ran, the `http` result's `Data` includes the
certificate fields produced by the refactored `certData` (`kind`, `source`,
`fingerprint`, `host`, `signature_algorithm`, `public_key_algorithm`,
`key_bits`, `subject`, `issuer`, `serial_number`, `dns_names`, `days_left`,
`value`, `not_before`, `not_after`). When certificate inspection did not run,
`Data` is unchanged from today (none).

## Testing

Table-driven tests for the `http` check:

1. `https` + certificate expiring within the threshold → `OK == false` with an
   expiry message and populated `Data`.
2. `https` + valid certificate, no problems → `OK == true`, `Data` carries the
   certificate fields.
3. `http://` URL with a `cert_*` key → `buildCheck` warning.
4. Change detection: two `Run` calls where the fingerprint/issuer/algorithm
   changes between them → second call alerts.
5. `cert_verify` failure (hostname mismatch / self-signed) → `OK == false`.

Served via `httptest.NewTLSServer` with crafted certificates (mirroring the
existing cert-sampler test patterns). Plus unit tests for the extracted
`verifyCertChain` and `certEvaluator`, and a regression check that
`certCheck.Run` still behaves identically after the refactor.

## Files touched

- `internal/checks/cert.go` — extract `verifyCertChain`, `certEvaluator`;
  refactor `certData`; `certCheck.Run` uses the evaluator.
- `internal/checks/types.go` — `httpCheck` gains certificate fields + evaluator,
  becomes pointer-receiver, runs inspection on `https`.
- `internal/checks/build.go` — parse `cert_*` keys, scheme check, build the
  per-check insecure client, return `&httpCheck{...}`.
- Tests alongside the above.
- Documentation: `docs/configuration.md` (http check certificate options).
