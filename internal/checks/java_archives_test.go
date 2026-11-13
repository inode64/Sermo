package checks

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sermo/internal/hostfs"
)

func writeTestJavaArchive(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for name, content := range entries {
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestJavaArchiveClosureAndCRC(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "main.jar")
	dep := filepath.Join(dir, "dep.jar")
	missing := filepath.Join(dir, "optional.jar")
	writeTestJavaArchive(t, root, map[string]string{javaManifestPath: "Manifest-Version: 1.0\r\nClass-Path: dep.jar optional.jar\r\n\r\n", "App.class": "bytecode"})
	writeTestJavaArchive(t, dep, map[string]string{javaManifestPath: "Class-Path: main.jar\n\n", "Dep.class": "dependency"})
	paths, err := inspectJavaArchives(t.Context(), []string{root}, hostfs.Open)
	want := []string{root, dep, missing}
	slices.Sort(want)
	if err != nil || !slices.Equal(paths, want) {
		t.Fatalf("%v, %v", paths, err)
	}
	if _, err := inspectJavaArchives(t.Context(), []string{missing}, hostfs.Open); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing root: %v", err)
	}
	data, err := os.ReadFile(dep)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("dependency"), []byte("corruption"), 1)
	if err := os.WriteFile(dep, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectJavaArchives(t.Context(), []string{root}, hostfs.Open); !errors.Is(err, errConfigSyntax) {
		t.Fatalf("CRC: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := inspectJavaArchives(ctx, []string{root}, hostfs.Open); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestJavaManifestAndReferences(t *testing.T) {
	largeSigned := "Manifest-Version: 1.0\nClass-Path: dep.jar\n\n" + strings.Repeat("Name: member.class\nSHA-256-Digest: abc\n\n", 2000)
	if refs, err := javaManifestClasspath([]byte(largeSigned[:javaManifestMaxBytes+1])); err != nil || !slices.Equal(refs, []string{"dep.jar"}) {
		t.Fatalf("large signed manifest: %v, %v", refs, err)
	}
	refs, err := javaManifestClasspath([]byte("Manifest-Version: 1.0\r\nClass-Path: first.jar se\r\n cond.jar\r\n\r\nName: ignored\r\nClass-Path: remote.jar\r\n"))
	if err != nil || !slices.Equal(refs, []string{"first.jar", "second.jar"}) {
		t.Fatalf("%v, %v", refs, err)
	}
	for _, data := range []string{" continuation", "Class-Path: one.jar\nclass-path: two.jar", "Class-Path", strings.Repeat("a", javaManifestMaxBytes+1), strings.Repeat("Header: value\r\n", javaManifestMaxBytes/8), string([]byte{0xff})} {
		if _, err := javaManifestClasspath([]byte(data)); !errors.Is(err, errConfigSyntax) {
			t.Errorf("manifest accepted: %v", err)
		}
	}
	for _, ref := range []string{"https://example.com/app.jar", "file://remote/app.jar", "file:/app.jar?x=1", "/app.jar#fragment", "classes/", "file:user@app.jar"} {
		if _, err := javaArchiveReference("/usr/share/app/main.jar", ref); err == nil {
			t.Errorf("accepted %q", ref)
		}
	}
	for ref, want := range map[string]string{"../lib/dep.jar": "/usr/share/lib/dep.jar", "file://localhost/usr/lib/dep.jar": "/usr/lib/dep.jar", "a%20b.jar": "/usr/share/app/a b.jar"} {
		if got, err := javaArchiveReference("/usr/share/app/main.jar", ref); err != nil || got != want {
			t.Errorf("%q: %q, %v", ref, got, err)
		}
	}
}

func TestJavaArchiveTrustAndLimits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.jar")
	writeTestJavaArchive(t, path, map[string]string{"App.class": "data"})
	if _, err := openTrustedJavaArchive(path); !errors.Is(err, errConfigSyntax) {
		t.Fatalf("writable /tmp ancestor accepted: %v", err)
	}
	if _, err := openTrustedJavaArchive(filepath.Join(dir, "missing.jar")); !errors.Is(err, errConfigSyntax) {
		t.Fatalf("missing dependency under unsafe ancestor: %v", err)
	}
	roots := make([]string, javaArchiveMaxCount+1)
	if _, err := inspectJavaArchives(t.Context(), roots, hostfs.Open); !errors.Is(err, errConfigSyntax) {
		t.Fatal(err)
	}
	var inflated uint64 = javaInflatedMaxBytes
	if _, err := inspectJavaArchive(t.Context(), path, hostfs.Open, &inflated); !errors.Is(err, errConfigSyntax) {
		t.Fatalf("expanded limit: %v", err)
	}
	writeFile(t, path, "not a zip")
	if _, err := inspectJavaArchives(t.Context(), []string{path}, hostfs.Open); !errors.Is(err, errConfigSyntax) {
		t.Fatalf("invalid archive: %v", err)
	}
}
