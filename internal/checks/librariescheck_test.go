package checks

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// testELF describes the dynamic section of a fixture shared object.
type testELF struct {
	class   elf.Class
	machine elf.Machine
	needed  []string
	runpath string
}

// writeTestELF writes a minimal little-endian ELF with only the sections the
// resolver reads: .dynstr and .dynamic (DT_NEEDED, DT_RUNPATH).
func writeTestELF(t *testing.T, path string, spec testELF) {
	t.Helper()
	strtab := []byte{0}
	addString := func(s string) uint64 {
		off := uint64(len(strtab))
		strtab = append(strtab, s...)
		strtab = append(strtab, 0)
		return off
	}
	type dyn struct {
		tag elf.DynTag
		val uint64
	}
	var dyns []dyn
	for _, n := range spec.needed {
		dyns = append(dyns, dyn{elf.DT_NEEDED, addString(n)})
	}
	if spec.runpath != "" {
		dyns = append(dyns, dyn{elf.DT_RUNPATH, addString(spec.runpath)})
	}
	dyns = append(dyns, dyn{elf.DT_NULL, 0})

	var buf bytes.Buffer
	write := func(v any) {
		if err := binary.Write(&buf, binary.LittleEndian, v); err != nil {
			t.Fatal(err)
		}
	}
	ident := [elf.EI_NIDENT]byte{0x7f, 'E', 'L', 'F', byte(spec.class), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)}
	const sectionCount = 3 // null, .dynstr, .dynamic
	if spec.class == elf.ELFCLASS64 {
		const headerSize, sectionSize, dynSize = 64, 64, 16
		dynstrOff := uint64(headerSize)
		dynOff := dynstrOff + uint64(len(strtab))
		dynOff += (8 - dynOff%8) % 8
		shOff := dynOff + uint64(len(dyns)*dynSize)
		write(elf.Header64{
			Ident: ident, Type: uint16(elf.ET_DYN), Machine: uint16(spec.machine), Version: uint32(elf.EV_CURRENT),
			Shoff: shOff, Ehsize: headerSize, Shentsize: sectionSize, Shnum: sectionCount,
		})
		buf.Write(strtab)
		buf.Write(make([]byte, dynOff-uint64(buf.Len())))
		for _, d := range dyns {
			write(elf.Dyn64{Tag: int64(d.tag), Val: d.val})
		}
		write(elf.Section64{})
		write(elf.Section64{Type: uint32(elf.SHT_STRTAB), Off: dynstrOff, Size: uint64(len(strtab)), Addralign: 1})
		write(elf.Section64{Type: uint32(elf.SHT_DYNAMIC), Off: dynOff, Size: uint64(len(dyns) * dynSize), Link: 1, Addralign: 8, Entsize: dynSize})
	} else {
		const headerSize, sectionSize, dynSize = 52, 40, 8
		dynstrOff := uint32(headerSize)
		dynOff := dynstrOff + uint32(len(strtab))
		dynOff += (4 - dynOff%4) % 4
		shOff := dynOff + uint32(len(dyns)*dynSize)
		write(elf.Header32{
			Ident: ident, Type: uint16(elf.ET_DYN), Machine: uint16(spec.machine), Version: uint32(elf.EV_CURRENT),
			Shoff: shOff, Ehsize: headerSize, Shentsize: sectionSize, Shnum: sectionCount,
		})
		buf.Write(strtab)
		buf.Write(make([]byte, int(dynOff)-buf.Len()))
		for _, d := range dyns {
			write(elf.Dyn32{Tag: int32(d.tag), Val: uint32(d.val)})
		}
		write(elf.Section32{})
		write(elf.Section32{Type: uint32(elf.SHT_STRTAB), Off: dynstrOff, Size: uint32(len(strtab)), Addralign: 1})
		write(elf.Section32{Type: uint32(elf.SHT_DYNAMIC), Off: dynOff, Size: uint32(len(dyns) * dynSize), Link: 1, Addralign: 4, Entsize: dynSize})
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The dynamic linker only loads a library of the binary's own ELF class and
// machine, and each library's DT_RUNPATH (with its own $ORIGIN) finds that
// library's dependencies.
func TestResolveNeededFollowsTheLoader(t *testing.T) {
	x8664 := elfTarget{class: elf.ELFCLASS64, machine: elf.EM_X86_64}
	cases := []struct {
		name        string
		files       map[string]testELF
		needed      []string
		dirs        []string
		wantMissing []string
	}{
		{
			name: "a 32-bit library earlier in the path does not satisfy a 64-bit binary",
			files: map[string]testELF{
				"lib/libsermotest.so.1": {class: elf.ELFCLASS32, machine: elf.EM_386},
			},
			needed:      []string{"libsermotest.so.1"},
			dirs:        []string{"lib", "lib64"},
			wantMissing: []string{"libsermotest.so.1"},
		},
		{
			name: "the matching library later in the path is used",
			files: map[string]testELF{
				"lib/libsermotest.so.1":   {class: elf.ELFCLASS32, machine: elf.EM_386, needed: []string{"libsermo32only.so.1"}},
				"lib64/libsermotest.so.1": {class: elf.ELFCLASS64, machine: elf.EM_X86_64},
			},
			needed: []string{"libsermotest.so.1"},
			dirs:   []string{"lib", "lib64"},
		},
		{
			name: "a same-class library for another machine is skipped",
			files: map[string]testELF{
				"lib64/libsermotest.so.1": {class: elf.ELFCLASS64, machine: elf.EM_AARCH64},
			},
			needed:      []string{"libsermotest.so.1"},
			dirs:        []string{"lib64"},
			wantMissing: []string{"libsermotest.so.1"},
		},
		{
			name: "a library's own RUNPATH with $ORIGIN resolves its dependencies",
			files: map[string]testELF{
				"lib64/libsermotest.so.1":        {class: elf.ELFCLASS64, machine: elf.EM_X86_64, needed: []string{"libsermodep.so.2"}, runpath: "$ORIGIN/private"},
				"lib64/private/libsermodep.so.2": {class: elf.ELFCLASS64, machine: elf.EM_X86_64},
			},
			needed: []string{"libsermotest.so.1"},
			dirs:   []string{"lib64"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, spec := range tc.files {
				writeTestELF(t, filepath.Join(root, rel), spec)
			}
			dirs := make([]string, 0, len(tc.dirs))
			for _, d := range tc.dirs {
				dirs = append(dirs, filepath.Join(root, d))
			}
			resolver := libraryResolver{target: x8664, dirs: dirs, seen: map[string]bool{}}
			missing := resolver.resolve(context.Background(), tc.needed, nil)
			if !slices.Equal(missing, tc.wantMissing) {
				t.Fatalf("missing = %v, want %v", missing, tc.wantMissing)
			}
		})
	}
}
