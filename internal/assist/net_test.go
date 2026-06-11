package assist

import (
	"strings"
	"testing"
)

func TestNetAssistantStateAndErrors(t *testing.T) {
	// Select iface 1 (eth0; lo is filtered out); monitor state+errors;
	// state on any change; errors threshold 100; notifier ops-email.
	script := strings.Join([]string{
		"1",   // MultiChoose interfaces -> eth0
		"1,2", // metrics: link up/down + link errors
		"1",   // state: any change
		"100", // errors threshold
		"2",   // notifier ops-email
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
	// Select eth0; only state; "only when down"; notifier team-slack.
	script := strings.Join([]string{"1", "1", "2", "3"}, "\n") + "\n"
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

func TestNetAssistantInheritsGlobalNotify(t *testing.T) {
	// Select eth0; only state; any change; inherit global notify.
	script := strings.Join([]string{"1", "1", "1", "4"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := netAssistant{}.Run(p, testEnvWithDefaultNotify())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	state := res.Watches["net-eth0"].(map[string]any)["metrics"].(map[string]any)["state"].(map[string]any)
	then := state["then"].(map[string]any)
	if _, hasNotify := then["notify"]; hasNotify {
		t.Fatalf("notify should be omitted to inherit global default: %v", then)
	}
}

func TestNetAssistantRequiresNotifier(t *testing.T) {
	env := testEnv()
	env.Notifiers = nil
	// Select default notify, but no global default is configured.
	script := strings.Join([]string{"1", "1", "1", "2"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	if _, err := (netAssistant{}).Run(p, env); err == nil {
		t.Fatal("a net watch with no notifier must error (no hook/expand offered)")
	}
}

func TestNetAssistantNotifyByName(t *testing.T) {
	// The shared all/none/default vocabulary works in the net wizard too:
	// notifiers can be picked by name, and "default" inherits the global default.
	t.Run("notifier by name", func(t *testing.T) {
		// Select eth0; only state; any change; type the notifier name.
		script := strings.Join([]string{"1", "1", "1", "team-slack"}, "\n") + "\n"
		p := NewPrompt(strings.NewReader(script), &strings.Builder{})
		res, err := netAssistant{}.Run(p, testEnv())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		then := res.Watches["net-eth0"].(map[string]any)["metrics"].(map[string]any)["state"].(map[string]any)["then"].(map[string]any)
		notify := then["notify"].([]string)
		if len(notify) != 1 || notify[0] != "team-slack" {
			t.Fatalf("notify = %v, want [team-slack]", notify)
		}
	})

	t.Run("default by name", func(t *testing.T) {
		// Select eth0; only state; any change; type "default" to inherit.
		script := strings.Join([]string{"1", "1", "1", "default"}, "\n") + "\n"
		p := NewPrompt(strings.NewReader(script), &strings.Builder{})
		res, err := netAssistant{}.Run(p, testEnvWithDefaultNotify())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		then := res.Watches["net-eth0"].(map[string]any)["metrics"].(map[string]any)["state"].(map[string]any)["then"].(map[string]any)
		if _, hasNotify := then["notify"]; hasNotify {
			t.Fatalf("'default' should omit notify to inherit the global default: %v", then)
		}
	})
}

func TestNetAssistantNotifyNoneErrors(t *testing.T) {
	// Select eth0; only state; any change; explicit none.
	script := strings.Join([]string{"1", "1", "1", "1"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	if _, err := (netAssistant{}).Run(p, testEnv()); err == nil {
		t.Fatal("a net watch with notify none should error because it has no other action")
	}
}
