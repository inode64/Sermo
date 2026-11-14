package checks

import (
	"bytes"
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"sermo/internal/execx"
	"sermo/internal/hostfs"
	"sermo/internal/process"
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
// observable in process mappings, rather than ELF metadata on the script: one
// that names no absolute interpreter (`#!/usr/bin/env python3`) or one whose
// interpreter is itself a script.
var ErrLibraryScript = errors.New("script has no ELF dependency metadata")

// maxShebangBytes bounds the first line read from a script, the most of an
// interpreter line Linux itself reads.
const maxShebangBytes = 256

// LibraryDependencies is the on-disk ELF dependency tree, separate from the
// process mappings that prove which dynamic modules are actually loaded.
// Interpreter names the ELF a script's first line runs, whose tree it is.
type LibraryDependencies struct {
	Binary      string   `json:"binary"`
	Interpreter string   `json:"interpreter,omitempty"`
	Paths       []string `json:"paths"`
	Missing     []string `json:"missing"`
}

func (c librariesCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	report, err := InspectLibraries(ctx, c.binary)
	if ctx.Err() != nil {
		return c.unavailableResult(c.binary+": "+execx.ContextFailure(ctx.Err(), c.timeout), run.start)
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return c.result(false, err.Error(), run.start)
		}
		return c.unavailableResult(err.Error(), run.start)
	}
	subject := c.binary
	if report.Interpreter != "" {
		subject += " (interpreter " + report.Interpreter + ")"
	}
	message := subject + ": all shared libraries resolve"
	if len(report.Paths) == 0 && len(report.Missing) == 0 {
		message = subject + ": static binary, no shared libraries"
	}
	if len(report.Missing) > 0 {
		message = subject + ": missing shared libraries: " + strings.Join(report.Missing, ", ")
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
	return inspectExecutable(ctx, binary, searchDirs, true)
}

// inspectExecutable resolves binary's ELF dependency tree. A script loads no
// library itself: the interpreter its first line names does, and an
// interpreter upgrade rewrites that line (#!/usr/bin/python3.14), so following
// it keeps the check on the interpreter the service starts with now instead of
// a pinned version. Only one level is followed, which also stops a script that
// names itself.
func inspectExecutable(ctx context.Context, binary string, searchDirs func(string, *elf.File) []string, followScript bool) (LibraryDependencies, error) {
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
		head, readErr := hostfs.ReadRegularFile(binary, maxShebangBytes)
		if readErr != nil || !bytes.HasPrefix(head, []byte(elfScriptPrefix)) {
			return report, fmt.Errorf("inspect %s: %w", binary, err)
		}
		interpreter := scriptInterpreter(head)
		if !followScript || interpreter == "" {
			return report, fmt.Errorf("inspect %s: %w", binary, ErrLibraryScript)
		}
		tree, err := inspectExecutable(ctx, interpreter, searchDirs, false)
		tree.Interpreter, tree.Binary = tree.Binary, binary
		if err != nil {
			return tree, fmt.Errorf("interpreter of %s: %w", binary, err)
		}
		return tree, nil
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

// ProgramInstalled reports whether path names a program that is installed: a
// file that exists and, for a script, whose first line names an interpreter
// that exists too, the one level the libraries check follows. A script whose
// interpreter is delegated to env counts as installed, since that lookup
// depends on the environment it starts in.
func ProgramInstalled(path string) bool {
	resolved, err := hostfs.EvalSymlinksTarget(path)
	if err != nil {
		return false
	}
	head, err := hostfs.ReadRegularFile(resolved, maxShebangBytes)
	if err != nil || !bytes.HasPrefix(head, []byte(elfScriptPrefix)) {
		// It exists; what is not a readable script is taken as the program.
		return true
	}
	interpreter := scriptInterpreter(head)
	if interpreter == "" {
		return true
	}
	_, err = hostfs.StatTarget(interpreter)
	return err == nil
}

// scriptInterpreter returns the absolute interpreter a script's first line
// names, or "" when it names none or delegates the choice to env, whose PATH
// lookup depends on the environment the service starts in.
func scriptInterpreter(head []byte) string {
	line, _, _ := bytes.Cut(bytes.TrimPrefix(head, []byte(elfScriptPrefix)), []byte{'\n'})
	fields := strings.Fields(string(line))
	if len(fields) == 0 || !filepath.IsAbs(fields[0]) || filepath.Base(fields[0]) == "env" {
		return ""
	}
	return filepath.Clean(fields[0])
}

// ResolveExecutableDependencies is the libraries preflight's resolver applied
// to a running process's executable: exe is the canonical path the process
// reports, read through root (its /proc/PID/root view), with that root's own
// ld.so.conf. Every path it returns is inside root, as the process sees it.
func ResolveExecutableDependencies(ctx context.Context, root, exe string) (process.ExecutableDependencies, error) {
	if err := ctx.Err(); err != nil {
		return process.ExecutableDependencies{}, fmt.Errorf("resolve libraries: %w", err)
	}
	ef, err := elf.Open(root + exe)
	if err != nil {
		return process.ExecutableDependencies{}, fmt.Errorf("inspect %s: %w", exe, err)
	}
	defer func() { _ = ef.Close() }()
	needed, err := ef.DynString(elf.DT_NEEDED)
	if err != nil {
		return process.ExecutableDependencies{}, fmt.Errorf("read dependencies of %s: %w", exe, err)
	}
	resolver := libraryResolver{
		root:     root,
		target:   elfTarget{class: ef.Class, machine: ef.Machine},
		dirs:     librarySearchDirs(root, exe, ef),
		seen:     make(map[string]bool),
		resolved: make(map[string]string),
	}
	missing := resolver.resolve(ctx, needed, nil)
	if err := ctx.Err(); err != nil {
		return process.ExecutableDependencies{}, fmt.Errorf("resolve %s: %w", exe, err)
	}
	return process.ExecutableDependencies{Resolved: resolver.resolved, Missing: missing}, resolver.err
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
	// root prefixes every file the resolver opens; paths it reports and
	// expands $ORIGIN with stay inside root. Empty is the host root.
	root   string
	target elfTarget
	dirs   []string
	seen   map[string]bool
	paths  []string
	// resolved, when set, records each SONAME's path.
	resolved map[string]string
	err      error
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

		lib, path, openFailed := openLibrary(r.root, soname, dirs, r.target)
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
		if r.resolved != nil {
			r.resolved[soname] = path
		}
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
	return librarySearchDirs("", binary, ef)
}

// librarySearchDirs is collectLibrarySearchDirs with ld.so.conf read under
// root; binary and the returned directories are paths inside root.
func librarySearchDirs(root, binary string, ef *elf.File) []string {
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
	dirs = append(dirs, parseLdSoConf(root+ldSoConfFile)...)
	// Common drop-in directory even if main conf doesn't include it.
	if entries, err := hostfs.ReadDir(root + ldSoConfDir); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ldSoConfSuffix) {
				dirs = append(dirs, parseLdSoConf(root+filepath.Join(ldSoConfDir, e.Name()))...)
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

// openLibrary opens the first candidate for soname in dirs that matches target,
// reading each under root and returning its path inside root.
// openFailed reports that a candidate existed but could not be read as ELF, so
// the caller can tell an unreadable library from an absent one. The caller
// closes the returned file.
func openLibrary(root, soname string, dirs []string, target elfTarget) (lib *elf.File, path string, openFailed bool) {
	candidates := []string{soname}
	if !filepath.IsAbs(soname) {
		candidates = candidates[:0]
		for _, d := range dirs {
			candidates = append(candidates, filepath.Join(d, soname))
		}
	}
	for _, cand := range candidates {
		ef, err := elf.Open(root + cand)
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
