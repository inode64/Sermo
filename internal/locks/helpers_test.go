package locks

import "testing"

// lockID joins service and (optional) name with a dot; an empty name yields just
// the service.
func TestLockID(t *testing.T) {
	if got := LockID("svc", ""); got != "svc" {
		t.Errorf("LockID(svc, \"\") = %q, want \"svc\"", got)
	}
	if got := LockID("svc", "default"); got != "svc.default" {
		t.Errorf("LockID(svc, default) = %q, want \"svc.default\"", got)
	}
}

func TestValidateIdentifier(t *testing.T) {
	cases := []struct {
		name       string
		value      string
		allowEmpty bool
		wantErr    bool
	}{
		{"empty disallowed", "", false, true},
		{"dot segment", ".", false, true},
		{"parent dot segment", "..", false, true},
		{"backslash separator", `a\b`, false, true},
		{"simple name", "deploy", false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateIdentifier("lock name", c.value, c.allowEmpty)
			if (err != nil) != c.wantErr {
				t.Fatalf("validateIdentifier(%q) err=%v wantErr=%v", c.value, err, c.wantErr)
			}
		})
	}
}
