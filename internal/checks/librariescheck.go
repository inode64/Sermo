package checks

import (
	"context"
	"debug/elf"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"sermo/internal/hostfs"

	"sermo/internal/execx"
	"sermo/internal/strutil"
)

const (
	ldSoConfDir       = "/etc/ld.so.conf.d"
	ldSoConfFile      = "/etc/ld.so.conf"
	ldSoConfSuffix    = ".conf"
	ldSoIncludePrefix = "include "
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
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	ef, err := elf.Open(c.binary)
	if err != nil {
		if os.IsNotExist(err) {
			return c.result(false, c.binary+": "+err.Error(), start)
		}
		return c.unavailableResult(c.binary+": "+err.Error(), start)
	}
	defer func() { _ = ef.Close() }()

	needed, err := ef.DynString(elf.DT_NEEDED)
	if err != nil || len(needed) == 0 {
		return c.result(true, c.binary+": static binary, no shared libraries", start)
	}

	resolver := libraryResolver{
		target: elfTarget{class: ef.Class, machine: ef.Machine},
		dirs:   collectLibrarySearchDirs(c.binary, ef),
		seen:   make(map[string]bool),
	}
	missing := resolver.resolve(ctx, needed, nil)
	if err := ctx.Err(); err != nil {
		return c.unavailableResult(c.binary+": "+execx.ContextFailure(err, c.timeout), start)
	}
	if len(missing) > 0 {
		return c.result(false, c.binary+": missing shared libraries: "+strings.Join(missing, ", "), start)
	}
	return c.result(true, c.binary+": all shared libraries resolve", start)
}

// elfTarget is the ELF class and machine every library of a binary must share.
// The dynamic linker skips a candidate of another class or architecture — the
// 32-bit copies multilib distributions keep in /lib and /usr/lib — and keeps
// searching, so a name match alone does not make a library resolvable.
type elfTarget struct {
	class   elf.Class
	machine elf.Machine
}

// libraryResolver walks a binary's DT_NEEDED tree. dirs is the binary's search
// path; each library additionally searches its own DT_RUNPATH/DT_RPATH first,
// with $ORIGIN at that library, as the dynamic linker does for its
// dependencies.
type libraryResolver struct {
	target elfTarget
	dirs   []string
	seen   map[string]bool
}

// resolve recursively resolves DT_NEEDED entries (including transitive
// dependencies of the resolved libraries), searching own before the binary's
// path. It returns the list of sonames that could not be located.
func (r *libraryResolver) resolve(ctx context.Context, needed, own []string) []string {
	var missing []string
	dirs := append(slices.Clip(own), r.dirs...)
	for _, soname := range needed {
		if err := ctx.Err(); err != nil {
			return missing
		}
		if r.seen[soname] {
			continue
		}
		r.seen[soname] = true

		lib, path, openFailed := openLibrary(soname, dirs, r.target)
		if lib == nil {
			reason := soname
			if openFailed {
				reason = soname + " (open failed)"
			}
			missing = append(missing, reason)
			continue
		}
		// Collect the library's own DT_NEEDED (transitive) and search path.
		subNeeded, _ := lib.DynString(elf.DT_NEEDED)
		subOwn := expandLibraryPath(dynamicLibraryPath(lib), path)
		_ = lib.Close()

		missing = append(missing, r.resolve(ctx, subNeeded, subOwn)...)
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
	dirs = append(dirs, expandLibraryPath(dynamicLibraryPath(ef), binary)...)

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
	dirs = append(dirs, parseLdSoConf(ldSoConfFile)...)
	// Common drop-in directory even if main conf doesn't include it.
	if entries, err := os.ReadDir(ldSoConfDir); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ldSoConfSuffix) {
				dirs = append(dirs, parseLdSoConf(filepath.Join(ldSoConfDir, e.Name()))...)
			}
		}
	}

	return strutil.Unique(dirs)
}

func dynamicLibraryPath(ef *elf.File) string {
	if paths, _ := ef.DynString(elf.DT_RUNPATH); len(paths) > 0 && paths[0] != "" {
		return paths[0]
	}
	if paths, _ := ef.DynString(elf.DT_RPATH); len(paths) > 0 {
		return paths[0]
	}
	return ""
}

func expandLibraryPath(paths, binary string) []string {
	dirs := make([]string, 0)
	for path := range strings.SplitSeq(paths, ldPathSeparator) {
		if path != "" {
			dirs = append(dirs, expandOrigin(path, binary))
		}
	}
	return dirs
}

func expandOrigin(p, binary string) string {
	dir := filepath.Dir(binary)
	p = strings.ReplaceAll(p, elfOriginToken, dir)
	p = strings.ReplaceAll(p, elfOriginBraced, dir)
	return filepath.Clean(p)
}

// openLibrary opens the first candidate for soname in dirs that matches target.
// openFailed reports that a candidate existed but could not be read as ELF, so
// the caller can tell an unreadable library from an absent one. The caller
// closes the returned file.
func openLibrary(soname string, dirs []string, target elfTarget) (lib *elf.File, path string, openFailed bool) {
	candidates := []string{soname}
	if !filepath.IsAbs(soname) {
		candidates = candidates[:0]
		for _, d := range dirs {
			candidates = append(candidates, filepath.Join(d, soname))
		}
	}
	for _, cand := range candidates {
		ef, err := elf.Open(cand)
		if err != nil {
			if !os.IsNotExist(err) {
				openFailed = true
			}
			continue
		}
		if ef.Class == target.class && ef.Machine == target.machine {
			return ef, cand, false
		}
		_ = ef.Close()
	}
	return nil, "", openFailed
}

// parseLdSoConf returns directory paths listed in a simple ld.so.conf file.
// It ignores comments and basic "include" lines (we separately scan the
// common /etc/ld.so.conf.d directory).
func parseLdSoConf(path string) []string {
	data, err := hostfs.ReadFile(path)
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
