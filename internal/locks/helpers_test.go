package locks

import "testing"

// lockID joins service and (optional) name with a dot; an empty name yields just
// the service.
func TestLockID(t *testing.T) {
	if got := lockID("svc", ""); got != "svc" {
		t.Errorf("lockID(svc, \"\") = %q, want \"svc\"", got)
	}
	if got := lockID("svc", "default"); got != "svc.default" {
		t.Errorf("lockID(svc, default) = %q, want \"svc.default\"", got)
	}
}

// orDefault returns the fallback only for an empty value.
func TestOrDefault(t *testing.T) {
	if got := orDefault("", "fb"); got != "fb" {
		t.Errorf("orDefault(\"\", fb) = %q, want \"fb\"", got)
	}
	if got := orDefault("value", "fb"); got != "value" {
		t.Errorf("orDefault(value, fb) = %q, want \"value\"", got)
	}
}
