package checks

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestXMLConfigValidation(t *testing.T) {
	for _, data := range []string{`<?xml version="1.0"?><Server><Service name="a&amp;b"/></Server>`, `<a xmlns:p="urn:test"><p:b p:x="1"/></a>`, `<a><!-- comment --><![CDATA[<text>]]></a>`} {
		if err := validateXML(t.Context(), []byte(data)); err != nil {
			t.Errorf("%s: %v", data, err)
		}
	}
	for _, data := range []string{"", `<a/><?xml version="1.0"?>`, `<?XML version="1.0"?><a/>`, `<a/><b/>`, `<a></b>`, `<a>`, `text<a/>`, `<a/>&amp;`, `<p:a/>`, `<a p:x="1"/>`, `<a x="1" x="2"/>`, `<a xmlns:p="urn:t" xmlns:q="urn:t" p:x="1" q:x="2"/>`, `<a xmlns:xml="wrong"/>`, `<a xmlns:p=""/>`, `<!DOCTYPE a SYSTEM "file:///etc/passwd"><a/>`, `<a>&unknown;</a>`, strings.Repeat("<a>", configXMLMaxDepth+1)} {
		if err := validateXML(t.Context(), []byte(data)); !errors.Is(err, errConfigSyntax) {
			t.Errorf("%s: got %v, want syntax error", data, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := validateXML(ctx, []byte(`<a/>`)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestNativeConfigResultAndChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.xml")
	c := configCheck{name: "xml", timeout: time.Second, format: ConfigFormatXML, paths: []string{path}, onChange: true, state: &cmdState{}}
	if got := c.Run(t.Context()); got.OK || !got.Unavailable {
		t.Fatalf("missing file: %+v", got)
	}
	writeFile(t, path, `<Server/>`)
	if got := c.Run(t.Context()); !got.OK {
		t.Fatal(got)
	}
	writeFile(t, path, `<Server><Service/></Server>`)
	if got := c.Run(t.Context()); got.OK || got.Unavailable {
		t.Fatalf("changed: %+v", got)
	}
	writeFile(t, path, `<Server>`)
	if got := c.Run(t.Context()); got.OK || got.Unavailable {
		t.Fatalf("invalid: %+v", got)
	}
}

func TestNativeConfigFields(t *testing.T) {
	for _, entry := range []map[string]any{
		{"format": "yaml", "path": "/etc/app.xml"}, {"format": "xml"}, {"format": "jar", "path": "relative.jar"},
		{"format": "xml", "path": "/etc/../app.xml"}, {"format": "xml", "path": "/etc/app.xml", "command": []string{"test"}},
	} {
		if err := ValidateConfigFormat(entry); err == nil {
			t.Errorf("accepted %v", entry)
		}
	}
	if err := ValidateConfigFormat(map[string]any{"format": "xml", "path": "/etc/app.xml"}); err != nil {
		t.Fatal(err)
	}
}
