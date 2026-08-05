package config

import "testing"

func restartPolicyServiceYAML(policy string) string {
	return `
name: svc
service: svc
policy:
  cooldown: 5m
` + policy
}

func TestRestartModeDefaultsToStaged(t *testing.T) {
	t.Parallel()

	for _, tree := range []map[string]any{nil, map[string]any{}} {
		got, err := ParseRestartMode(tree)
		if err != nil {
			t.Fatalf("ParseRestartMode(%v): %v", tree, err)
		}
		if got != RestartModeStaged {
			t.Fatalf("ParseRestartMode(%v) = %q, want %q", tree, got, RestartModeStaged)
		}
	}
}

func TestValidateRestartPolicyModes(t *testing.T) {
	t.Parallel()

	for _, mode := range []RestartMode{RestartModeStaged, RestartModeNative} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()

			issues := validateService(t, restartPolicyServiceYAML("restart_policy: { mode: "+string(mode)+" }\n"))
			mustNotHave(t, issues, ServiceKeyRestartPolicy)
		})
	}
}

func TestValidateRestartPolicyShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		policy string
		want   string
	}{
		{name: "mapping required", policy: "restart_policy: native\n", want: "restart_policy must be a mapping"},
		{name: "mode required", policy: "restart_policy: {}\n", want: `restart_policy.mode "" is not one of staged, native`},
		{name: "mode must be known", policy: "restart_policy: { mode: atomic }\n", want: `restart_policy.mode "atomic" is not one of staged, native`},
		{name: "unknown key", policy: "restart_policy: { mode: staged, typo: true }\n", want: "restart_policy.typo is not supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mustHave(t, validateService(t, restartPolicyServiceYAML(tt.policy)), tt.want)
		})
	}
}

func TestValidateNativeRestartPolicyRejectsExternalControl(t *testing.T) {
	t.Parallel()

	yaml := restartPolicyServiceYAML(`restart_policy: { mode: native }
control:
  type: docker
  container: svc
`)
	mustHave(t, validateService(t, yaml), `restart_policy.mode="native" is not supported with control`)
}

func TestValidateNativeRestartPolicyAllowsAuxiliaryInitUnits(t *testing.T) {
	t.Parallel()

	yaml := restartPolicyServiceYAML(`restart_policy: { mode: native }
also_service:
  systemd: [ svc.socket ]
`)
	mustNotHave(t, validateService(t, yaml), ServiceKeyRestartPolicy)
}
