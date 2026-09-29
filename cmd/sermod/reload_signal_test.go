package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// A SIGHUP delivered while sermod is still starting must be captured, not
// take Go's default action (terminate), which systemd would not restart.
func TestNotifyReloadClaimsSIGHUP(t *testing.T) {
	hup := notifyReload()
	defer signal.Stop(hup)
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case sig := <-hup:
		if sig != syscall.SIGHUP {
			t.Fatalf("received %v, want SIGHUP", sig)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SIGHUP was not delivered to the reload channel")
	}
}

// A SIGHUP buffered during startup is applied once the reload loop starts, and
// the loop stops with the daemon context.
func TestServeReloadsAppliesPendingStartupSIGHUP(t *testing.T) {
	hup := make(chan os.Signal, 1)
	hup <- syscall.SIGHUP // arrived before the monitor existed
	reloaded := make(chan struct{}, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveReloads(ctx, hup, func(context.Context) { reloaded <- struct{}{} })
	}()
	select {
	case <-reloaded:
	case <-time.After(5 * time.Second):
		t.Fatal("pending startup SIGHUP was not applied")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reload loop did not stop with the daemon context")
	}
	if len(reloaded) != 0 {
		t.Fatalf("extra reloads: %d", len(reloaded))
	}
}

// A SIGHUP racing shutdown must not start a new generation.
func TestServeReloadsIgnoresSIGHUPAfterShutdown(t *testing.T) {
	hup := make(chan os.Signal, 1)
	hup <- syscall.SIGHUP
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	serveReloads(ctx, hup, func(context.Context) { t.Fatal("reload ran after shutdown") })
}
