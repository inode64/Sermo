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
