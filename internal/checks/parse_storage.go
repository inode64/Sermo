package checks

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"time"

	"sermo/internal/cfgval"
)

// ValidateLogCheck validates log fields through the same pure parser used by
// construction. It does not read files or allocate the runtime tail state.
func ValidateLogCheck(entry map[string]any) error {
	_, err := parseLogFields(entry)
	return err
}

func parseLogFields(entry map[string]any) (logCheck, error) {
	var c logCheck
	var errs []error
	c.path = cfgval.String(entry[CheckKeyPath])
	switch {
	case c.path == "":
		errs = append(errs, errors.New("log check requires a path"))
	case !filepath.IsAbs(c.path):
		errs = append(errs, errors.New("log check path must be absolute"))
	default:
		c.path = filepath.Clean(c.path)
	}
	pattern := cfgval.String(entry[CheckKeyRegex])
	if pattern == "" {
		errs = append(errs, errors.New("log check requires a regex"))
	} else {
		var err error
		c.regex, err = regexp.Compile(pattern)
		if err != nil {
			errs = append(errs, fmt.Errorf("log check regex is invalid: %w", err))
		}
	}
	if _, ok := entry[CheckKeyCount].(map[string]any); !ok {
		errs = append(errs, errors.New("log check requires a count {op, value}"))
	} else {
		var err error
		c.op, c.value, err = ParsePredicate(CheckKeyCount, entry[CheckKeyCount])
		errs = append(errs, err)
	}
	var err error
	c.window, err = parseWithin(entry, "for a log check (e.g. 5m)")
	errs = append(errs, err)
	return c, errors.Join(errs...)
}

// ValidateCountCheck validates both count modes without touching the directory
// or creating a counter window.
func ValidateCountCheck(entry map[string]any) error {
	_, err := parseCountFields(entry)
	return err
}

func parseCountFields(entry map[string]any) (countCheck, error) {
	c := countCheck{path: cfgval.String(entry[CheckKeyPath]), kind: cfgval.String(entry[CheckKeyOf])}
	var errs []error
	if c.path == "" {
		errs = append(errs, errors.New("count check requires a path"))
	}
	if c.kind == "" {
		c.kind = CountKindAny
	} else if !validCountKind(c.kind) {
		errs = append(errs, fmt.Errorf("count `of` %q is not one of %s", c.kind, CountKindSummary))
	}
	var recursiveErr, hiddenErr error
	c.recursive, recursiveErr = parseStorageBool(entry, CheckKeyRecursive, CheckTypeCount)
	c.includeHidden, hiddenErr = parseStorageBool(entry, CheckKeyIncludeHidden, CheckTypeCount)
	errs = append(errs, recursiveErr, hiddenErr)
	if delta, present := entry[CheckKeyDelta]; present {
		errs = append(errs, parseCountDelta(&c, entry, delta))
	} else {
		errs = append(errs, parseCountThreshold(&c, entry))
	}
	return c, errors.Join(errs...)
}

func parseCountDelta(c *countCheck, entry map[string]any, delta any) error {
	var errs []error
	if _, hasCount := entry[CheckKeyCount]; hasCount {
		errs = append(errs, errors.New("count check must not mix a count threshold with delta"))
	}
	_, hasOp := entry[CheckKeyOp]
	_, hasValue := entry[CheckKeyValue]
	if hasOp || hasValue {
		errs = append(errs, errors.New("count check must not mix top-level op/value with delta"))
	}
	var predicateErr, windowErr error
	c.deltaOp, c.deltaValue, predicateErr = ParsePredicate(CheckKeyDelta, delta)
	c.window, windowErr = parseWithin(entry, "when count delta is set (e.g. 2m)")
	return errors.Join(append(errs, predicateErr, windowErr)...)
}

func parseCountThreshold(c *countCheck, entry map[string]any) error {
	var errs []error
	if cfgval.String(entry[CheckKeyWithin]) != "" {
		errs = append(errs, errors.New("within requires delta {op, value}"))
	}
	var threshold any = entry
	if raw, present := entry[CheckKeyCount]; present {
		_, hasOp := entry[CheckKeyOp]
		_, hasValue := entry[CheckKeyValue]
		if hasOp || hasValue {
			errs = append(errs, errors.New("count check must not mix a nested count {op, value} with top-level op/value"))
		}
		threshold = raw
	}
	var err error
	c.op, c.value, err = ParsePredicate("count check", threshold)
	return errors.Join(append(errs, err)...)
}

// ValidateSizeCheck validates path growth fields without sampling the path.
func ValidateSizeCheck(entry map[string]any) error {
	_, err := parseSizeFields(entry)
	return err
}

func parseSizeFields(entry map[string]any) (sizeCheck, error) {
	c := sizeCheck{path: cfgval.String(entry[CheckKeyPath])}
	var errs []error
	if c.path == "" {
		errs = append(errs, errors.New("path is required for a size check"))
	}
	var err error
	c.includeHidden, err = parseStorageBool(entry, CheckKeyIncludeHidden, CheckTypeSize)
	errs = append(errs, err)
	growBy := cfgval.String(entry[CheckKeyGrowBy])
	if growBy == "" {
		errs = append(errs, errors.New("grow_by is required for a size check (e.g. 1G)"))
	} else if c.growBy, err = parseSize(growBy); err != nil {
		errs = append(errs, fmt.Errorf("grow_by %q must be a positive size with a K/M/G/T suffix (e.g. 1G, 500M)", growBy))
	}
	c.window, err = parseWithin(entry, "for a size check (e.g. 1h)")
	return c, errors.Join(append(errs, err)...)
}

func parseStorageBool(entry map[string]any, key, checkType string) (bool, error) {
	raw, present := entry[key]
	if !present {
		return false, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("%s %s must be a boolean", checkType, key)
	}
	return value, nil
}

func parseWithin(entry map[string]any, requirement string) (time.Duration, error) {
	raw := cfgval.String(entry[CheckKeyWithin])
	if raw == "" {
		return 0, fmt.Errorf("within is required %s", requirement)
	}
	window := cfgval.Duration(entry[CheckKeyWithin])
	if window <= 0 {
		return 0, fmt.Errorf("within %q must be a valid positive duration", raw)
	}
	return window, nil
}
