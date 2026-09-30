package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/severity"
)

func TestSeverityLabel(t *testing.T) {
	tests := map[severity.Level]string{
		"":                cliTextFail,
		severity.Debug:    cliTextDebug,
		severity.Info:     cliTextInfo,
		severity.Warning:  cliTextWarn,
		severity.Error:    cliTextFail,
		severity.Critical: cliTextCrit,
	}
	for level, want := range tests {
		if got := severityLabel(level); got != want {
			t.Errorf("severityLabel(%q) = %q, want %q", level, got, want)
		}
	}
}

func TestEventsTableShowsSeverity(t *testing.T) {
	var stdout bytes.Buffer
	app := App{Stdout: &stdout}
	app.writeEventsTable([]event{
		{Time: "2026-09-30T10:00:00Z", Watch: "disk-root", Kind: "firing", Severity: "critical", Message: "used 99%"},
		{Time: "2026-09-30T09:00:00Z", Service: "web", Kind: "action", Action: "restart", Message: "restarted"},
	})
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "SEVERITY") {
		t.Fatalf("events table:\n%s", stdout.String())
	}
	if fields := strings.Fields(lines[1]); len(fields) < 4 || fields[3] != "critical" {
		t.Fatalf("graded row = %q, want critical in the SEVERITY column", lines[1])
	}
	if fields := strings.Fields(lines[2]); len(fields) < 4 || fields[3] != "-" {
		t.Fatalf("ungraded row = %q, want the - placeholder", lines[2])
	}
}

// A tier Sermo ignores is a warning: `config validate` prints it and still
// succeeds, because the configuration loads and runs.
func TestConfigValidatePrintsLevelWarnings(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "sermo.yml"), `
paths:
  services: [ `+root+`/services ]
defaults:
  policy:
    cooldown: 5m
watches:
  disk-root:
    severity: warning
    check:
      type: storage
      path: /
      used_pct: { op: ">=", value: "97%" }
      levels:
        error: { used_pct: { op: ">=", value: "95%" } }
    then:
      hook: { command: [/bin/true] }
`)
	global := filepath.Join(root, "sermo.yml")
	var stdout, stderr bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &stderr}
	if code := app.Run(context.Background(), []string{"--config", global, "config", "validate"}); code != exitSuccess {
		t.Fatalf("exit = %d, want success; stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "WARN global:") || !strings.Contains(stderr.String(), "levels.error.used_pct >= 95 is not stricter than >= 97; ignored") {
		t.Fatalf("stderr = %q, want the ignored tier", stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != cliTextOK {
		t.Fatalf("stdout = %q, want OK", stdout.String())
	}
	stdout.Reset()
	if code := app.Run(context.Background(), []string{"--config", global, "--json", "config", "validate"}); code != exitSuccess {
		t.Fatalf("json exit = %d", code)
	}
	if !strings.Contains(stdout.String(), `"warnings":[{"message":"watches.disk-root.check.levels.error`) || !strings.Contains(stdout.String(), `"valid":true`) {
		t.Fatalf("json = %s, want valid with warnings", stdout.String())
	}
}
