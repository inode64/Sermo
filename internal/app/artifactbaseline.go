package app

import (
	"maps"
	"sync"
)

// ArtifactBaseline holds the acknowledged fingerprint of each watched path
// behind a service's `changed:` conditions. The service's worker and its
// operation engine share one, and the engine also runs on another service's
// goroutine when that service cascades to it through also_apply, so every
// access is locked: an unsynchronized map would abort the whole daemon.
type ArtifactBaseline struct {
	mu           sync.Mutex
	fingerprints map[string]string
}

// NewArtifactBaseline returns an empty baseline.
func NewArtifactBaseline() *ArtifactBaseline {
	return &ArtifactBaseline{fingerprints: map[string]string{}}
}

// Changed reports whether path differs from its acknowledged fingerprint. The
// first observation adopts the current fingerprint (so a daemon start never
// triggers a restart); thereafter it is true until acknowledged. A nil
// baseline watches nothing.
func (b *ArtifactBaseline) Changed(path string, samples *ArtifactSamples) (bool, error) {
	return b.changedWithFingerprint(path, samples, fileFingerprint)
}

func (b *ArtifactBaseline) changedWithFingerprint(path string, samples *ArtifactSamples, directFingerprint func(string) string) (bool, error) {
	if b == nil {
		return false, nil
	}
	cur, observed := currentArtifactFingerprint(path, samples, directFingerprint)
	if !observed {
		return false, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	base, seen := b.fingerprints[path]
	if !seen {
		b.fingerprints[path] = cur
		return false, nil
	}
	return cur != base, nil
}

// Acknowledge refreshes every watched fingerprint and cache entry after a
// successful (re)launch. This one-off refresh keeps the acknowledged baseline
// aligned with the cache when an artifact changes during the operation,
// without adding filesystem work to normal service cycles.
func (b *ArtifactBaseline) Acknowledge(samples *ArtifactSamples) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for path := range b.fingerprints {
		if samples != nil {
			if _, tracked, _ := samples.FileFingerprint(path); tracked {
				samples.StoreFile(path)
			}
		}
		if fingerprint, observed := currentArtifactFingerprint(path, samples, fileFingerprint); observed {
			b.fingerprints[path] = fingerprint
		}
	}
}

// restore replaces the acknowledged fingerprints in place: the operation
// engine captured this baseline when it was built, so swapping in another
// one would leave the engine judging `changed:` against a baseline the worker
// no longer acknowledges.
func (b *ArtifactBaseline) restore(fingerprints map[string]string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	clear(b.fingerprints)
	maps.Copy(b.fingerprints, fingerprints)
}

// snapshot returns a copy of the acknowledged fingerprints.
func (b *ArtifactBaseline) snapshot() map[string]string {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return maps.Clone(b.fingerprints)
}
