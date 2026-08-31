package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFindUnusedProductionGlobals(t *testing.T) {
	root := writeModule(t, map[string]string{
		"p/p.go": `package p

const ExportedUnused = 1
const unexportedUnused = 2
var ExportedVariable = 3
var unexportedVariable = 4
const TestOnly = 5
const ExportedAcrossPackages = 6
var UsedVariable = ExportedAcrossPackages

func ReadUsedVariable() int { return UsedVariable }
func unusedFunction() {}
type record struct { Field int }
`,
		"p/p_test.go": `package p

import "testing"

func TestOnlyConsumer(t *testing.T) { _ = TestOnly }
`,
		"q/q.go": `package q

import "example.test/unused/p"

const UsedConstant = p.ExportedAcrossPackages
var UsedVariable = p.ReadUsedVariable()
func Values() (int, int) { return UsedConstant, UsedVariable }
`,
		"q/ignored.go": `//go:build unusedglobals_ignore

package q

const IgnoredByBuildSelection = 1
`,
	})

	findings, err := findUnused(context.Background(), root, []string{"./..."})
	if err != nil {
		t.Fatalf("findUnused: %v", err)
	}
	got := make([]string, 0, len(findings))
	for _, item := range findings {
		got = append(got, string(item.key.kind)+" "+item.key.name)
	}
	want := []string{
		"const ExportedUnused",
		"const TestOnly",
		"const unexportedUnused",
		"var ExportedVariable",
		"var unexportedVariable",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("findings = %v, want %v", got, want)
	}
}

func TestFindUnusedRejectsInvalidProductionPackage(t *testing.T) {
	root := writeModule(t, map[string]string{
		"broken/broken.go": "package broken\nvar Value = missing\n",
	})

	_, err := findUnused(context.Background(), root, []string{"./..."})
	if err == nil || !strings.Contains(err.Error(), "undefined: missing") {
		t.Fatalf("findUnused error = %v, want type-check failure", err)
	}
}

func TestFindWriteOnlyProductionFields(t *testing.T) {
	root := writeModule(t, map[string]string{
		"p/p.go": `package p

import (
	"io"
	"text/template"
)

type record struct {
	Read         int
	WriteOnly    int
	Compound     int
	Addressed    int
	Tagged       int ` + "`json:\"tagged\"`" + `
	NeverWritten int
}

var value = record{Read: 1, WriteOnly: 2, Compound: 3, Addressed: 4, Tagged: 5}

func readRecord() int {
	value.WriteOnly = 6
	value.Compound++
	readPointer(&value.Addressed)
	return value.Read
}

func readPointer(value *int) { _ = *value }

var positional = struct {
	Read      int
	WriteOnly int
}{1, 2}

func readPositional() int { return positional.Read }

type mapKey struct {
	PID        int
	StartTicks uint64
}

var keyed = map[mapKey]bool{{PID: 1, StartTicks: 2}: true}

func readMapKey() bool { return keyed[mapKey{PID: 1, StartTicks: 2}] }

type payload struct {
	Visible int
}

var payloads = map[string]any{"sample": []payload{{Visible: 1}}}

type templateData struct {
	Subject string
}

type templateField struct {
	Name  string
	Value string
}

func (templateData) Fields() []templateField {
	return []templateField{{Name: "state", Value: "ok"}}
}

func renderTemplate() error {
	tmpl := template.Must(template.New("subject").Parse("{{.Subject}} {{range .Fields}}{{.Name}}={{.Value}}{{end}}"))
	return tmpl.Execute(io.Discard, templateData{Subject: "hello"})
}
`,
		"p/p_test.go": `package p

import "testing"

func TestWriteOnlyFieldConsumer(t *testing.T) { _ = value.WriteOnly }
`,
	})

	findings, err := findUnused(context.Background(), root, []string{"./..."})
	if err != nil {
		t.Fatalf("findUnused: %v", err)
	}
	var got []string
	for _, item := range findings {
		message := item.message(root)
		if strings.Contains(message, "struct field") {
			got = append(got, message)
		}
	}
	if len(got) != 2 {
		t.Fatalf("write-only field findings = %v, want two WriteOnly fields", got)
	}
	for _, message := range got {
		if !strings.Contains(message, "struct field WriteOnly is written but never read in production") {
			t.Errorf("field finding = %q", message)
		}
	}
}

func TestFindWriteOnlyFieldAcrossPackages(t *testing.T) {
	root := writeModule(t, map[string]string{
		"p/p.go": `package p

type Payload struct {
	Read      int
	WriteOnly int
}

func New() Payload { return Payload{Read: 1, WriteOnly: 2} }
`,
		"q/q.go": `package q

import "example.test/unused/p"

func Read() int { return p.New().Read }
`,
	})

	findings, err := findUnused(context.Background(), root, []string{"./..."})
	if err != nil {
		t.Fatalf("findUnused: %v", err)
	}
	var fieldFindings []string
	for _, item := range findings {
		if message := item.message(root); strings.Contains(message, "struct field") {
			fieldFindings = append(fieldFindings, message)
		}
	}
	if len(fieldFindings) != 1 || !strings.Contains(fieldFindings[0], "struct field WriteOnly") {
		t.Fatalf("field findings = %v, want cross-package WriteOnly", fieldFindings)
	}
}

func TestRunRequiresPackagePattern(t *testing.T) {
	if got := run(nil, &strings.Builder{}, &strings.Builder{}); got != exitUsage {
		t.Fatalf("run exit = %d, want %d", got, exitUsage)
	}
}

func TestRunDiagnosticStatus(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		wantCode   int
		wantOutput string
	}{
		{
			name:       "unused constant fails",
			source:     "package sample\nconst Unused = 1\n",
			wantCode:   exitFindings,
			wantOutput: "package-level const Unused has no production references",
		},
		{
			name: "used globals pass",
			source: `package sample
const Used = 1
var Value = Used
func Read() int { return Value }
`,
			wantCode: exitOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeModule(t, map[string]string{"sample/sample.go": tt.source})
			var stdout, stderr strings.Builder
			got := run([]string{"-dir", root, "./..."}, &stdout, &stderr)
			if got != tt.wantCode {
				t.Fatalf("run exit = %d, want %d; stderr=%q", got, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.wantOutput) {
				t.Fatalf("stdout = %q, want substring %q", stdout.String(), tt.wantOutput)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files["go.mod"] = "module example.test/unused\n\ngo 1.26\n"
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create fixture directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
	return root
}
