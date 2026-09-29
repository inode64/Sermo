package cli

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

// Ctrl-C at the hidden prompt must return (so the deferred echo restore runs)
// instead of killing sermoctl with the terminal still silent.
func TestReadUntilSignalReturnsOnInterrupt(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	sigs := make(chan os.Signal, 1)
	sigs <- syscall.SIGINT
	done := make(chan error, 1)
	go func() {
		_, err := readUntilSignal(func() (string, error) { <-block; return "", nil }, sigs)
		done <- err
	}()
	select {
	case err := <-done:
		interrupted, ok := errors.AsType[interruptedError](err)
		if !ok || interrupted.exitCode() != 130 {
			t.Fatalf("err = %v, want an interrupt mapped to exit 130", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prompt kept blocking after SIGINT")
	}
}

func TestReadUntilSignalReturnsLine(t *testing.T) {
	line, err := readUntilSignal(func() (string, error) { return "secret", nil }, make(chan os.Signal))
	if err != nil || line != "secret" {
		t.Fatalf("readUntilSignal() = %q, %v", line, err)
	}
}
