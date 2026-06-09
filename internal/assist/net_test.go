package assist

import (
	"strings"
	"testing"
)

func TestNetAssistantStateAndErrors(t *testing.T) {
	// Select iface 1 (eth0; lo is filtered out); monitor state+errors;
	// state on any change; errors threshold 100; notifier 1 (ops-email).
	script := strings.Join([]string{
		"1",   // MultiChoose interfaces -> eth0
		"1,2", // metrics: link up/down + link errors
		"1",   // state: any change
		"100", // errors threshold
		"1",   // notifier ops-email
	}, "\n") + "\n"

	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := netAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry, ok := res.Watches["net-eth0"].(map[string]any)
	if !ok {
		t.Fatalf("expected watch net-eth0, got %v", res.Watches)
	}
	if entry["check"].(map[string]any)["interface"] != "eth0" {
		t.Fatalf("check = %v", entry["check"])
	}
	metrics := entry["metrics"].(map[string]any)
	state := metrics["state"].(map[string]any)
	if state["on"] != "change" {
		t.Fatalf("state = %v, want on:change", state)
	}
	if state["then"].(map[string]any)["notify"].([]string)[0] != "ops-email" {
		t.Fatalf("state.then = %v", state["then"])
	}
	errs := metrics["errors"].(map[string]any)
	delta := errs["delta"].(map[string]any)
	if delta["op"] != ">" || delta["value"] != 100 {
		t.Fatalf("errors.delta = %v", delta)
	}
	if _, hasSpeed := metrics["speed"]; hasSpeed {
		t.Fatalf("speed must not be present: %v", metrics)
	}
}

func TestNetAssistantStateDownOnly(t *testing.T) {
	// Select eth0; only state; "only when down"; notifier 2.
	script := strings.Join([]string{"1", "1", "2", "2"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := netAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	state := res.Watches["net-eth0"].(map[string]any)["metrics"].(map[string]any)["state"].(map[string]any)
	if state["expect"] != "down" {
		t.Fatalf("state = %v, want expect:down", state)
	}
}

func TestNetAssistantRequiresNotifier(t *testing.T) {
	env := testEnv()
	env.Notifiers = nil
	script := strings.Join([]string{"1", "1", "1"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	if _, err := (netAssistant{}).Run(p, env); err == nil {
		t.Fatal("a net watch with no notifier must error (no hook/expand offered)")
	}
}
