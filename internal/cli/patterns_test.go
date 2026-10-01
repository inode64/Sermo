package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatternsListsSetsInUse(t *testing.T) {
	root, global := writeCatalogServiceConfig(t)
	patternsDir := filepath.Join(root, "catalog", "patterns")
	if err := os.MkdirAll(patternsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(patternsDir, "common.yml"), `
name: common
description: "Shared signals"
rules:
  - { id: failed, match: 'failed', severity: error }
  - { id: warn, match: 'warn', severity: warning }
`)
	mustWrite(t, filepath.Join(patternsDir, "unused.yml"), "name: unused\nrules:\n  - { id: x, match: 'x', severity: error }\n")
	check := "checks:\n  probe: { type: command, command: [/bin/true], analyze: { use: [common] } }\n"
	servicesDir := filepath.Join(root, "services")
	mustWrite(t, filepath.Join(servicesDir, "web.yml"), "name: web\nuses: nginx\n"+check)
	mustWrite(t, filepath.Join(servicesDir, "api.yml"), "name: api\nservice: api\n"+check)

	var stdout bytes.Buffer
	app := monitorTestApp(root, &stdout)
	if code := app.Run(context.Background(), []string{"--config", global, "patterns"}); code != exitSuccess {
		t.Fatalf("patterns exit = %d", code)
	}
	got := stdout.String()
	if !strings.Contains(got, "USED BY") || !strings.Contains(got, "api,web") {
		t.Errorf("patterns should list common with its users:\n%s", got)
	}
	if strings.Contains(got, "unused") {
		t.Errorf("patterns must hide sets no service uses:\n%s", got)
	}

	stdout.Reset()
	if code := app.Run(context.Background(), []string{"--config", global, "--json", "patterns", "catalog"}); code != exitSuccess {
		t.Fatalf("patterns catalog exit = %d", code)
	}
	var payload struct {
		Patterns []struct {
			Name   string   `json:"name"`
			Rules  int      `json:"rules"`
			UsedBy []string `json:"used_by"`
		} `json:"patterns"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	sets := map[string]int{}
	for i, p := range payload.Patterns {
		sets[p.Name] = i
	}
	common, okCommon := sets["common"]
	_, okUnused := sets["unused"]
	if !okCommon || !okUnused {
		t.Fatalf("patterns catalog = %+v, want every set", payload.Patterns)
	}
	if p := payload.Patterns[common]; p.Rules != 2 || strings.Join(p.UsedBy, ",") != "api,web" {
		t.Errorf("common = %+v", p)
	}
}

func TestPatternsNoneInUse(t *testing.T) {
	root, global := writeCatalogServiceConfig(t)
	var stdout bytes.Buffer
	app := monitorTestApp(root, &stdout)
	if code := app.Run(context.Background(), []string{"--config", global, "patterns"}); code != exitSuccess {
		t.Fatalf("patterns exit = %d", code)
	}
	if got := strings.TrimSpace(stdout.String()); got != "no pattern sets in use" {
		t.Fatalf("output = %q", got)
	}
}
