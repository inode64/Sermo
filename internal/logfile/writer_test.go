package logfile

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenWriteAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "event.log")

	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Write(map[string]string{"kind": "action", "service": "web"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	w2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := w2.Write(map[string]string{"kind": "alert"}); err != nil {
		t.Fatalf("Write second: %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("Close second: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open read: %v", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	var lines []map[string]string
	for sc.Scan() {
		var row map[string]string
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatalf("json: %v", err)
		}
		lines = append(lines, row)
	}
	if len(lines) != 2 || lines[0]["service"] != "web" || lines[1]["kind"] != "alert" {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestOpenRejectsRelativePath(t *testing.T) {
	if _, err := Open("relative.log"); err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestWriteNilWriter(t *testing.T) {
	var w *Writer
	if err := w.Write(map[string]string{"x": "y"}); err != nil {
		t.Fatalf("nil Write: %v", err)
	}
}
