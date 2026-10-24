package app

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"sermo/internal/state"
)

// BenchmarkWebBackendWatches measures one /api/watches read on a host with 60
// storage watches whose monitor state is persisted in the real state store, as
// every dashboard poll does per viewer.
func BenchmarkWebBackendWatches(b *testing.B) {
	const count = 60
	store, err := state.OpenContextWith(context.Background(), filepath.Join(b.TempDir(), state.Filename), state.Options{})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	backend := &WebBackend{watches: map[string]*webWatch{}, store: store}
	for i := range count {
		name := fmt.Sprintf("storage-%02d", i)
		backend.watchOrder = append(backend.watchOrder, name)
		backend.watches[name] = &webWatch{name: name, checkType: "storage"}
		if err := store.SetActive(WatchMonitorKey(name), i%2 == 0, "web"); err != nil {
			b.Fatal(err)
		}
	}
	for b.Loop() {
		if got := backend.Watches(context.Background()); len(got) != count {
			b.Fatalf("got %d watches, want %d", len(got), count)
		}
	}
}
