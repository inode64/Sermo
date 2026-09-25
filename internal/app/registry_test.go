package app

import (
	"sync"
	"testing"
)

func TestRegistryDisabled(t *testing.T) {
	var r *registry[int]
	r.Publish("service", 1)
	r.Clear("service")
	var value int
	if ok := r.get("service", &value); ok || value != 0 {
		t.Fatalf("disabled registry returned %d, %v", value, ok)
	}
}

func TestRegistryConcurrentAccess(t *testing.T) {
	r := newRegistry[int]()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for n := range 100 {
				r.Publish("service", n)
				r.get("service", new(int))
				r.Clear("service")
			}
		})
	}
	wg.Wait()
	r.Publish("service", 42)
	var value int
	if ok := r.get("service", &value); !ok || value != 42 {
		t.Fatalf("published value: %d, %v", value, ok)
	}
	r.Clear("service")
	if ok := r.get("service", &value); ok {
		t.Fatal("cleared value remains")
	}
}
