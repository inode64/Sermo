package app

import "sync"

// registry stores immutable published values. Callers own snapshot construction.
// A nil registry is disabled; values must not be mutated after publication.
type registry[V any] struct {
	mu     sync.RWMutex
	values map[string]V
}

func newRegistry[V any]() *registry[V] {
	return &registry[V]{values: make(map[string]V)}
}

func (r *registry[V]) Publish(key string, value V) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.values[key] = value
	r.mu.Unlock()
}

// get copies the published value into dst. Concrete registries expose typed Get methods.
func (r *registry[V]) get(key string, dst *V) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.values[key]
	*dst = value
	return ok
}

func (r *registry[V]) Clear(key string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.values, key)
	r.mu.Unlock()
}
