package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStreamDisabledWithoutBroadcaster(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testAPIPath(apiSegmentStream), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("stream without broadcaster = %d, want 404", rec.Code)
	}
}

func TestStreamPushesChangeSignal(t *testing.T) {
	changes := NewBroadcaster()
	srv := httptest.NewServer((&Server{Backend: &fakeBackend{}, Changes: changes}).Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+testAPIPath(apiSegmentStream), nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if ct := res.Header.Get(headerContentType); ct != streamContentType {
		t.Fatalf("content type = %q, want %q", ct, streamContentType)
	}

	reader := bufio.NewReader(res.Body)
	first, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read retry hint: %v", err)
	}
	if !strings.HasPrefix(streamRetryHint, first) {
		t.Fatalf("stream should open with the retry hint, got %q", first)
	}

	// The subscription is registered inside the handler; wait for it before
	// notifying so the signal cannot be lost to the startup race.
	deadline := time.Now().Add(2 * time.Second)
	for {
		changes.mu.Lock()
		subscribed := len(changes.subs) > 0
		changes.mu.Unlock()
		if subscribed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("handler never subscribed to the broadcaster")
		}
		time.Sleep(time.Millisecond)
	}
	changes.Notify()
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before the change signal: %v", err)
		}
		if strings.TrimSpace(line) == "event: change" {
			return
		}
	}
}

func TestBroadcasterCoalescesPendingSignals(t *testing.T) {
	changes := NewBroadcaster()
	ch := changes.subscribe()
	defer changes.unsubscribe(ch)
	changes.Notify()
	changes.Notify() // second signal coalesces into the pending one
	select {
	case <-ch:
	default:
		t.Fatal("subscriber should have a pending signal")
	}
	select {
	case <-ch:
		t.Fatal("coalesced signals should deliver exactly once")
	default:
	}
}
