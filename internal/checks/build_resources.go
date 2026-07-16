package checks

import "sermo/internal/cfgval"

func requireCheckPath(entry map[string]any, checkType string) (string, string) {
	path := cfgval.AsString(entry[CheckKeyPath])
	if path == "" {
		return "", checkType + " check requires a path"
	}
	return path, ""
}

func requireCheckPaths(entry map[string]any, checkType string) ([]string, string) {
	paths := cfgval.StringList(entry[CheckKeyPath])
	if len(paths) == 0 {
		return nil, checkType + " check requires a path"
	}
	return paths, ""
}

// buildFileExistsCheck builds a check that a path exists.
func buildFileExistsCheck(b base, entry map[string]any) (Check, string) {
	path, errs := requireCheckPath(entry, CheckTypeFileExists)
	if errs != "" {
		return nil, errs
	}
	return fileExistsCheck{base: b, path: path}, ""
}

// buildFileCheck builds a check that a path exists and is a regular file.
func buildFileCheck(b base, entry map[string]any) (Check, string) {
	path, errs := requireCheckPath(entry, CheckTypeFile)
	if errs != "" {
		return nil, errs
	}
	return fileCheck{base: b, path: path, nonEmpty: cfgval.Bool(entry[CheckKeyNonEmpty])}, ""
}

// buildLockfileCheck builds a check that one service-owned lockfile candidate
// exists and is a regular file.
func buildLockfileCheck(b base, entry map[string]any) (Check, string) {
	paths, errs := requireCheckPaths(entry, CheckTypeLockfile)
	if errs != "" {
		return nil, errs
	}
	return lockfileCheck{base: b, paths: paths}, ""
}

// buildPidfileCheck builds a check that a pidfile exists and references a running
// process. Gate it with `requires: [service]` so it only errors while the service
// is active.
func buildPidfileCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	paths, errs := requireCheckPaths(entry, CheckTypePidfile)
	if errs != "" {
		return nil, errs
	}
	return pidfileCheck{base: b, paths: paths, fallbackPIDs: deps.PidfileFallbackPIDs}, ""
}

// buildSocketCheck builds a check that one Unix socket candidate exists.
func buildSocketCheck(b base, entry map[string]any) (Check, string) {
	paths, errs := requireCheckPaths(entry, CheckTypeSocket)
	if errs != "" {
		return nil, errs
	}
	return socketCheck{base: b, paths: paths}, ""
}

// buildBinaryCheck builds a check on a binary's fingerprint.
func buildBinaryCheck(b base, entry map[string]any) (Check, string) {
	path, errs := requireCheckPath(entry, CheckTypeBinary)
	if errs != "" {
		return nil, errs
	}
	return binaryCheck{base: b, path: path}, ""
}

// buildLibrariesCheck builds a check on a binary's shared-library dependencies.
// Implemented natively with debug/elf (no ldd).
func buildLibrariesCheck(b base, entry map[string]any) (Check, string) {
	binary := cfgval.AsString(entry[CheckKeyBinary])
	if binary == "" {
		return nil, "libraries check requires a binary"
	}
	return librariesCheck{base: b, binary: binary}, ""
}
