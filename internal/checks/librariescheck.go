package checks

import (
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"sermo/internal/execx"
	"sermo/internal/hostfs"
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
	elfScriptPrefix   = "#!"
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

// ErrLibraryScript identifies a script whose interpreter dependencies are
// observable in process mappings, rather than ELF metadata on the script.
var ErrLibraryScript = errors.New("script has no ELF dependency metadata")

// LibraryDependencies is the on-disk ELF dependency tree, separate from the
// process mappings that prove which dynamic modules are actually loaded.
type LibraryDependencies struct {
	Binary  string   `json:"binary"`
	Paths   []string `json:"paths"`
	Missing []string `json:"missing"`
}

func (c librariesCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	report, err := InspectLibraries(ctx, c.binary)
	if ctx.Err() != nil {
		return c.unavailableResult(c.binary+": "+execx.ContextFailure(ctx.Err(), c.timeout), run.start)
	}
	if err != nil {
		if os.IsNotExist(err) {
			return c.result(false, err.Error(), run.start)
		}
		return c.unavailableResult(err.Error(), run.start)
	}
	message := c.binary + ": all shared libraries resolve"
	if len(report.Paths) == 0 && len(report.Missing) == 0 {
		message = c.binary + ": static binary, no shared libraries"
	}
	if len(report.Missing) > 0 {
		message = c.binary + ": missing shared libraries: " + strings.Join(report.Missing, ", ")
	}
	result := c.result(len(report.Missing) == 0, message, run.start)
	result.Data = map[string]any{DataKeyDependencies: report}
	return result
}

// InspectLibraries uses the same resolver as the libraries preflight. It never
// executes the binary or a dynamic loader; callers supply a bounded context.
func InspectLibraries(ctx context.Context, binary string) (LibraryDependencies, error) {
	return inspectLibraries(ctx, binary, collectLibrarySearchDirs)
}

func inspectLibraries(ctx context.Context, binary string, searchDirs func(string, *elf.File) []string) (LibraryDependencies, error) {
	report := LibraryDependencies{Binary: binary}
	if err := ctx.Err(); err != nil {
		return report, fmt.Errorf("inspect libraries: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(binary)
	if err != nil {
		return report, fmt.Errorf("resolve library binary %s: %w", binary, err)
	}
	binary, report.Binary = resolved, resolved
	ef, err := elf.Open(binary)
	if err != nil {
		if prefix, readErr := hostfs.ReadRegularFile(binary, int64(len(elfScriptPrefix))); readErr == nil && string(prefix) == elfScriptPrefix {
			return report, fmt.Errorf("inspect %s: %w", binary, ErrLibraryScript)
		}
		return report, fmt.Errorf("inspect %s: %w", binary, err)
	}
	defer func() { _ = ef.Close() }()
	needed, err := ef.DynString(elf.DT_NEEDED)
	if err != nil {
		return report, fmt.Errorf("read dependencies of %s: %w", binary, err)
	}
	if len(needed) == 0 {
		return report, nil
	}
	resolver := libraryResolver{
		target: elfTarget{class: ef.Class, machine: ef.Machine},
		dirs:   searchDirs(binary, ef),
		seen:   make(map[string]bool),
	}
	report.Missing = resolver.resolve(ctx, needed, nil)
	report.Paths = strutil.SortedUnique(resolver.paths)
	if err := ctx.Err(); err != nil {
		return report, fmt.Errorf("inspect %s: %w", binary, err)
	}
	return report, resolver.err
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
	paths  []string
	err    error
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
				r.err = fmt.Errorf("read library %s: no readable ELF candidate", soname)
			}
			missing = append(missing, reason)
			continue
		}
		r.paths = append(r.paths, path)
		// Collect the library's own DT_NEEDED (transitive) and search path.
		subNeeded, err := lib.DynString(elf.DT_NEEDED)
		if err != nil {
			r.err = fmt.Errorf("read dependencies of %s: %w", path, err)
		}
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
	if entries, err := hostfs.ReadDir(ldSoConfDir); err == nil {
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
