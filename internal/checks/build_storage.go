package checks

import (
	"time"

	"sermo/internal/cfgval"
)

// buildCountCheck builds a check on the number of entries under a path.
func buildCountCheck(b base, entry map[string]any) (Check, string) {
	path, errs := requireCheckPath(entry, CheckTypeCount)
	if errs != "" {
		return nil, errs
	}
	kind := cfgval.AsString(entry[CheckKeyOf])
	if kind == "" {
		kind = CountKindAny
	}
	if !validCountKind(kind) {
		return nil, "count check `of` must be " + CountKindSummary
	}
	if _, hasDelta := entry[CheckKeyDelta]; hasDelta {
		if _, hasCount := entry[CheckKeyCount]; hasCount {
			return nil, "count check must not mix a count threshold with delta"
		}
		_, hasOp := entry[CheckKeyOp]
		_, hasValue := entry[CheckKeyValue]
		if hasOp || hasValue {
			return nil, "count check must not mix top-level op/value with delta"
		}
		op, val, errs := parseDeltaThreshold(entry[CheckKeyDelta], "count check")
		if errs != "" {
			return nil, errs
		}
		window := cfgval.Duration(entry[CheckKeyWithin])
		if window <= 0 {
			return nil, "count check delta requires a positive within (e.g. 2m)"
		}
		return countCheck{
			base:          b,
			path:          path,
			kind:          kind,
			recursive:     cfgval.Bool(entry[CheckKeyRecursive]),
			includeHidden: cfgval.Bool(entry[CheckKeyIncludeHidden]),
			deltaOp:       op,
			deltaValue:    val,
			window:        window,
			clock:         time.Now,
			state:         &counterWindow{},
		}, ""
	}
	if cfgval.String(entry[CheckKeyWithin]) != "" {
		return nil, "count check within requires delta {op, value}"
	}
	// The threshold may sit at the top level (op/value) or be nested under
	// `count: {op, value}` like every other named predicate.
	threshold := entry
	if m, ok := entry[CheckKeyCount].(map[string]any); ok {
		threshold = m
	}
	op := cfgval.AsString(threshold[CheckKeyOp])
	if !cfgval.IsCompareOp(op) {
		return nil, "count check requires a valid op (>=, >, <=, <, ==, !=)"
	}
	val, err := parseFiniteThreshold(threshold[CheckKeyValue])
	if err != nil {
		return nil, "count check value " + err.Error()
	}
	return countCheck{base: b, path: path, kind: kind, recursive: cfgval.Bool(entry[CheckKeyRecursive]), includeHidden: cfgval.Bool(entry[CheckKeyIncludeHidden]), op: op, value: val}, ""
}

// buildStorageCheck builds a storage space/inode and/or mount check.
func buildStorageCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	path, errs := requireCheckPath(entry, CheckTypeStorage)
	if errs != "" {
		return nil, errs
	}
	preds, err := parseLevelPreds(entry, StoragePredFields)
	if err != nil {
		return nil, "storage check: " + err.Error()
	}
	mount := parseMountCond(entry)
	if len(preds) == 0 && !mount.active {
		return nil, "storage check requires a space/inode predicate (used_pct/free_pct/used_bytes/free_bytes/inodes_*) and/or a mount condition (mounted)"
	}
	return storageCheck{base: b, path: path, preds: preds, usage: deps.StorageUsage, mount: mount, mountSampler: deps.MountSampler}, ""
}

// buildSizeCheck builds a path-growth check over a time window.
func buildSizeCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	path, errs := requireCheckPath(entry, CheckTypeSize)
	if errs != "" {
		return nil, errs
	}
	// parseSize already rejects zero, negative and unitless values, so a nil
	// error guarantees growBy > 0 — no redundant positivity guard needed.
	growBy, err := parseSize(cfgval.String(entry[CheckKeyGrowBy]))
	if err != nil {
		return nil, "size check requires a positive grow_by with a K/M/G/T suffix (e.g. 1G)"
	}
	window := cfgval.Duration(entry[CheckKeyWithin])
	if window <= 0 {
		return nil, "size check requires a positive within (e.g. 1h)"
	}
	return &sizeCheck{base: b, path: path, growBy: growBy, window: window, includeHidden: cfgval.Bool(entry[CheckKeyIncludeHidden]), sampler: deps.SizeSampler, clock: time.Now, state: &sizeState{}}, ""
}
