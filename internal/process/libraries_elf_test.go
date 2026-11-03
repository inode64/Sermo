package process

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLibraryReplacementRequiresVerifiedSONAME(t *testing.T) {
	for _, name := range []string{"replacement", "independent numbering", "independent SONAME mismatch", "private plugin release", "empty release component", "unversioned object", "old unreadable", "different old name", "path traversal", "alias missing", "not regular", "same inode", "new unreadable", "different SONAME", "different class", "different machine", "changed file"} {
		t.Run(name, func(t *testing.T) {
			oldID := ExecutableFile{Device: 1, Inode: 10}
			installed := libraryFile{identity: ExecutableFile{Device: 1, Inode: 20}, regular: true, modified: 30, changed: 40}
			old := libraryELF{soname: "libfixture.so.1", class: elf.ELFCLASS64, machine: elf.EM_X86_64}
			current := old
			current.file = installed
			r := libraryReader{
				stat: func(path string) (libraryFile, error) {
					if path != "/proc/42/root/lib/libfixture.so.1" {
						t.Fatalf("unexpected replacement path: %s", path)
					}
					switch name {
					case "alias missing":
						return libraryFile{}, os.ErrNotExist
					case "not regular":
						return libraryFile{}, nil
					case "same inode":
						return libraryFile{identity: oldID, regular: true}, nil
					}
					return installed, nil
				},
				elf: func(path string, expected ExecutableFile) (libraryELF, error) {
					if path == "/proc/42/map_files/1000-2000" {
						if expected != oldID {
							t.Fatal("old mapped identity lost")
						}
						if name == "old unreadable" {
							return libraryELF{}, os.ErrPermission
						}
						return old, nil
					}
					if path != "/proc/42/root/lib/libfixture.so.1" || expected != installed.identity {
						t.Fatalf("unexpected installed ELF: %s %+v", path, expected)
					}
					if name == "new unreadable" {
						return libraryELF{}, os.ErrPermission
					}
					return current, nil
				},
			}
			switch name {
			case "different old name":
				old.soname = "libother.so.1"
			case "path traversal":
				old.soname = "../libfixture.so.1"
			case "different SONAME", "independent SONAME mismatch":
				current.soname = "libfixture.so.2"
			case "different class":
				current.class = elf.ELFCLASS32
			case "different machine":
				current.machine = elf.EM_AARCH64
			case "changed file":
				current.file.changed++
			}
			mappedPath := "/lib/libfixture.so.1.2.0"
			switch name {
			case "independent numbering", "independent SONAME mismatch":
				mappedPath = "/lib/libfixture.so.3.53.1"
			case "private plugin release":
				mappedPath = "/lib/libfixture.so.plugin"
			case "empty release component":
				mappedPath = "/lib/libfixture.so.3..1"
			case "unversioned object":
				mappedPath = "/lib/libfixture.so"
			}
			path, file, ok := r.libraryReplacement(42, "/proc/42/root", mappedPath, libraryMapping{identity: oldID, span: "1000-2000"})
			if ok != (name == "replacement" || name == "independent numbering") || (ok && (path != "/lib/libfixture.so.1" || file != installed)) {
				t.Fatalf("replacement = %q %+v %v", path, file, ok)
			}
		})
	}
}

func TestReadLibraryELF(t *testing.T) {
	for _, name := range []string{"SONAME", "wrong inode", "not ELF", "missing SONAME", "multiple SONAMEs", "missing file", "directory"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "libfixture.so.1.2.0")
			names := []string{"libfixture.so.1"}
			if name == "missing SONAME" {
				names = nil
			} else if name == "multiple SONAMEs" {
				names = append(names, "libother.so.1")
			}
			writeLibraryELFFixture(t, path, names)
			if name == "not ELF" {
				if err := os.WriteFile(path, []byte("not ELF"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "directory" {
				path = filepath.Dir(path)
			}
			file, err := statLibraryFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if name == "wrong inode" {
				file.identity.Inode++
			} else if name == "missing file" {
				path += ".absent"
			}
			got, err := readLibraryELF(path, file.identity)
			if name != "SONAME" {
				if err == nil {
					t.Fatal("unverified ELF accepted")
				}
				return
			}
			if err != nil || got.file != file || got.soname != names[0] || got.class != elf.ELFCLASS64 || got.machine != elf.EM_X86_64 {
				t.Fatalf("ELF = %+v, %v", got, err)
			}
		})
	}
}

func TestScanLibrariesResolvesRemovedVersion(t *testing.T) {
	for _, tc := range []struct{ path, soname string }{
		{"/lib/libpam.so.0.1.0", "libpam.so.0"},
		{"/lib/libsqlite3.so.3.53.1", "libsqlite3.so.0"},
	} {
		t.Run(tc.soname, func(t *testing.T) {
			r, proc := libraryFixture()
			r.readFile = func(string) ([]byte, error) {
				return []byte("1000-2000 r-xp 0000 08:01 42 " + tc.path + " (deleted)\n"), nil
			}
			stat := r.stat
			r.stat = func(path string) (libraryFile, error) {
				if strings.HasSuffix(path, tc.path) {
					return libraryFile{}, os.ErrNotExist
				}
				return stat(path)
			}
			r.elf = func(path string, expected ExecutableFile) (libraryELF, error) {
				if strings.Contains(path, "/map_files/") {
					return libraryELF{soname: tc.soname, class: elf.ELFCLASS64, machine: elf.EM_X86_64}, nil
				}
				file, err := r.stat(path)
				if err != nil || file.identity != expected {
					return libraryELF{}, errors.New("unexpected replacement identity")
				}
				return libraryELF{file: file, soname: tc.soname, class: elf.ELFCLASS64, machine: elf.EM_X86_64}, nil
			}
			report, err := r.scan(t.Context(), []Process{proc})
			if err != nil || len(report.Libraries) != 1 {
				t.Fatalf("scan = %+v, %v", report, err)
			}
			got := report.Libraries[0]
			if got.Missing || got.Path != tc.path || got.ReplacementPath != "/lib/"+tc.soname || got.Installed.Inode != 43 {
				t.Fatalf("finding = %+v", got)
			}
		})
	}
}

func writeLibraryELFFixture(t *testing.T, path string, names []string) {
	t.Helper()
	strings := []byte{0}
	var dynamic []elf.Dyn64
	for _, name := range names {
		dynamic = append(dynamic, elf.Dyn64{Tag: int64(elf.DT_SONAME), Val: uint64(len(strings))})
		strings = append(strings, name...)
		strings = append(strings, 0)
	}
	dynamic = append(dynamic, elf.Dyn64{Tag: int64(elf.DT_NULL)})
	const headerSize, sectionSize, dynamicSize = 64, 64, 16
	dynamicOffset := uint64(headerSize + len(strings))
	sectionOffset := dynamicOffset + uint64(len(dynamic)*dynamicSize)
	header := elf.Header64{
		Ident: [elf.EI_NIDENT]byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)},
		Type:  uint16(elf.ET_DYN), Machine: uint16(elf.EM_X86_64), Version: uint32(elf.EV_CURRENT),
		Shoff: sectionOffset, Ehsize: headerSize, Shentsize: sectionSize, Shnum: 3,
	}
	var data bytes.Buffer
	for _, value := range []any{header, strings, dynamic, elf.Section64{},
		elf.Section64{Type: uint32(elf.SHT_STRTAB), Off: headerSize, Size: uint64(len(strings)), Addralign: 1},
		elf.Section64{Type: uint32(elf.SHT_DYNAMIC), Off: dynamicOffset, Size: uint64(len(dynamic) * dynamicSize), Link: 1, Addralign: 8, Entsize: dynamicSize}} {
		if err := binary.Write(&data, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
