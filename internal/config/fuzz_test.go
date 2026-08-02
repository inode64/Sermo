package config

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzLoadGlobal ensures that untrusted global YAML can only yield a parsed
// configuration or a regular error, never a panic.
func FuzzLoadGlobal(f *testing.F) {
	f.Add([]byte("engine:\n  backend: auto\n"))
	f.Add([]byte("paths: [\n"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, source []byte) {
		path := filepath.Join(t.TempDir(), "sermo.yml")
		if err := os.WriteFile(path, source, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = loadGlobal(path)
	})
}

// FuzzLoadDocument ensures per-target YAML documents (services, watches, …)
// only yield a Document or a regular error — never a panic — for untrusted bytes.
func FuzzLoadDocument(f *testing.F) {
	f.Add([]byte("name: web\ncheck:\n  type: tcp\n  host: 127.0.0.1\n  port: 80\n"))
	f.Add([]byte("name: [\n"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, source []byte) {
		path := filepath.Join(t.TempDir(), "doc.yml")
		if err := os.WriteFile(path, source, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = loadDocument(path)
	})
}
