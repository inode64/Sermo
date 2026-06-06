package locks

import (
	"os"
	"path/filepath"
	"time"
)

// PauseStore records which services have monitoring paused (operator ran
// `unmonitor`) as marker files under <runtime>/paused. A paused service's daemon
// worker skips its cycle until `monitor` clears the marker. State persists across
// daemon restarts because it lives on disk, alongside locks under paths.runtime.
type PauseStore struct{ dir string }

// NewPauseStore returns a store rooted at dir (typically <runtime>/paused).
func NewPauseStore(dir string) PauseStore { return PauseStore{dir: dir} }

func (p PauseStore) path(service string) string {
	return filepath.Join(p.dir, service+".paused")
}

// Pause marks a service paused (idempotent) and returns the marker path. The file
// records when it was paused, for inspection.
func (p PauseStore) Pause(service string) (string, error) {
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return "", err
	}
	path := p.path(service)
	content := []byte(time.Now().UTC().Format(time.RFC3339) + "\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Resume clears a service's pause, reporting whether it had been paused.
func (p PauseStore) Resume(service string) (bool, error) {
	err := os.Remove(p.path(service))
	switch {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, err
	}
}

// Paused reports whether monitoring is currently paused for the service.
func (p PauseStore) Paused(service string) bool {
	_, err := os.Stat(p.path(service))
	return err == nil
}
