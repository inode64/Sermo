package operation

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx/execxtest"
)

func TestGuardPreservesReferencedBuildIssues(t *testing.T) {
	for _, section := range []string{"checks", "preflight"} {
		for _, optional := range []bool{false, true} {
			for _, condition := range []string{"active", "failed", "not"} {
				t.Run(section+"/"+condition+"/optional="+strconv.FormatBool(optional), func(t *testing.T) {
					operand := map[string]any{"active": map[string]any{"check": "invalid"}}
					if condition == "not" {
						operand = map[string]any{"not": operand}
					} else {
						operand = map[string]any{condition: map[string]any{"check": "invalid"}}
					}
					tree := guardBuildTree(operand)
					tree[section] = map[string]any{"invalid": map[string]any{
						"type": "log", "path": "/var/log/app.log", "regex": "(",
						"count": map[string]any{"op": ">", "value": 1}, "within": "5m", "optional": optional,
					}}
					guard := guardClosure(tree, checks.Deps{DefaultTimeout: time.Second}, nil, nil)
					blocked, _, err := guard(t.Context(), "restart")
					if blocked || err == nil || !strings.Contains(err.Error(), "regex is invalid") || strings.Contains(err.Error(), "unknown check") {
						t.Fatalf("malformed reference: blocked=%v err=%v, want original build error", blocked, err)
					}
				})
			}
		}
	}
}

func TestGuardBuildIssuesPreserveReferencePrecedence(t *testing.T) {
	for _, validCheck := range []bool{false, true} {
		t.Run("valid-check="+strconv.FormatBool(validCheck), func(t *testing.T) {
			tree := guardBuildTree(map[string]any{"failed": map[string]any{"check": "probe"}})
			valid := map[string]any{"type": "command", "command": []any{"probe"}}
			invalid := map[string]any{"type": "command"}
			check, preflight := invalid, valid
			if validCheck {
				check, preflight = valid, invalid
			}
			tree["checks"] = map[string]any{"probe": check, "unreferenced": invalid}
			tree["preflight"] = map[string]any{"probe": preflight}
			runner := execxtest.Outputs("ok")
			guard := guardClosure(tree, checks.Deps{Runner: runner, DefaultTimeout: time.Second}, nil, nil)
			blocked, _, err := guard(t.Context(), "restart")
			if validCheck {
				if blocked || err != nil || len(runner.Calls()) != 1 {
					t.Fatalf("valid service check must win: blocked=%v err=%v calls=%v", blocked, err, runner.Calls())
				}
			} else if blocked || err == nil || !strings.Contains(err.Error(), "command") || len(runner.Calls()) != 0 {
				t.Fatalf("invalid service check must not fall back to preflight: blocked=%v err=%v calls=%v", blocked, err, runner.Calls())
			}
		})
	}
}

func guardBuildTree(condition map[string]any) map[string]any {
	return map[string]any{"rules": map[string]any{"guard": map[string]any{
		"type": "guard", "blocks": []any{"restart"}, "if": condition,
		"then": map[string]any{"action": "block", "message": "busy"},
	}}}
}
