package process

import (
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// A removed versioned file is replaceable only through its own verified
// SONAME in the same directory, with matching ELF architecture. Filename
// similarity, an ldconfig entry or the daemon's DT_NEEDED tree cannot prove
// that a dynamically loaded plugin still has a replacement.
func (r libraryReader) libraryReplacement(pid int, root, path string, mapping libraryMapping) (string, libraryFile, bool) {
	if r.elf == nil || mapping.span == "" {
		return "", libraryFile{}, false
	}
	old, err := r.elf(PIDPath(pid, procFileMapFiles)+"/"+mapping.span, mapping.identity)
	if err != nil || old.soname == "" || filepath.Base(old.soname) != old.soname ||
		!strings.HasPrefix(filepath.Base(path), old.soname+".") {
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
