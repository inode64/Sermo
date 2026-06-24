package notify

import (
	"strings"
	"testing"
)

// parseTemplate requires at least one of subject/body; pin both halves of that
// AND so neither guard can be inverted without a test failing.
func TestParseTemplateRequiresSubjectOrBody(t *testing.T) {
	if _, err := parseTemplate("t", []byte("subject: \"\"\nbody: \"\"\n")); err == nil {
		t.Error("empty subject and body: want error")
	}
	if _, err := parseTemplate("t", []byte("other: value\n")); err == nil {
		t.Error("no subject/body keys: want error")
	}
	if _, err := parseTemplate("t", []byte("subject: Hello {{.name}}\n")); err != nil {
		t.Errorf("subject only: unexpected error %v", err)
	}
	if _, err := parseTemplate("t", []byte("body: a body line\n")); err != nil {
		t.Errorf("body only: unexpected error %v", err)
	}
}

// LoadTemplate fails closed when no template directory is configured.
func TestLoadTemplateRequiresDir(t *testing.T) {
	_, err := LoadTemplate("", "default-alert")
	if err == nil || !strings.Contains(err.Error(), "directory is required") {
		t.Errorf("empty dir: got %v, want a 'directory is required' error", err)
	}
}
