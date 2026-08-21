package notify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingNotifier struct {
	msg Message
}

func (n *recordingNotifier) Name() string { return "record" }

func (n *recordingNotifier) Type() string { return "record" }

func (n *recordingNotifier) Send(_ context.Context, msg Message) error {
	n.msg = msg
	return nil
}

func TestTemplateRendersMessageAndFields(t *testing.T) {
	tmpl, err := parseTemplate("custom", []byte(`
subject: '{{ .Subject }} / {{ .Field "SERMO_SERVICE" }}'
body: |
  {{ .Body }}
  {{ range .SortedFields }}{{ .Name }}={{ .Value }}
  {{ end }}
`))
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := tmpl.Render(Message{
		Subject: "alert",
		Body:    "body",
		Fields:  map[string]string{"SERMO_RULE": "high-cpu", "SERMO_SERVICE": "nginx"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rendered.Subject != "alert / nginx" {
		t.Fatalf("subject = %q", rendered.Subject)
	}
	if !strings.Contains(rendered.Body, "SERMO_RULE=high-cpu") || !strings.Contains(rendered.Body, "SERMO_SERVICE=nginx") {
		t.Fatalf("body = %q", rendered.Body)
	}
	if strings.Index(rendered.Body, "SERMO_RULE") > strings.Index(rendered.Body, "SERMO_SERVICE") {
		t.Fatalf("fields are not sorted: %q", rendered.Body)
	}
}

func TestTemplatedNotifierRendersBeforeSend(t *testing.T) {
	tmpl, err := parseTemplate("custom", []byte(`
subject: "{{ .Subject }} / rendered"
body: '{{ .Body }} / {{ .Field "SERMO_WATCH" }}'
`))
	if err != nil {
		t.Fatal(err)
	}
	inner := &recordingNotifier{}
	notifier := WithTemplate(inner, tmpl)
	if err := notifier.Send(context.Background(), Message{
		Subject: "watch",
		Body:    "payload",
		Fields:  map[string]string{"SERMO_WATCH": "storage-root"},
	}); err != nil {
		t.Fatal(err)
	}
	if inner.msg.Subject != "watch / rendered" || inner.msg.Body != "payload / storage-root" {
		t.Fatalf("message = %+v", inner.msg)
	}
}

func TestValidTemplateNameRejectsPathTraversal(t *testing.T) {
	for _, name := range []string{"../secret", "a/b", "bad name", ".."} {
		if ValidTemplateName(name) {
			t.Fatalf("name %q should be invalid", name)
		}
	}
	for _, name := range []string{"default-alert", "tenant.ops", "email_1"} {
		if !ValidTemplateName(name) {
			t.Fatalf("name %q should be valid", name)
		}
	}
}

// TestLoadTemplateLocalOverrideShadowsPackaged covers the per-host template
// layer: unlike the document directories, whose overrides merge field by field,
// a template is replaced whole because it has no named entries to merge.
func TestLoadTemplateLocalOverrideShadowsPackaged(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "templates")
	local := base + LocalDirSuffix
	for _, dir := range []string{base, local} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(dir, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "alert.yml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	renderedSubject := func() string {
		t.Helper()
		tmpl, err := LoadTemplate(base, "alert")
		if err != nil {
			t.Fatalf("LoadTemplate() error = %v", err)
		}
		rendered, err := tmpl.Render(Message{Subject: "ignored", Body: "ignored"})
		if err != nil {
			t.Fatalf("Render() error = %v", err)
		}
		return rendered.Subject
	}

	write(base, "subject: packaged\nbody: packaged body\n")
	if got := renderedSubject(); got != "packaged" {
		t.Fatalf("subject = %q, want the packaged template before any override", got)
	}

	write(local, "subject: host\nbody: host body\n")
	if got := renderedSubject(); got != "host" {
		t.Fatalf("subject = %q, want the .local override to shadow the packaged template", got)
	}

	// A directory of that name must not shadow a real template.
	if err := os.Remove(filepath.Join(local, "alert.yml")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(local, "alert.yml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := renderedSubject(); got != "packaged" {
		t.Fatalf("subject = %q, want a non-regular override entry to be ignored", got)
	}
}
