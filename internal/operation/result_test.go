package operation

import "testing"

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
		{ResultPreflightFailed, false},
	}
	for _, tc := range cases {
		r := Result{Status: tc.status}
		if got := r.RecordsRemediation(); got != tc.want {
			t.Errorf("status %q: RecordsRemediation() = %v, want %v", tc.status, got, tc.want)
		}
	}
}
