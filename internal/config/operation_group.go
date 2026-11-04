package config

import (
	"fmt"

	"sermo/internal/locks"
)

// ServiceKeyOperationGroup serializes related services without coupling checks.
const ServiceKeyOperationGroup = "operation_group"

// ValidateOperationGroup is shared by validation and operation construction.
func ValidateOperationGroup(tree map[string]any) error {
	raw, present := tree[ServiceKeyOperationGroup]
	if !present {
		return nil
	}
	group, ok := raw.(string)
	if !ok {
		return fmt.Errorf("%s must be a string", ServiceKeyOperationGroup)
	}
	if err := locks.ValidateOperationGroup(group); err != nil {
		return fmt.Errorf("%s: %w", ServiceKeyOperationGroup, err)
	}
	return nil
}
