package config

import "testing"

func TestOperationGroupValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  any
		ok   bool
	}{
		{name: "group", raw: "rpc", ok: true},
		{name: "none", raw: "", ok: true},
		{name: "boolean", raw: false},
		{name: "mapping", raw: map[string]any{}},
		{name: "parent", raw: ".."},
		{name: "slash", raw: "rpc/statd"},
		{name: "backslash", raw: `rpc\statd`},
		{name: "nul", raw: "rpc\x00statd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateOperationGroup(map[string]any{ServiceKeyOperationGroup: tc.raw}); (err == nil) != tc.ok {
				t.Fatalf("validation: %v", err)
			}
		})
	}
}

func TestValidateServiceRejectsUnsafeOperationGroup(t *testing.T) {
	mustHave(t, validateService(t, "name: s\noperation_group: ../rpc\n"), ServiceKeyOperationGroup)
	mustHave(t, validateService(t, "name: s\noperation_group: false\n"), ServiceKeyOperationGroup)
}
