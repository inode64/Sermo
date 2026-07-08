package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/goccy/go-yaml"
)

// Template applies a YAML notification template to a Message before delivery.
type Template struct {
	name    string
	subject *template.Template
	body    *template.Template
}

// TemplateField is one sorted structured field exposed to templates.
type TemplateField struct {
	Name  string
	Value string
}

type templateFile struct {
	Subject string `yaml:"subject"`
	Body    string `yaml:"body"`
}

type templateData struct {
	Subject string
	Body    string
	fields  map[string]string
}

const (
	templateFileSuffix        = ".yml"
	templateSubjectNameSuffix = ":subject"
	templateBodyNameSuffix    = ":body"
	templateOptionMissingKey  = "missingkey=zero"
)

// Field returns a structured field value by name, or an empty string when the
// notification did not provide that field.
func (d templateData) Field(name string) string {
	return d.fields[name]
}

// SortedFields returns structured fields in stable order.
func (d templateData) SortedFields() []TemplateField {
	names := slices.Sorted(maps.Keys(d.fields))
	out := make([]TemplateField, 0, len(names))
	for _, name := range names {
		out = append(out, TemplateField{Name: name, Value: d.fields[name]})
	}
	return out
}

// Name returns the configured template name.
func (t *Template) Name() string {
	if t == nil {
		return ""
	}
	return t.name
}

// ValidTemplateName reports whether name is safe to map to a file inside the
// configured template directory.
func ValidTemplateName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
}

// LoadTemplate loads a named template from dir. Template names are mapped to
// `<name>.yml` and cannot contain path separators.
func LoadTemplate(dir, name string) (*Template, error) {
	if dir == "" {
		return nil, errors.New("template directory is required")
	}
	if !ValidTemplateName(name) {
		return nil, fmt.Errorf("invalid template name %q", name)
	}
	path := filepath.Join(dir, name+templateFileSuffix)
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	tmpl, err := parseTemplate(name, data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return tmpl, nil
}

func parseTemplate(name string, data []byte) (*Template, error) {
	var raw templateFile
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse template YAML: %w", err)
	}
	if strings.TrimSpace(raw.Subject) == "" && strings.TrimSpace(raw.Body) == "" {
		return nil, errors.New("template must define subject or body")
	}
	t := &Template{name: name}
	if strings.TrimSpace(raw.Subject) != "" {
		parsed, err := template.New(name + templateSubjectNameSuffix).Option(templateOptionMissingKey).Parse(raw.Subject)
		if err != nil {
			return nil, fmt.Errorf("parse subject: %w", err)
		}
		t.subject = parsed
	}
	if strings.TrimSpace(raw.Body) != "" {
		parsed, err := template.New(name + templateBodyNameSuffix).Option(templateOptionMissingKey).Parse(raw.Body)
		if err != nil {
			return nil, fmt.Errorf("parse body: %w", err)
		}
		t.body = parsed
	}
	return t, nil
}

// Render applies the template. Missing subject or body fields keep the original
// message value, which lets a template override only the part it needs.
func (t *Template) Render(msg Message) (Message, error) {
	if t == nil {
		return msg, nil
	}
	data := templateData{Subject: msg.Subject, Body: msg.Body, fields: msg.Fields}
	out := msg
	if t.subject != nil {
		rendered, err := renderTemplate(t.subject, data)
		if err != nil {
			return Message{}, fmt.Errorf("render subject: %w", err)
		}
		out.Subject = rendered
	}
	if t.body != nil {
		rendered, err := renderTemplate(t.body, data)
		if err != nil {
			return Message{}, fmt.Errorf("render body: %w", err)
		}
		out.Body = rendered
	}
	return out, nil
}

func renderTemplate(t *template.Template, data templateData) (string, error) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

type templatedNotifier struct {
	inner    Notifier
	template *Template
}

// WithTemplate returns a notifier that renders tmpl before delegating delivery.
func WithTemplate(inner Notifier, tmpl *Template) Notifier {
	if tmpl == nil {
		return inner
	}
	return &templatedNotifier{inner: inner, template: tmpl}
}

func (n *templatedNotifier) Name() string { return n.inner.Name() }

func (n *templatedNotifier) Type() string { return n.inner.Type() }

func (n *templatedNotifier) Send(ctx context.Context, msg Message) error {
	rendered, err := n.template.Render(msg)
	if err != nil {
		return fmt.Errorf("render template %s: %w", n.template.Name(), err)
	}
	return n.inner.Send(ctx, rendered)
}
