package web

import (
	"net/http"
	"sync"
	"time"
)

// The event stream pushes a small "something changed" signal to connected
// dashboards over Server-Sent Events. Clients react by fetching through the
// normal JSON API, so the stream carries no payload contract of its own; the
// periodic dashboard poll stays as the reconciliation fallback.
const (
	streamContentType       = "text/event-stream"
	streamHeartbeatInterval = 25 * time.Second
	streamWriteTimeout      = 10 * time.Second
	// streamRetryHint asks EventSource to wait this many milliseconds before
	// reconnecting, keeping restart storms off a recovering daemon.
	streamRetryHint     = "retry: 5000\n\n"
	streamHeartbeat     = ": ping\n\n"
	streamChangeMessage = "event: change\ndata: 1\n\n"
)

// Broadcaster fans a data-changed signal out to every connected stream.
// Notify never blocks: each subscriber holds at most one pending signal, so
// bursts coalesce.
type Broadcaster struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

// NewBroadcaster returns an empty broadcaster.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: map[chan struct{}]struct{}{}}
}

// Notify signals every subscriber that dashboard-visible data changed.
func (b *Broadcaster) Notify() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- struct{}{}:
		default: // a signal is already pending; coalesce
		}
	}
}

func (b *Broadcaster) subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broadcaster) unsubscribe(ch chan struct{}) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

// handleStream serves the change-signal stream. Auth follows the normal read
// rules (the stream itself exposes no data beyond "something changed").
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if s.Changes == nil {
		writeError(w, http.StatusNotFound, "event stream disabled")
		return
	}
	w.Header().Set(headerContentType, streamContentType)
	w.Header().Set(headerCacheControl, headerValueNoStore)
	rc := http.NewResponseController(w)
	write := func(chunk string) bool {
		// The generic server write timeout would kill a long-lived stream;
		// extend it per write. Recorders without deadline support are fine.
		_ = rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
		if _, err := w.Write([]byte(chunk)); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write(streamRetryHint) {
		return
	}

	ch := s.Changes.subscribe()
	defer s.Changes.unsubscribe(ch)
	heartbeat := time.NewTicker(streamHeartbeatInterval)
	defer heartbeat.Stop()
	done := r.Context().Done()
	var shutdown <-chan struct{}
	if s.shutdown != nil {
		shutdown = s.shutdown.Done()
	}
	for {
		select {
		case <-done:
			return
		case <-shutdown:
			return
		case <-ch:
			if !write(streamChangeMessage) {
				return
			}
		case <-heartbeat.C:
			if !write(streamHeartbeat) {
				return
			}
		}
	}
}
