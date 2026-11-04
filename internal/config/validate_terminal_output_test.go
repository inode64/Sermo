package config

import "testing"

func TestValidateTerminalOutputNativeAction(t *testing.T) {
	for _, name := range []string{"valid", "wrong type", "no cooldown", "zero cooldown", "relative log", "unclean log", "unknown action option", "not mapping", "no selector", "delegated"} {
		t.Run(name, func(t *testing.T) {
			selector := map[string]any{"exe": "/usr/bin/fixture-driver", "user": "root"}
			check := map[string]any{"type": "terminal_output", "processes": map[string]any{"main": selector}}
			action := map[string]any{"log_file": "/var/log/sermo/output.log"}
			then := map[string]any{"drain_output": action}
			watch := map[string]any{"check": check, "then": then, "policy": map[string]any{"cooldown": "5m"}}
			switch name {
			case "wrong type":
				watch["check"] = map[string]any{"type": "tcp", "host": "127.0.0.1", "port": 22}
			case "no cooldown":
				delete(watch, "policy")
			case "zero cooldown":
				watch["policy"] = map[string]any{"cooldown": "0s"}
			case "relative log":
				action["log_file"] = "run/output.log"
			case "unclean log":
				action["log_file"] = "/var/log/../output.log"
			case "unknown action option":
				action["signal"] = "KILL"
			case "not mapping":
				then["drain_output"] = true
			case "no selector":
				delete(check, "processes")
			case "delegated":
				selector["delegated"] = true
			}
			tree := map[string]any{"watches": map[string]any{"recover-output": watch}}
			if name == "valid" {
				assertNoWatchIssues(t, tree)
			} else {
				assertWatchIssues(t, tree, "")
			}
		})
	}
}
