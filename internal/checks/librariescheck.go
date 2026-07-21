package checks

import (
	"context"
	"debug/elf"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sermo/internal/execx"
)

const (
	ldSoConfDir       = "/etc/ld.so.conf.d"
	ldSoConfFile      = "/etc/ld.so.conf"
	ldSoConfSuffix    = ".conf"
	ldSoIncludePrefix = "include "
	ldLibraryPathEnv  = "LD_LIBRARY_PATH"
	ldPathSeparator   = ":"
	ldCommentHash     = "#"
	ldCommentSemi     = ";"
	elfOriginToken    = "$ORIGIN"
	elfOriginBraced   = "${ORIGIN}"
	libDirAArch64     = "/lib/aarch64-linux-gnu"
	libDirARMHF       = "/lib/arm-linux-gnueabihf"
	libDirI386        = "/lib/i386-linux-gnu"
	libDirRoot        = "/lib"
	libDirRoot64      = "/lib64"
	libDirUsr         = "/usr/lib"
	libDirUsr64       = "/usr/lib64"
	libDirUsrAArch64  = "/usr/lib/aarch64-linux-gnu"
	libDirUsrARMHF    = "/usr/lib/arm-linux-gnueabihf"
	libDirUsrI386     = "/usr/lib/i386-linux-gnu"
	libDirUsrX8664    = "/usr/lib/x86_64-linux-gnu"
	libDirX8664       = "/lib/x86_64-linux-gnu"
)

// librariesCheck verifies that all DT_NEEDED shared libraries for a binary
// can be resolved using the dynamic linker's search rules (rpath/runpath,
// system library directories and /etc/ld.so.conf*). Implemented with debug/elf
// only (no external ldd), per the native-Go policy.
type librariesCheck struct {
	base
	binary string
}

func (c librariesCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	ef, err := elf.Open(c.binary)
	if err != nil {
		return c.result(false, c.binary+": "+err.Error(), start)
	}
	defer ef.Close()

	needed, err := ef.DynString(elf.DT_NEEDED)
	if err != nil || len(needed) == 0 {
		return c.result(true, c.binary+": static binary, no shared libraries", start)
	}

	dirs := collectLibrarySearchDirs(c.binary, ef)

	// LD_LIBRARY_PATH takes precedence (as the real dynamic linker does).
	// We prepend it so it is searched first.
	if lp := os.Getenv(ldLibraryPathEnv); lp != "" {
		for p := range strings.SplitSeq(lp, ldPathSeparator) {
			if p != "" {
				dirs = append([]string{expandOrigin(p, c.binary)}, dirs...)
			}
		}
		dirs = dedupPreserveOrder(dirs)
	}

	missing := resolveNeeded(ctx, needed, dirs, make(map[string]bool))
	if err := ctx.Err(); err != nil {
		return c.result(false, c.binary+": "+execx.ContextFailure(err, c.timeout), start)
	}
	if len(missing) > 0 {
		return c.result(false, c.binary+": missing shared libraries", start)
	}
	return c.result(true, c.binary+": all shared libraries resolve", start)
}

// resolveNeeded recursively resolves DT_NEEDED entries (including transitive
// dependencies of the resolved libraries). It returns the list of sonames
// that could not be located.
func resolveNeeded(ctx context.Context, needed, dirs []string, seen map[string]bool) []string {
	var missing []string
	for _, soname := range needed {
		if err := ctx.Err(); err != nil {
			return missing
		}
		if seen[soname] {
			continue
		}
		seen[soname] = true

		path := findLibrary(soname, dirs)
		if path == "" {
			missing = append(missing, soname)
			continue
		}

		// Open the resolved library to collect its own DT_NEEDED (transitive).
		ef, err := elf.Open(path)
		if err != nil {
			missing = append(missing, soname+" (open failed)")
			continue
		}
		subNeeded, _ := ef.DynString(elf.DT_NEEDED)
		ef.Close()

		if len(subNeeded) > 0 {
			subMissing := resolveNeeded(ctx, subNeeded, dirs, seen)
			missing = append(missing, subMissing...)
		}
	}
	return missing
}

// collectLibrarySearchDirs builds the library search path list for the given
// binary, honouring its DT_RUNPATH / DT_RPATH (with $ORIGIN expansion),
// its directory, common multi-arch paths, and a best-effort parse of
// /etc/ld.so.conf (and .d fragments).
func collectLibrarySearchDirs(binary string, ef *elf.File) []string {
	var dirs []string

	// Prefer RUNPATH, fall back to RPATH (older binaries).
	if rps, _ := ef.DynString(elf.DT_RUNPATH); len(rps) > 0 && rps[0] != "" {
		for p := range strings.SplitSeq(rps[0], ldPathSeparator) {
			if p != "" {
				dirs = append(dirs, expandOrigin(p, binary))
			}
		}
	} else if rps, _ := ef.DynString(elf.DT_RPATH); len(rps) > 0 && rps[0] != "" {
		for p := range strings.SplitSeq(rps[0], ldPathSeparator) {
			if p != "" {
				dirs = append(dirs, expandOrigin(p, binary))
			}
		}
	}

	// Directory of the binary itself (some apps ship private libs next to exe).
	if d := filepath.Dir(binary); d != "" && d != "." {
		dirs = append(dirs, d)
	}

	// Common system locations (covers most distros and multi-arch setups).
	dirs = append(dirs,
		libDirRoot, libDirUsr,
		libDirRoot64, libDirUsr64,
		libDirX8664, libDirUsrX8664,
		libDirAArch64, libDirUsrAArch64,
		libDirI386, libDirUsrI386,
		libDirARMHF, libDirUsrARMHF,
	)

	// Best-effort augmentation from ld.so.conf and fragments.
	if more := parseLdSoConf(ldSoConfFile); len(more) > 0 {
		dirs = append(dirs, more...)
	}
	// Common drop-in directory even if main conf doesn't include it.
	if entries, _ := os.ReadDir(ldSoConfDir); len(entries) > 0 {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ldSoConfSuffix) {
				if more := parseLdSoConf(filepath.Join(ldSoConfDir, e.Name())); len(more) > 0 {
					dirs = append(dirs, more...)
				}
			}
		}
	}

	return dedupPreserveOrder(dirs)
}

func expandOrigin(p, binary string) string {
	dir := filepath.Dir(binary)
	p = strings.ReplaceAll(p, elfOriginToken, dir)
	p = strings.ReplaceAll(p, elfOriginBraced, dir)
	return filepath.Clean(p)
}

func findLibrary(soname string, dirs []string) string {
	if filepath.IsAbs(soname) {
		if _, err := os.Stat(soname); err == nil {
			return soname
		}
		return ""
	}
	for _, d := range dirs {
		cand := filepath.Join(d, soname)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return ""
}

// parseLdSoConf returns directory paths listed in a simple ld.so.conf file.
// It ignores comments and basic "include" lines (we separately scan the
// common /etc/ld.so.conf.d directory).
func parseLdSoConf(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for line := range strings.SplitSeq(string(data), checkLineSeparator) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ldCommentHash) || strings.HasPrefix(line, ldCommentSemi) {
			continue
		}
		if strings.HasPrefix(line, ldSoIncludePrefix) {
			continue // we handle .d explicitly
		}
		out = append(out, line)
	}
	return out
}

// dedupPreserveOrder removes duplicate directories while keeping the first
// occurrence (used after prepending LD_LIBRARY_PATH).
func dedupPreserveOrder(dirs []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}
