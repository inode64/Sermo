package operation

import (
	"testing"

	"sermo/internal/checks"
)

func TestRecordsRemediation(t *testing.T) {
	cases := []struct {
		status ResultStatus
		want   bool
	}{
		{ResultOK, true},
		{ResultFailed, true},
		{ResultPostflightFailed, true},
		{ResultOrphanProcesses, true},
		{ResultBlocked, false},
		{ResultSkipped, false},
		{ResultPreflightFailed, false},
	}
	for _, tc := range cases {
		r := Result{Status: tc.status}
		if got := r.RecordsRemediation(); got != tc.want {
			t.Errorf("status %q: RecordsRemediation() = %v, want %v", tc.status, got, tc.want)
		}
	}
}

// The event of a refused or incomplete operation says which checks failed and
// why, required and optional apart; the passing ones add nothing.
func TestAuditMessageNamesTheFailedChecks(t *testing.T) {
	r := Result{Status: ResultPreflightFailed, Message: "preflight failed", Checks: []checks.Result{
		{Check: "ceph-mon-binary", OK: true, Message: "/usr/bin/ceph-mon is executable"},
		{Check: "ceph-version", Message: "exit 1 (want 0): ModuleNotFoundError: No module named 'prettytable'"},
		{Check: "update-libraries", Optional: true, Message: "libfoo.so.1 not found"},
	}}
	const want = "preflight failed; check ceph-version failed: exit 1 (want 0): ModuleNotFoundError: No module named 'prettytable'" +
		"; optional check update-libraries failed: libfoo.so.1 not found"
	if got := r.AuditMessage(); got != want {
		t.Fatalf("AuditMessage() = %q, want %q", got, want)
	}
}
