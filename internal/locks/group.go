package locks

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

const operationGroupsDirectory = "groups"

// ValidateOperationGroup applies the same filename boundary as operation locks.
// An empty group preserves independent per-service operations.
func ValidateOperationGroup(group string) error {
	if err := validateIdentifier("operation group", group, true); err != nil {
		return err
	}
	if group == "" {
		return nil
	}
	_, err := lockPath("", group+lockSuffix)
	return err
}

// AcquireWithGroup holds both the service's operation lock and an optional
// shared group lock. Contention fails fast, so unrelated services never wait
// behind a dependency's slow operation. Group locks cannot collide with service
// names or operator-owned named locks.
func (l OperationLocker) AcquireWithGroup(service, group string, ttl time.Duration) (func() error, error) {
	if err := ValidateOperationGroup(group); err != nil {
		return nil, err
	}
	var grouped *Handle
	if group != "" {
		locker := l
		locker.Dir = filepath.Join(l.Dir, operationGroupsDirectory)
		if l.OnReclaim != nil {
			locker.OnReclaim = func(_, reason string) { l.OnReclaim(service, "operation group "+group+": "+reason) }
		}
		var err error
		grouped, err = locker.Acquire(group, ttl)
		if err != nil {
			return nil, fmt.Errorf("operation group %s: %w", group, err)
		}
	}
	owned, err := l.Acquire(service, ttl)
	if err != nil {
		return nil, errors.Join(err, grouped.Release())
	}
	return func() error { return errors.Join(owned.Release(), grouped.Release()) }, nil
}
