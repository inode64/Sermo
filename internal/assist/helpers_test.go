package assist

import (
	"strings"
	"testing"
)

func testEnv() Env {
	return Env{
		Notifiers: []string{"ops-email", "team-slack"},
		Volumes: func() ([]Volume, error) {
			return []Volume{
				{Mountpoint: "/mnt/backup", FSType: "ext4", Device: "/dev/mapper/vg0-data"},
				{Mountpoint: "/", FSType: "xfs", Device: "/dev/sda1"},
			}, nil
		},
		Ifaces: func() ([]Iface, error) {
			return []Iface{
				{Name: "eth0", Up: true},
				{Name: "lo", Up: true, Loopback: true},
			}, nil
		},
	}
}

func testEnvWithDefaultNotify() Env {
	env := testEnv()
	env.DefaultNotify = []string{"ops-email"}
	return env
}

// runAssistant drives an assistant with newline-delimited answers and captures
// its result and operator-facing output.
func runAssistant(t *testing.T, assistant Assistant, env Env, steps ...string) (Result, string) {
	t.Helper()
	var out strings.Builder
	p := NewPrompt(strings.NewReader(strings.Join(steps, "\n")+"\n"), &out)
	result, err := assistant.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return result, out.String()
}
