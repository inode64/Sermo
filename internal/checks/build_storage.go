package checks

// buildCountCheck builds a check on the number of entries under a path.
func buildCountCheck(b base, entry map[string]any) (Check, string) {
	c, err := parseCountFields(entry)
	if err != nil {
		return nil, err.Error()
	}
	c.base = b
	if c.deltaOp != "" {
		c.state = &counterWindow{}
	}
	return c, ""
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
	c, err := parseSizeFields(entry)
	if err != nil {
		return nil, err.Error()
	}
	c.base, c.sampler, c.state = b, deps.SizeSampler, &sizeState{}
	return &c, ""
}

// buildLogCheck adds runtime tail state to the validated log fields.
func buildLogCheck(b base, entry map[string]any) (Check, string) {
	c, err := parseLogFields(entry)
	if err != nil {
		return nil, err.Error()
	}
	c.base, c.state = b, newLogState()
	return c, ""
}
