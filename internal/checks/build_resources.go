package checks

import "sermo/internal/cfgval"

// buildFileExistsCheck builds a check that a path exists.
func buildFileExistsCheck(b base, entry map[string]any) (Check, string) {
	path := cfgval.AsString(entry[CheckKeyPath])
	if path == "" {
		return nil, "file_exists check requires a path"
	}
	return fileExistsCheck{base: b, path: path}, ""
}

// buildFileCheck builds a check that a path exists and is a regular file.
func buildFileCheck(b base, entry map[string]any) (Check, string) {
	path := cfgval.AsString(entry[CheckKeyPath])
	if path == "" {
		return nil, "file check requires a path"
	}
	return fileCheck{base: b, path: path, nonEmpty: cfgval.Bool(entry[CheckKeyNonEmpty])}, ""
}

// buildLockfileCheck builds a check that one service-owned lockfile candidate
// exists and is a regular file.
func buildLockfileCheck(b base, entry map[string]any) (Check, string) {
	paths := cfgval.StringList(entry[CheckKeyPath])
	if len(paths) == 0 {
		return nil, "lockfile check requires a path"
	}
	return lockfileCheck{base: b, paths: paths}, ""
}

// buildPidfileCheck builds a check that a pidfile exists and references a running
// process. Gate it with `requires: [service]` so it only errors while the service
// is active.
func buildPidfileCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	paths := cfgval.StringList(entry[CheckKeyPath])
	if len(paths) == 0 {
		return nil, "pidfile check requires a path"
	}
	return pidfileCheck{base: b, paths: paths, fallbackPIDs: deps.PidfileFallbackPIDs}, ""
}

// buildSocketCheck builds a check that one Unix socket candidate exists.
func buildSocketCheck(b base, entry map[string]any) (Check, string) {
	paths := cfgval.StringList(entry[CheckKeyPath])
	if len(paths) == 0 {
		return nil, "socket check requires a path"
	}
	return socketCheck{base: b, paths: paths}, ""
}

// buildBinaryCheck builds a check on a binary's fingerprint.
func buildBinaryCheck(b base, entry map[string]any) (Check, string) {
	path := cfgval.AsString(entry[CheckKeyPath])
	if path == "" {
		return nil, "binary check requires a path"
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
