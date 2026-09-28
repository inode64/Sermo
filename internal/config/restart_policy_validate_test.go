package config

import "testing"

func TestRestartPolicyIsRetired(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"native", "staged"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			mustHave(t, validateService(t, "name: demo\npolicy: {cooldown: 1m}\nrestart_policy: {mode: "+mode+"}\n"), "restart_policy is no longer supported")
		})
	}
}
