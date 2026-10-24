package hostfs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReadPidfilePostgresFormat(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "postmaster.pid")
	if err := os.WriteFile(pidfile, []byte("4991\n/var/lib/postgresql/18/data\n1780913194\n5432\n/run/postgresql\n*\nready\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPidfile(pidfile)
	if err != nil {
		t.Fatalf("ReadPidfile() error = %v", err)
	}
	if got != 4991 {
		t.Fatalf("ReadPidfile() = %d, want 4991", got)
	}
}

// A pidfile path that turns out to be a FIFO must fail at once instead of
// blocking the discovery cycle until some writer opens it.
func TestReadPidfileRefusesFIFOWithoutBlocking(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "daemon.pid")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := ReadPidfile(fifo)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("ReadPidfile(fifo) error = %v, want a non-regular-file refusal", err)
		}
	case <-time.After(2 * time.Second):
		// Unblock the reader so the goroutine does not outlive the test.
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			_ = w.Close()
		}
		t.Fatal("ReadPidfile blocked on a FIFO")
	}
}

// Pidfiles are host files located by Sermo, so they go through hostfs: a relative
// path is refused rather than resolved against the daemon's working directory.
func TestReadPidfileRefusesRelativePath(t *testing.T) {
	if _, err := ReadPidfile("run/daemon.pid"); !errors.Is(err, ErrPath) {
		t.Fatalf("ReadPidfile(relative) error = %v, want ErrPath", err)
	}
}

// Only the first line names the PID; a huge file must not be read whole.
func TestReadPidfileReadsABoundedPrefix(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "big.pid")
	content := "4242\n" + strings.Repeat("x", 1<<20)
	if err := os.WriteFile(pidfile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPidfile(pidfile)
	if err != nil || got != 4242 {
		t.Fatalf("ReadPidfile(big) = %d, %v, want 4242", got, err)
	}
}

func TestReadPidfileProcessGroupForm(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name    string
		content string
		want    int
		wantErr bool
	}{
		{name: "negative is the group leader pid", content: "-3730\n", want: 3730},
		{name: "negative with trailing metadata", content: "-42\nsomething else\n", want: 42},
		{name: "minus one names every process", content: "-1\n", wantErr: true},
		{name: "minus zero is not a group", content: "-0\n", wantErr: true},
		{name: "zero stays invalid", content: "0\n", wantErr: true},
		{name: "non numeric stays invalid", content: "-\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pidfile := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_")+".pid")
			if err := os.WriteFile(pidfile, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := ReadPidfile(pidfile)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ReadPidfile(%q) = %d, want an error", tt.content, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadPidfile(%q) error = %v", tt.content, err)
			}
			if got != tt.want {
				t.Fatalf("ReadPidfile(%q) = %d, want %d", tt.content, got, tt.want)
			}
		})
	}
}
