# HTTPS Certificate Inspection in the `http` Check - Implementation Plan - historical

> Historical implementation plan. The feature is implemented; keep this
> checklist as archival context only. For new agent work, follow the repository's
> current `AGENTS.md` workflow rather than the old worktree instruction.

**Goal:** Make the `http` check protocol-aware so that, on an `https://` URL, it can also assert the server certificate's expiry, chain/hostname validity, and changes — reusing the certificate machinery in `internal/checks/cert.go`.

**Architecture:** Refactor `cert.go` to expose three reusable units — `verifyCertChain`, a stateful `certEvaluator`, and a value-based `certData` — then extend `httpCheck` to read the leaf certificate from `resp.TLS` (via an `InsecureSkipVerify` client so invalid/expired certs are still readable) and fold any certificate problem into its pass/fail result. `buildCheck` parses the new `cert_*` keys and rejects them on non-https URLs.

**Tech Stack:** Go, standard library (`crypto/tls`, `crypto/x509`, `net/http`, `net/url`), `httptest` for tests.

**Spec:** `docs/superpowers/specs/2026-06-09-https-cert-check-design.md`

**Conventions reminder (`AGENTS.md`):** every modified `.go` file must be
`gofmt`-clean. Before the final commit, run the full static-analysis checklist
(Task 7).

---

## File Structure

- `internal/checks/cert.go` — add `certOptions`, `certEvaluator`, `verifyCertChain`; change `certData` signature; refactor `certCheck` to use the evaluator. (Certificate logic stays in its home file.)
- `internal/checks/types.go` — `httpCheck` gains certificate fields + a `success` helper; `Run` becomes a pointer receiver and runs inspection.
- `internal/checks/build.go` — parse `cert_*` keys, scheme check, build the per-check insecure client, return `&httpCheck{...}`; add `net/url` and `crypto/tls` imports.
- `internal/checks/cert_test.go` — unit tests for `certEvaluator` and `verifyCertChain`; confirm `certCheck` regression.
- `internal/checks/checks_test.go` — integration tests for the `http` check over TLS.
- `docs/configuration.md` — document the new `cert_*` options on the `http` check.

---

## Task 1: Extract `verifyCertChain` from `defaultCertSampler`

Pure refactor, no behaviour change. Establishes the shared chain/hostname verifier the `http` check will reuse.

**Files:**
- Modify: `internal/checks/cert.go` (`defaultCertSampler`, ~lines 364-389)
- Test: `internal/checks/cert_test.go`

- [ ] **Step 1: Write the failing test**

Add to `cert_test.go`:

```go
func TestVerifyCertChainSelfSigned(t *testing.T) {
	// A self-signed leaf does not chain to the system roots.
	leaf := mustSelfSigned(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if got := verifyCertChain(leaf, nil, leaf.Subject.CommonName); got == "" {
		t.Fatal("a self-signed cert must produce a verify error")
	}
}
```

Add this helper near the top of `cert_test.go` (used again in later tasks) — it mints a self-signed leaf certificate:

```go
func mustSelfSigned(t *testing.T, notBefore, notAfter time.Time) *x509.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test.local"},
		DNSNames:     []string{"test.local"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
```

Add the imports this helper needs to `cert_test.go`: `crypto/rand`, `crypto/rsa`, `crypto/x509`, `crypto/x509/pkix`, `math/big`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/checks/ -run TestVerifyCertChainSelfSigned -v`
Expected: FAIL — `undefined: verifyCertChain`.

- [ ] **Step 3: Add `verifyCertChain` and call it from `defaultCertSampler`**

In `cert.go`, add the function (place it just above `defaultCertSampler`):

```go
// verifyCertChain validates leaf against the system roots, using peers as the
// candidate intermediates, and checks it covers serverName. It returns the
// verification error string, or "" when the certificate is valid.
func verifyCertChain(leaf *x509.Certificate, peers []*x509.Certificate, serverName string) string {
	roots, _ := x509.SystemCertPool()
	inter := x509.NewCertPool()
	for _, c := range peers {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: serverName, Roots: roots, Intermediates: inter}); err != nil {
		return err.Error()
	}
	return ""
}
```

Replace the inline verify block in `defaultCertSampler` (the `if verify { ... }` at ~lines 378-387) with:

```go
	if verify {
		s.VerifyError = verifyCertChain(leaf, state.PeerCertificates[1:], serverName)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/checks/ -run 'TestVerifyCertChain|TestCert' -v`
Expected: PASS (new test passes; all existing cert tests still pass — `defaultCertSampler` behaviour is unchanged).

- [ ] **Step 5: Commit**

```bash
git add internal/checks/cert.go internal/checks/cert_test.go
git commit -m "Extract verifyCertChain helper from defaultCertSampler"
```

---

## Task 2: Introduce `certOptions` + `certEvaluator`; refactor `certCheck` onto it

Factor the expiry/verify/change evaluation out of `certCheck.Run` into a reusable, independently tested unit. `certCheck` keeps identical behaviour.

**Files:**
- Modify: `internal/checks/cert.go` (`certCheck` struct + `Run`, ~lines 56-158)
- Test: `internal/checks/cert_test.go`

- [ ] **Step 1: Write the failing unit test for the evaluator**

Add to `cert_test.go`:

```go
func TestCertEvaluatorChangeAndExpiry(t *testing.T) {
	var e certEvaluator
	opts := certOptions{expiresInDays: 14, onChange: true}
	now := time.Now()

	s := healthyCert() // 60 days out, fingerprint "aaaa"
	if probs, _, _ := e.evaluate(s, opts, now); len(probs) != 0 {
		t.Fatalf("first observation primes and a healthy cert must not alert: %v", probs)
	}
	s.Fingerprint = "bbbb" // changed
	if probs, _, _ := e.evaluate(s, opts, now); len(probs) != 1 || probs[0] != "certificate changed" {
		t.Fatalf("a fingerprint change must alert after priming: %v", probs)
	}

	// Expiry threshold is independent of priming.
	var e2 certEvaluator
	soon := healthyCert()
	soon.NotAfter = now.Add(5 * 24 * time.Hour)
	probs, daysLeft, hasExpiry := e2.evaluate(soon, certOptions{expiresInDays: 14}, now)
	if !hasExpiry || daysLeft > 5 || len(probs) != 1 {
		t.Fatalf("a cert 5 days out must alert with expires_in_days=14: probs=%v days=%d", probs, daysLeft)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/checks/ -run TestCertEvaluatorChangeAndExpiry -v`
Expected: FAIL — `undefined: certEvaluator` / `undefined: certOptions`.

- [ ] **Step 3: Add the types and refactor `certCheck`**

In `cert.go`, add above `certCheck`:

```go
// certOptions configures which certificate conditions raise a problem.
type certOptions struct {
	expiresInDays  int
	verify         bool
	onAlgoChange   bool
	onIssuerChange bool
	onChange       bool
}

// certEvaluator turns a CertSample into the problems it represents under a set
// of certOptions. It is stateful for change detection — it remembers the
// previous sample's algorithm, issuer and fingerprint — so a change condition
// only fires from the second observation onward.
type certEvaluator struct {
	primed     bool
	lastAlgo   string
	lastIssuer string
	lastFP     string
}

// evaluate reports the problems for sample s under opts at time now, plus the
// days until expiry and whether the material has an expiry at all.
func (e *certEvaluator) evaluate(s CertSample, opts certOptions, now time.Time) (problems []string, daysLeft int, hasExpiry bool) {
	hasExpiry = !s.NotAfter.IsZero()
	if hasExpiry {
		daysLeft = int(s.NotAfter.Sub(now).Hours() / 24)
		switch {
		case now.After(s.NotAfter):
			problems = append(problems, "expired")
		case now.Before(s.NotBefore):
			problems = append(problems, "not yet valid")
		case opts.expiresInDays > 0 && daysLeft < opts.expiresInDays:
			problems = append(problems, fmt.Sprintf("expires in %d days", daysLeft))
		}
	}
	if opts.verify && s.VerifyError != "" {
		problems = append(problems, "chain: "+s.VerifyError)
	}
	if !e.primed {
		e.primed = true
	} else {
		if opts.onAlgoChange && s.SignatureAlgorithm != e.lastAlgo {
			problems = append(problems, "signature algorithm "+e.lastAlgo+" -> "+s.SignatureAlgorithm)
		}
		if opts.onIssuerChange && s.Issuer != e.lastIssuer {
			problems = append(problems, "issuer changed")
		}
		if opts.onChange && s.Fingerprint != e.lastFP {
			problems = append(problems, "certificate changed")
		}
	}
	e.lastAlgo, e.lastIssuer, e.lastFP = s.SignatureAlgorithm, s.Issuer, s.Fingerprint
	return problems, daysLeft, hasExpiry
}
```

In the `certCheck` struct, remove the four change-detection fields (`primed`, `lastAlgo`, `lastIssuer`, `lastFP`) and add one field:

```go
	eval certEvaluator
```

In `certCheck.Run`, replace the whole inline block that computes `problems` (the `now := time.Now()` block through `c.lastAlgo, c.lastIssuer, c.lastFP = ...`, ~lines 115-147) with:

```go
	problems, daysLeft, hasExpiry := c.eval.evaluate(s, certOptions{
		expiresInDays:  c.expiresInDays,
		verify:         c.verify,
		onAlgoChange:   c.onAlgoChange,
		onIssuerChange: c.onIssuerChange,
		onChange:       c.onChange,
	}, time.Now())
```

Leave the rest of `Run` (the `ok := len(problems) > 0`, message building, `certData` call) unchanged for now — `certData` is updated in Task 3.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/checks/ -run TestCert -v`
Expected: PASS — the new evaluator test plus every existing `certCheck` test (`TestCertHealthyNoAlert`, `TestCertExpiringSoon`, `TestCertExpiredAndNotYetValid`, `TestCertVerifyError`, `TestCertAlgorithmChangeEdge`, `TestCertIssuerAndFingerprintChange`, …) confirming the refactor is behaviour-preserving.

- [ ] **Step 5: Commit**

```bash
git add internal/checks/cert.go internal/checks/cert_test.go
git commit -m "Factor cert expiry/verify/change evaluation into certEvaluator"
```

---

## Task 3: Make `certData` take plain values

So both `certCheck` and `httpCheck` build the same data map without depending on `*certCheck`.

**Files:**
- Modify: `internal/checks/cert.go` (`certData`, ~lines 177-217; and its caller in `certCheck.Run`, ~line 156)

- [ ] **Step 1: Change the signature**

Replace `func certData(c *certCheck, s CertSample, daysLeft int, hasExpiry bool) map[string]any {` with:

```go
func certData(source, host, path string, s CertSample, daysLeft int, hasExpiry bool) map[string]any {
```

Inside the body, replace the three `c.`-prefixed references:
- `"source": c.source(),` → `"source": source,`
- `if c.host != "" {` → `if host != "" {` and `data["host"] = c.host` → `data["host"] = host`
- `if c.path != "" {` → `if path != "" {` and `data["path"] = c.path` → `data["path"] = path`

- [ ] **Step 2: Update the caller in `certCheck.Run`**

Replace `res.Data = certData(c, s, daysLeft, hasExpiry)` with:

```go
	res.Data = certData(c.source(), c.host, c.path, s, daysLeft, hasExpiry)
```

- [ ] **Step 3: Run tests to verify they pass**

Run: `go test ./internal/checks/ -run TestCert -v`
Expected: PASS — `TestCertDataFields` still asserts every field, unchanged.

- [ ] **Step 4: Commit**

```bash
git add internal/checks/cert.go
git commit -m "Make certData take plain values so http check can reuse it"
```

---

## Task 4: Extend `httpCheck` to inspect the certificate

Add certificate fields to `httpCheck`, make `Run` a pointer receiver, and fold certificate problems into the pass/fail result via a `success` helper.

**Files:**
- Modify: `internal/checks/types.go` (`httpCheck` struct ~lines 46-57; `Run` ~lines 70-123)
- Test: `internal/checks/checks_test.go`

- [ ] **Step 1: Write the failing integration tests**

Add to `checks_test.go` (add `crypto/tls` and `net/url` to its import block — neither is imported yet):

```go
func TestHTTPCheckCertExpiry(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	insecure := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}

	// Threshold far in the future → the server's short-lived cert "expires soon".
	c := &httpCheck{
		base: base{name: "h", timeout: time.Second}, client: insecure, certClient: insecure,
		url: srv.URL, method: "GET", expect: statusMatcher{codes: []int{200}},
		certHost: hostOf(t, srv.URL), certOpts: certOptions{expiresInDays: 1000000},
	}
	res := c.Run(context.Background())
	if res.OK {
		t.Fatalf("a cert inside the expiry threshold must fail the http check: %q", res.Message)
	}
	if res.Data["fingerprint"] == nil {
		t.Fatalf("cert data must be exposed: %v", res.Data)
	}
}

func TestHTTPCheckCertVerifyDisabledPasses(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	insecure := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}

	c := &httpCheck{
		base: base{name: "h", timeout: time.Second}, client: insecure, certClient: insecure,
		url: srv.URL, method: "GET", expect: statusMatcher{codes: []int{200}},
		certHost: hostOf(t, srv.URL), certOpts: certOptions{verify: false},
	}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("a reachable cert with no failing assertion must pass: %q", res.Message)
	}
	if _, ok := res.Data["not_after"].(string); !ok {
		t.Fatalf("cert data must carry not_after: %v", res.Data)
	}
}

func TestHTTPCheckCertVerifyFails(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	insecure := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}

	c := &httpCheck{
		base: base{name: "h", timeout: time.Second}, client: insecure, certClient: insecure,
		url: srv.URL, method: "GET", expect: statusMatcher{codes: []int{200}},
		certHost: hostOf(t, srv.URL), certOpts: certOptions{verify: true},
	}
	if c.Run(context.Background()).OK {
		t.Fatal("verify=true against a self-signed test server must fail")
	}
}
```

Add this small helper to `checks_test.go`:

```go
func hostOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname()
}
```

Add `net/url` and `crypto/tls` to the `checks_test.go` import block.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/checks/ -run TestHTTPCheckCert -v`
Expected: FAIL — `httpCheck` has no `certClient`/`certHost`/`certOpts` fields.

- [ ] **Step 3: Extend the `httpCheck` struct**

In `types.go`, add to the `httpCheck` struct (after `expectJSON`):

```go
	// Certificate inspection (https only). certHost is non-empty when any cert_*
	// option is configured; certClient is then an InsecureSkipVerify client so
	// the leaf can be read even when expired or otherwise invalid.
	certHost   string
	certClient *http.Client
	certOpts   certOptions
	certEval   certEvaluator
```

- [ ] **Step 4: Make `Run` a pointer receiver, route the request client, and funnel successes through `success`**

Change the receiver: `func (c httpCheck) Run(ctx context.Context) Result {` → `func (c *httpCheck) Run(ctx context.Context) Result {`.

Select the request client (add right after `defer cancel()`):

```go
	client := c.client
	if c.certHost != "" {
		client = c.certClient
	}
```

Replace `resp, err := c.client.Do(req)` with `resp, err := client.Do(req)`.

Replace the two HTTP success returns with calls to the new helper:
- The early one (`if c.expectBody == "" && len(c.expectJSON) == 0 { return c.result(true, fmt.Sprintf("status %d", resp.StatusCode), start) }`) → `return c.success(resp, fmt.Sprintf("status %d", resp.StatusCode), start)`.
- The final `return c.result(true, fmt.Sprintf("status %d", resp.StatusCode), start)` → `return c.success(resp, fmt.Sprintf("status %d", resp.StatusCode), start)`.

(Leave all failure returns as `c.result(false, ...)` — a failed HTTP assertion is the primary problem and short-circuits certificate inspection.)

Add the `success` helper below `Run`:

```go
// success builds the result for a request whose HTTP assertions all passed,
// folding in certificate inspection when configured (https only). A certificate
// problem turns the otherwise-passing check into a failure, keeping the http
// check's pass/fail semantics (OK==true means healthy).
func (c *httpCheck) success(resp *http.Response, statusMsg string, start time.Time) Result {
	if c.certHost == "" || resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		return c.result(true, statusMsg, start)
	}
	leaf := resp.TLS.PeerCertificates[0]
	s := certSampleFromCert(leaf)
	if c.certOpts.verify {
		s.VerifyError = verifyCertChain(leaf, resp.TLS.PeerCertificates[1:], c.certHost)
	}
	problems, daysLeft, hasExpiry := c.certEval.evaluate(s, c.certOpts, time.Now())
	ok := len(problems) == 0
	msg := statusMsg
	if !ok {
		msg = c.certHost + ": " + strings.Join(problems, "; ")
	}
	res := c.result(ok, msg, start)
	res.Data = certData(c.certHost, c.certHost, "", s, daysLeft, hasExpiry)
	return res
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/checks/ -run 'TestHTTPCheck' -v`
Expected: PASS — the three new cert tests plus the existing `TestHTTPCheck*` tests (those construct `httpCheck` value variables and call `.Run`, which remains valid on an addressable variable with a pointer receiver).

- [ ] **Step 6: Commit**

```bash
git add internal/checks/types.go internal/checks/checks_test.go
git commit -m "Inspect server certificate in the http check on https URLs"
```

---

## Task 5: Wire `cert_*` config keys in `buildCheck`

Parse the new keys, enforce the https-only rule, build the per-check insecure client, and return a pointer.

**Files:**
- Modify: `internal/checks/build.go` (imports; the `case "http":` block ~lines 161-196)
- Test: `internal/checks/checks_test.go`

- [ ] **Step 1: Write the failing build tests**

Add to `checks_test.go`:

```go
func TestBuildHTTPCertRequiresHTTPS(t *testing.T) {
	built, warns := Build(map[string]any{
		"h": map[string]any{"type": "http", "url": "http://example.com", "cert_expires_in_days": 14},
	}, Deps{DefaultTimeout: time.Second})
	if len(built) != 0 || len(warns) == 0 {
		t.Fatalf("cert_* on an http url must warn and build nothing: built=%d warns=%v", len(built), warns)
	}
	if !strings.Contains(warns[0], "https") {
		t.Fatalf("warning should mention https: %q", warns[0])
	}
}

func TestBuildHTTPSCertActivates(t *testing.T) {
	built, warns := Build(map[string]any{
		"h": map[string]any{"type": "http", "url": "https://example.com", "cert_expires_in_days": 14},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("a valid https cert check must build cleanly: warns=%v", warns)
	}
	hc, ok := built[0].Check.(*httpCheck)
	if !ok {
		t.Fatalf("http check must build to *httpCheck, got %T", built[0].Check)
	}
	if hc.certHost != "example.com" || hc.certClient == nil || hc.certOpts.expiresInDays != 14 {
		t.Fatalf("cert inspection not wired: host=%q client=%v opts=%+v", hc.certHost, hc.certClient, hc.certOpts)
	}
	if !hc.certOpts.verify {
		t.Fatal("cert_verify must default to true when inspection is active")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/checks/ -run 'TestBuildHTTP' -v`
Expected: FAIL — no scheme check yet; `built[0].Check` is a value `httpCheck`, not `*httpCheck`.

- [ ] **Step 3: Add imports**

In `build.go`, add `"crypto/tls"` and `"net/url"` to the import block.

- [ ] **Step 4: Rewrite the `case "http":` block**

Rename the local `url` variable to `rawURL` (it would otherwise shadow the `net/url` package), and append the certificate wiring. The block becomes:

```go
	case "http":
		rawURL := asString(entry["url"])
		if rawURL == "" {
			return nil, "http check requires a url"
		}
		method := strings.ToUpper(asString(entry["method"]))
		if method == "" {
			method = http.MethodGet
		}
		expect, err := parseStatusMatcher(entry["expect_status"])
		if err != nil {
			return nil, "http check: " + err.Error()
		}
		var body []byte
		contentType := ""
		if j, ok := entry["json"]; ok && j != nil {
			raw, err := json.Marshal(j)
			if err != nil {
				return nil, "http check: invalid json body: " + err.Error()
			}
			body, contentType = raw, "application/json"
		} else if s := asString(entry["body"]); s != "" {
			body = []byte(s)
		}
		hc := &httpCheck{
			base:        b,
			client:      client,
			url:         rawURL,
			method:      method,
			headers:     stringMap(entry["headers"]),
			body:        body,
			contentType: contentType,
			expect:      expect,
			expectBody:  asString(entry["expect_body"]),
			expectJSON:  parseJSONAssertions(entry["expect_json"]),
		}
		if warn := configureHTTPCert(hc, entry, rawURL); warn != "" {
			return nil, warn
		}
		return hc, ""
```

Add a helper (place it near `buildCheck`, e.g. just below it):

```go
// httpCertKeys are the optional certificate-inspection keys on the http check.
var httpCertKeys = []string{
	"cert_expires_in_days", "cert_verify",
	"cert_on_change", "cert_on_issuer_change", "cert_on_algorithm_change",
}

// configureHTTPCert enables certificate inspection on hc when any cert_* key is
// present. It requires an https url and returns a warning string on a config
// error (empty when there is nothing to configure or configuration succeeded).
func configureHTTPCert(hc *httpCheck, entry map[string]any, rawURL string) string {
	active := false
	for _, k := range httpCertKeys {
		if _, ok := entry[k]; ok {
			active = true
			break
		}
	}
	if !active {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "http check: invalid url: " + err.Error()
	}
	if u.Scheme != "https" {
		return "http check: cert_* options require an https url"
	}
	verify := true
	if v, ok := entry["cert_verify"].(bool); ok {
		verify = v
	}
	days := 0
	if v, ok := intField(entry["cert_expires_in_days"]); ok {
		days = v
	}
	hc.certHost = u.Hostname()
	hc.certOpts = certOptions{
		expiresInDays:  days,
		verify:         verify,
		onAlgoChange:   asBool(entry["cert_on_algorithm_change"]),
		onIssuerChange: asBool(entry["cert_on_issuer_change"]),
		onChange:       asBool(entry["cert_on_change"]),
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // leaf inspected and verified manually via verifyCertChain
	hc.certClient = &http.Client{Transport: tr}
	return ""
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/checks/ -run 'TestBuildHTTP|TestHTTPCheck' -v`
Expected: PASS.

- [ ] **Step 6: Run the whole package**

Run: `go test ./internal/checks/`
Expected: PASS (no regressions anywhere in the checks package).

- [ ] **Step 7: Commit**

```bash
git add internal/checks/build.go internal/checks/checks_test.go
git commit -m "Wire cert_* config keys into the http check (https only)"
```

---

## Task 6: Document the new options

**Files:**
- Modify: `docs/configuration.md` (the `http` check section)

- [ ] **Step 1: Find the http check docs**

Run: `grep -n "http" docs/configuration.md | head`
Locate the section describing the `http` check's keys (`url`, `method`, `expect_status`, …).

- [ ] **Step 2: Add the certificate options**

Document, in the same style as the surrounding keys, that on an `https://` URL the `http` check accepts:
- `cert_expires_in_days` — alert when the certificate expires within N days (or is expired / not yet valid).
- `cert_verify` — verify chain + hostname (default `true` when any `cert_*` key is set).
- `cert_on_change`, `cert_on_issuer_change`, `cert_on_algorithm_change` — alert on fingerprint / issuer / signature-algorithm change between cycles.

Note: these keys require an `https` URL (a build warning is emitted otherwise); a certificate problem fails the `http` check, and the certificate fields are added to the result data. Mention that the standalone `cert` check remains the tool for raw TLS endpoints and local certificate files.

- [ ] **Step 3: Commit**

```bash
git add docs/configuration.md
git commit -m "Document http check certificate (cert_*) options"
```

---

## Task 7: Full verification before finishing

**Files:** none (verification only)

- [ ] **Step 1: Format check**

Run: `gofmt -l ./internal ./cmd`
Expected: prints nothing.

- [ ] **Step 2: Build and test**

Run: `go build ./... && go test ./...`
Expected: all pass.

- [ ] **Step 3: Static analysis (per `AGENTS.md`)**

```sh
export PATH="$HOME/go/bin:$PATH"
govulncheck ./...
staticcheck ./...
revive -config revive.toml ./...
golangci-lint run
```
Expected: no findings from any tool. In particular confirm the single `//nolint:gosec` on the `InsecureSkipVerify` line in `build.go` is accepted and justified (the certificate is inspected and verified manually via `verifyCertChain`), consistent with the existing `//nolint:gosec` on `defaultCertSampler`.

- [ ] **Step 4: Final commit (only if any formatting/lint fix was needed)**

```bash
git add -A
git commit -m "Satisfy gofmt/static analysis for https cert inspection"
```

---

## Notes / decisions captured from the spec

- **Why `InsecureSkipVerify` on the request client:** a verifying client aborts the TLS handshake on an expired/invalid certificate, so `resp.TLS` would be unreadable — defeating expiry monitoring. The check therefore skips transport-level verification and verifies manually via `verifyCertChain`, exactly as the standalone `cert` check's `defaultCertSampler` does.
- **`cert_verify: false` alone** activates inspection with no failing assertion — a harmless no-op that still switches to the insecure client. Intentional (covered by `TestHTTPCheckCertVerifyDisabledPasses`).
- **No `server_name` override key** is added; the `ServerName` for verification is the host parsed from `url`.
- **Pass/fail vs condition-style:** certificate problems are folded into the `http` check as additional failure reasons; the `http` check keeps `OK==true` meaning healthy (opposite of the standalone `cert` check).
