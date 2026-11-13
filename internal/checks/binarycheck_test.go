package checks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBinaryResolvedPath(t *testing.T) {
	dir := t.TempDir()
	first, second, link := filepath.Join(dir, "first"), filepath.Join(dir, "second"), filepath.Join(dir, "selected")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("first", link); err != nil {
		t.Fatal(err)
	}
	c, warning := buildBinaryCheck(base{name: "selection"}, map[string]any{CheckKeyPath: link, CheckKeyResolvedPath: first})
	if warning != "" {
		t.Fatal(warning)
	}
	if result := c.Run(context.Background()); !result.OK {
		t.Fatal(result)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("second", link); err != nil {
		t.Fatal(err)
	}
	if result := c.Run(context.Background()); result.OK {
		t.Fatal("accepted replaced selection", result)
	}
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	if result := c.Run(context.Background()); result.OK {
		t.Fatal("accepted dangling selection", result)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("selected", link); err != nil {
		t.Fatal(err)
	}
	if result := c.Run(context.Background()); result.OK {
		t.Fatal("accepted link cycle", result)
	}
}
