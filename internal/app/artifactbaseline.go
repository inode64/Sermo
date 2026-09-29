package app

import (
	"maps"
	"sync"
)

// ArtifactBaseline holds the acknowledged state behind a service's `changed:`
// conditions: the fingerprint of each watched path and the version of each
// watched app. The service's worker and its operation engine share one, and
// the engine also runs on another service's goroutine when that service
// cascades to it through also_apply, so every access is locked: an
// unsynchronized map would abort the whole daemon.
type ArtifactBaseline struct {
	mu           sync.Mutex
	fingerprints map[string]string
	// versions holds the acknowledged version-short of each watched app+level
	// (key "app:level"), the version analogue of fingerprints. versionsLast
	// holds the most recently sampled one, so Acknowledge can adopt the
	// post-restart version without re-running the version command.
	versions     map[string]string
	versionsLast map[string]string
}

// artifactBaselineState is a copy of an ArtifactBaseline carried across a
// config reload.
type artifactBaselineState struct {
	fingerprints map[string]string
	versions     map[string]string
	versionsLast map[string]string
}

// NewArtifactBaseline returns an empty baseline.
func NewArtifactBaseline() *ArtifactBaseline {
	return &ArtifactBaseline{
		fingerprints: map[string]string{},
		versions:     map[string]string{},
		versionsLast: map[string]string{},
	}
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

// versionChanged records version as the latest sample for key ("app:level")
// and reports whether it differs from the acknowledged version. The first
// observation adopts it, like Changed.
func (b *ArtifactBaseline) versionChanged(key, version string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.versionsLast[key] = version
	base, seen := b.versions[key]
	if !seen {
		b.versions[key] = version
		return false
	}
	return version != base
}

// versionChange returns the acknowledged and the latest sampled version for
// key ("app:level").
func (b *ArtifactBaseline) versionChange(key string) (acknowledged, latest string) {
	if b == nil {
		return "", ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.versions[key], b.versionsLast[key]
}

// Acknowledge refreshes every watched fingerprint and cache entry after a
// successful (re)launch. This one-off refresh keeps the acknowledged baseline
// aligned with the cache when an artifact changes during the operation,
// without adding filesystem work to normal service cycles. It also adopts the
// app versions sampled during rule evaluation: after a successful restart the
// service runs the upgraded app, so the last-seen version is the one to
// acknowledge, without re-running the version command.
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
	maps.Copy(b.versions, b.versionsLast)
}

// restore replaces the acknowledged state in place: the operation engine
// captured this baseline when it was built, so swapping in another one would
// leave the engine judging `changed:` against a baseline the worker no longer
// acknowledges.
func (b *ArtifactBaseline) restore(state artifactBaselineState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fingerprints = cloneOrEmpty(state.fingerprints)
	b.versions = cloneOrEmpty(state.versions)
	b.versionsLast = cloneOrEmpty(state.versionsLast)
}

// snapshot returns a copy of the acknowledged state, or nil when nothing has
// been observed yet.
func (b *ArtifactBaseline) snapshot() *artifactBaselineState {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.fingerprints) == 0 && len(b.versions) == 0 && len(b.versionsLast) == 0 {
		return nil
	}
	return &artifactBaselineState{
		fingerprints: maps.Clone(b.fingerprints),
		versions:     maps.Clone(b.versions),
		versionsLast: maps.Clone(b.versionsLast),
	}
}

func cloneOrEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return maps.Clone(m)
}
