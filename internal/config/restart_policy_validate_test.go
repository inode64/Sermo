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

	if got := RestartMode(nil); got != RestartModeStaged {
		t.Fatalf("RestartMode(nil) = %q, want %q", got, RestartModeStaged)
	}
	if got := RestartMode(map[string]any{}); got != RestartModeStaged {
		t.Fatalf("RestartMode(empty) = %q, want %q", got, RestartModeStaged)
	}
}

func TestValidateRestartPolicyModes(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{RestartModeStaged, RestartModeNative} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			issues := validateService(t, restartPolicyServiceYAML("restart_policy: { mode: "+mode+" }\n"))
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

func TestValidateNativeRestartPolicyRejectsUnsupportedControlShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		extra string
		want  string
	}{
		{
			name: "external control",
			extra: `control:
  type: docker
  container: svc
`,
			want: `restart_policy.mode="native" is not supported with control`,
		},
		{
			name: "auxiliary init units",
			extra: `also_service:
  systemd: [ svc.socket ]
`,
			want: `restart_policy.mode="native" is not supported with also_service`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			yaml := restartPolicyServiceYAML("restart_policy: { mode: native }\n" + tt.extra)
			mustHave(t, validateService(t, yaml), tt.want)
		})
	}
}
