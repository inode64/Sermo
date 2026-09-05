//go:build semgreptest

// Package tests holds fixtures that prove every rule in .semgrep/rules/ still
// fires. `semgrep --test` matches each `ruleid:` annotation against a real
// finding and each `ok:` line against the absence of one, and exits non-zero
// when they disagree — so a rule that silently stops matching fails the build
// instead of passing as a no-op, which is how govet and revive were dormant
// here for a long time.
//
// Go tooling never sees this file: the toolchain skips directories whose name
// begins with a dot, and the build tag keeps it out of any explicit build.
package tests

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/httpx"
	"sermo/internal/netutil"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
	"sermo/internal/strutil"
)

func compareManagers(a, b servicemgr.Manager) bool {
	// ruleid: uncomparable-servicemgr-manager
	return a == b
}

func compareManagerToNil(a servicemgr.Manager) bool {
	// ok: uncomparable-servicemgr-manager
	return a == nil
}

// WebBackend stands in for the real one; semgrep parses rather than compiles.
type WebBackend struct{}

func (b *WebBackend) serviceWarningReason(name string) string {
	// ruleid: web-request-must-not-discover-processes
	_, _ = process.PIDsByComm(name)
	return ""
}

func mutateSharedSnapshot(r *process.CachingReader, pid int) {
	// ruleid: shared-process-snapshot-must-not-be-mutated
	snap := r.Snapshot()
	delete(snap, pid)
}

func readSharedSnapshot(r *process.CachingReader, pid int) bool {
	// ok: shared-process-snapshot-must-not-be-mutated
	snap := r.Snapshot()
	_, ok := snap[pid]
	return ok
}

type Blocker struct{ Cmdline string }

func redactInPlace(items []Blocker) []Blocker {
	for i := range items {
		// ruleid: redactor-must-not-mutate-its-argument
		items[i].Cmdline = ""
	}
	return items
}

// ok: redactor-must-not-mutate-its-argument
func redactViaHelper(items []Blocker) []Blocker {
	return redactCloned(items, func(b *Blocker) { b.Cmdline = "" })
}

func redactCloned[T any](items []T, redact func(*T)) []T { return items }

func bypassServiceStart(m servicemgr.Manager, ctx context.Context) error {
	// ruleid: service-lifecycle-must-use-operation
	return m.Start(ctx, "nginx")
}

func serviceStatusIsReadOnly(m servicemgr.Manager, ctx context.Context) (servicemgr.ServiceStatus, error) {
	// ok: service-lifecycle-must-use-operation
	return m.Status(ctx, "nginx")
}

func bypassKill(pid int) error {
	// ruleid: syscall-kill-must-use-process-signaler
	return syscall.Kill(pid, syscall.SIGTERM)
}

func pidLivenessProbe(pid int) error {
	// ok: syscall-kill-must-use-process-signaler
	return syscall.Kill(pid, 0)
}

func parseYAMLDuration(raw string) (time.Duration, error) {
	// ruleid: yaml-duration-must-use-cfgval
	return time.ParseDuration(raw)
}

func csrfHeaderLiteral() string {
	// ruleid: daemon-http-contract-literals
	return "X-Sermo-Csrf"
}

func generationHeaderName() string {
	// ok: daemon-http-contract-literals
	return "invalid X-Sermo-Generation header"
}

func nestedUniqueThenMerge(primary string, fallbacks []string) []string {
	// ruleid: strutil-must-not-merge-unique-of-unique
	return strutil.MergeUnique(strutil.Unique([]string{primary}), fallbacks...)
}

func uniqueCombinedPidfiles(primary string, fallbacks []string) []string {
	// ok: strutil-must-not-merge-unique-of-unique
	return strutil.Unique(append([]string{primary}, fallbacks...))
}

func mergeCompositeSeed(primary string, fallbacks []string) []string {
	// ruleid: strutil-must-not-merge-composite-list
	return strutil.MergeUnique([]string{primary}, fallbacks...)
}

func mergeExistingList(list, extra []string) []string {
	// ok: strutil-must-not-merge-composite-list
	return strutil.MergeUnique(list, extra...)
}

func identityWrap(err error) error {
	// ruleid: error-wrap-must-add-context
	return fmt.Errorf("%w", err)
}

func wrapWithStep(err error) error {
	// ok: error-wrap-must-add-context
	return fmt.Errorf("web bind: %w", err)
}

func skipDisabledInline(entry map[string]any) {
	// ruleid: enabled-must-use-cfgval-disabled
	if v, ok := entry["enabled"].(bool); ok && !v {
		return
	}
}

func skipDisabledOwner(entry map[string]any) {
	// ok: enabled-must-use-cfgval-disabled
	if cfgval.Disabled(entry) {
		return
	}
}

func skipDisabledTelegramKey(entry map[string]any) {
	// ruleid: enabled-must-use-cfgval-disabled
	if v, ok := entry[telegrambot.KeyEnabled].(bool); ok && !v {
		return
	}
}

func skipVerifyLiteral() *tls.Config {
	// ruleid: tls-skip-verify-must-use-netutil
	return &tls.Config{InsecureSkipVerify: true}
}

func skipVerifyOwner(host string) *tls.Config {
	// ok: tls-skip-verify-must-use-netutil
	return netutil.TLSClientConfigForMode(host, netutil.TLSModeSkipVerify)
}

func rawClientDo(c *http.Client, req *http.Request) (*http.Response, error) {
	// ruleid: http-client-do-must-use-httpx
	return c.Do(req)
}

func httpxDo(c httpx.Doer, req *http.Request) (*http.Response, error) {
	// ok: http-client-do-must-use-httpx
	return httpx.Do(c, req)
}

func raw2xx(code int) bool {
	// ruleid: http-2xx-must-use-httpx
	return code/100 == 2
}

func successStatus(code int) bool {
	// ok: http-2xx-must-use-httpx
	return httpx.SuccessStatus(code)
}

func rawErrorBody(resp *http.Response) string {
	// ruleid: http-error-body-must-use-httpx
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return strings.TrimSpace(string(body))
}

func useErrorBody(resp *http.Response) string {
	// ok: http-error-body-must-use-httpx
	return httpx.ErrorBody(resp, httpx.ErrorBodyLimit)
}
