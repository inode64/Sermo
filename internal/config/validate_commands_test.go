package config

import (
	"fmt"
	"strings"
	"testing"
)

// TestValidateCommandsExpectations checks that a named command validates its
// expect_exit and expect_stdout/expect_stderr matchers.
func TestValidateCommandsExpectations(t *testing.T) {
	var issues []string
	add := func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) }

	tree := map[string]any{"commands": map[string]any{
		"version": map[string]any{
			"command":       []any{"/bin/tool"},
			"user":          "",
			"expect_exit":   "nope",                                   // not an int
			"expect_stdout": map[string]any{"op": "=>", "value": "1"}, // invalid op
			"expect_stderr": 42,                                       // wrong shape
			"on_change":     "yes",
			"version_match": map[string]any{"rejects": "MariaDB"},
		},
	}}
	validateCommands(tree, add)

	joined := strings.Join(issues, "\n")
	for _, want := range []string{
		"commands.version user must be a non-empty string",
		"commands.version expect_exit must be an integer or a non-empty list of integers",
		"commands.version.expect_stdout op",
		"commands.version.expect_stderr must be a string substring or an {op, value} mapping",
		"commands.version.on_change must be a boolean",
		"commands.version.version_match unknown key",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing issue %q in:\n%s", want, joined)
		}
	}
}

// TestValidateCommandsValid accepts a well-formed command with output matchers.
func TestValidateCommandsValid(t *testing.T) {
	var issues []string
	add := func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) }

	tree := map[string]any{"commands": map[string]any{
		"version": map[string]any{
			"command":       []any{"/bin/tool", "--version"},
			"user":          "postgres",
			"expect_exit":   []any{0, 1},
			"expect_stdout": "v1.",
			"expect_stderr": map[string]any{"op": "==", "value": ""},
			"on_change":     true,
			"version_match": map[string]any{"excludes": "MariaDB"},
			"export": map[string]any{
				"raw": map[string]any{"from": "stdout", "trim": true, "regex": `([0-9.]+)`, "default": "unknown"},
			},
		},
	}}
	validateCommands(tree, add)
	if len(issues) != 0 {
		t.Errorf("expected no issues, got: %v", issues)
	}
}

// assertCheckSectionIssue runs validateCheckSection over a single named check and
// asserts want appears in the reported issues.
func assertCheckSectionIssue(t *testing.T, name string, check map[string]any, want string) {
	t.Helper()
	var issues []string
	add := func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) }
	validateCheckSection(map[string]any{"checks": map[string]any{name: check}}, "checks", "/run/sermo/locks", add)
	if joined := strings.Join(issues, "\n"); !strings.Contains(joined, want) {
		t.Fatalf("missing issue %q in:\n%s", want, joined)
	}
}

func TestValidateCommandRejectsEmptyArgvItem(t *testing.T) {
	assertCheckSectionIssue(t, "version",
		map[string]any{"type": "command", "command": []any{""}},
		"checks.version command must be an array, not a shell string")
}

func TestValidateCommandCheckUser(t *testing.T) {
	assertCheckSectionIssue(t, "config",
		map[string]any{"type": "command", "command": []any{"/bin/tool"}, "user": []any{"postgres"}},
		"checks.config user must be a non-empty string")
}

func TestValidateCommandsExport(t *testing.T) {
	var issues []string
	add := func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) }

	tree := map[string]any{"commands": map[string]any{
		"version": map[string]any{
			"command": []any{"/bin/tool", "--version"},
			"export": map[string]any{
				"bad.name": map[string]any{},
				"stderr":   map[string]any{"from": "log"},
				"trim":     map[string]any{"trim": "yes"},
				"regex":    map[string]any{"regex": "["},
				"empty_re": map[string]any{"regex": ""},
				"shape":    "stdout",
				"nil":      nil,
			},
		},
	}}
	validateCommands(tree, add)

	joined := strings.Join(issues, "\n")
	for _, want := range []string{
		`commands.version.export variable "bad.name" must be a simple variable name`,
		"commands.version.export.stderr.from must be stdout or stderr",
		"commands.version.export.trim.trim must be a boolean",
		"commands.version.export.regex.regex is invalid",
		"commands.version.export.empty_re.regex must be non-empty",
		"commands.version.export.shape must be a mapping",
		"commands.version.export.nil must be a mapping",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing issue %q in:\n%s", want, joined)
		}
	}
}
