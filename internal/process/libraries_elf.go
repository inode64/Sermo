package process

import (
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"sermo/internal/hostfs"
)

type libraryELF struct {
	file    libraryFile
	soname  string
	class   elf.Class
	machine elf.Machine
}

// readLibraryELF pins the file before reading its metadata. A map_files link
// can disappear or change with a process's mappings; its pathname alone does
// not establish that this is the inode the scan observed.
func readLibraryELF(path string, expected ExecutableFile) (libraryELF, error) {
	f, err := hostfs.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return libraryELF{}, fmt.Errorf("open library ELF: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return libraryELF{}, fmt.Errorf("stat library ELF: %w", err)
	}
	before, err := libraryFileInfo(info)
	if err != nil || !before.regular || before.identity != expected {
		return libraryELF{}, errors.New("library ELF identity differs or is not regular")
	}
	ef, err := elf.NewFile(f)
	if err != nil {
		return libraryELF{}, fmt.Errorf("read library ELF: %w", err)
	}
	names, err := ef.DynString(elf.DT_SONAME)
	if err != nil || ef.Type != elf.ET_DYN || len(names) != 1 {
		return libraryELF{}, errors.New("library ELF has no unique shared-object SONAME")
	}
	info, err = f.Stat()
	if err != nil {
		return libraryELF{}, fmt.Errorf("revalidate library ELF: %w", err)
	}
	after, err := libraryFileInfo(info)
	if err != nil || before != after {
		return libraryELF{}, errors.New("library ELF changed while reading")
	}
	return libraryELF{file: before, soname: names[0], class: ef.Class, machine: ef.Machine}, nil
}

// A removed versioned file is replaceable through its own verified SONAME in
// the same directory, with matching ELF architecture. Filename similarity or an
// ldconfig entry cannot prove that a dynamically loaded plugin still has a
// replacement; closureReplacement covers the one other provable case.
func (r libraryReader) libraryReplacement(pid int, root, path string, mapping libraryMapping) (string, libraryFile, bool) {
	if r.elf == nil || mapping.span == "" {
		return "", libraryFile{}, false
	}
	old, err := r.elf(PIDPath(pid, procFileMapFiles)+"/"+mapping.span, mapping.identity)
	if err != nil || old.soname == "" || filepath.Base(old.soname) != old.soname ||
		!versionedLibraryName(filepath.Base(path), old.soname) {
		return "", libraryFile{}, false
	}
	alias := filepath.Join(filepath.Dir(path), old.soname)
	installed, err := r.stat(root + alias)
	if err != nil || !installed.regular || installed.identity == mapping.identity {
		return "", libraryFile{}, false
	}
	current, err := r.elf(root+alias, installed.identity)
	if err != nil || current.file != installed || current.soname != old.soname ||
		current.class != old.class || current.machine != old.machine {
		return "", libraryFile{}, false
	}
	return alias, installed, true
}

// SQLite's physical release number differs from its ABI major. The verified
// SONAME still identifies its replacement; keep numeric release names within
// the same library family so unrelated private plugins remain advisory.
func versionedLibraryName(name, soname string) bool {
	if strings.HasPrefix(name, soname+".") {
		return true
	}
	family, abi, ok := strings.Cut(soname, ".so.")
	if !ok {
		return false
	}
	if _, err := strconv.ParseUint(abi, 10, 64); err != nil {
		return false
	}
	version, ok := strings.CutPrefix(name, family+".so.")
	if !ok {
		return false
	}
	for component := range strings.SplitSeq(version, ".") {
		if _, err := strconv.ParseUint(component, 10, 64); err != nil {
			return false
		}
	}
	return true
}

// closureReplacement replaces a removed library the executable links: a
// toolchain slot change removes the old compiler's directory, and libstdc++ or
// libgcc_s then live in the new one. The executable's own DT_NEEDED closure
// still names the mapped SONAME, so the loader finds it by SONAME at start, and
// the file it resolves to must be a verified ELF with that SONAME and the same
// architecture. A dlopen plugin or PAM module is outside that closure and stays
// missing: nothing proves where the program would load it from.
func (r libraryReader) closureReplacement(pid int, root string, mapping libraryMapping, closure executableClosure) (string, libraryFile, bool) {
	if !closure.known || closure.err != nil || r.elf == nil || mapping.span == "" {
		return "", libraryFile{}, false
	}
	old, err := r.elf(PIDPath(pid, procFileMapFiles)+"/"+mapping.span, mapping.identity)
	if err != nil || old.soname == "" {
		return "", libraryFile{}, false
	}
	path, linked := closure.deps.Resolved[old.soname]
	if !linked || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", libraryFile{}, false
	}
	installed, err := r.stat(root + path)
	if err != nil || !installed.regular || installed.identity == mapping.identity {
		return "", libraryFile{}, false
	}
	current, err := r.elf(root+path, installed.identity)
	if err != nil || current.file != installed || current.soname != old.soname ||
		current.class != old.class || current.machine != old.machine {
		return "", libraryFile{}, false
	}
	return path, installed, true
}
