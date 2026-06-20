package notify

import (
	"context"
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
