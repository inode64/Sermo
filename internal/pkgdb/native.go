package pkgdb

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"sermo/internal/hostfs"
)

const (
	gentooObjPrefix = "obj "
	gentooSymPrefix = "sym "
	gentooSymArrow  = " -> "
	// scanBufferSize is the initial line buffer; maxLineSize bounds one entry
	// (a path plus its checksum and mtime) so a corrupt file cannot grow it
	// without limit.
	scanBufferSize = 64 << 10
	maxLineSize    = 1 << 20
)

// Gentoo: /var/db/pkg/<category>/<pkg-version>/CONTENTS lists
//
//	obj <path> <md5> <mtime>
//	sym <path> -> <target> <mtime>
//	dir <path>
//
// Paths may contain spaces, so each entry type is split on its known layout.
func (x *Index) buildGentoo(ctx context.Context, paths pathSet) error {
	dbDir := x.join(gentooDBDir)
	categories, err := hostfs.ReadDir(dbDir)
	if err != nil {
		return fmt.Errorf("pkgdb: gentoo: %w", err)
	}
	for _, category := range categories {
		if !category.IsDir() {
			continue
		}
		catDir := filepath.Join(dbDir, category.Name())
		pkgs, err := hostfs.ReadDir(catDir)
		if err != nil {
			return fmt.Errorf("pkgdb: gentoo: %w", err)
		}
		for _, pkg := range pkgs {
			if !pkg.IsDir() {
				continue
			}
			if err := interrupted(ctx); err != nil {
				return err
			}
			// A package directory without CONTENTS is a merge in progress
			// (Portage's -MERGING-<pkg>), not an installed package.
			if err := readGentooContents(filepath.Join(catDir, pkg.Name(), gentooContents), paths); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func readGentooContents(path string, paths pathSet) error {
	return scanLines(path, func(line string) {
		if p, ok := gentooEntryPath(line); ok {
			paths.add(p)
		}
	})
}

// scanLines streams one database file line by line, without carriage returns,
// so a large dpkg or pacman list is never held as one string. A missing file
// is reported as fs.ErrNotExist for callers that skip it.
func scanLines(path string, fn func(line string)) error {
	f, err := hostfs.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, scanBufferSize), maxLineSize)
	for scanner.Scan() {
		fn(strings.TrimRight(scanner.Text(), "\r"))
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

func gentooEntryPath(line string) (string, bool) {
	if rest, ok := strings.CutPrefix(line, gentooObjPrefix); ok {
		// Drop the two trailing fields (md5, mtime); the path keeps its spaces.
		withMD5, _, ok := strings.CutLast(rest, " ")
		if !ok {
			return "", false
		}
		p, _, ok := strings.CutLast(withMD5, " ")
		if !ok || p == "" {
			return "", false
		}
		return p, true
	}
	if rest, ok := strings.CutPrefix(line, gentooSymPrefix); ok {
		p, _, found := strings.Cut(rest, gentooSymArrow)
		if !found || p == "" {
			return "", false
		}
		return p, true
	}
	return "", false
}

// dpkg: /var/lib/dpkg/info/<pkg>.list holds one absolute path per line; the
// first line "/." is the root directory.
func (x *Index) buildDpkg(ctx context.Context, paths pathSet) error {
	infoDir := x.join(dpkgInfoDir)
	entries, err := hostfs.ReadDir(infoDir)
	if err != nil {
		return fmt.Errorf("pkgdb: dpkg: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), dpkgListSuffix) {
			continue
		}
		if err := interrupted(ctx); err != nil {
			return err
		}
		err := scanLines(filepath.Join(infoDir, e.Name()), func(line string) {
			if line != "" && line != "/." && strings.HasPrefix(line, "/") {
				paths.add(line)
			}
		})
		if err != nil {
			return fmt.Errorf("pkgdb: dpkg: %w", err)
		}
	}
	return nil
}

// pacman: /var/lib/pacman/local/<pkg>/files lists, after a %FILES% header,
// one path per line relative to / until a blank line or the next header.
// Directories end with "/".
func (x *Index) buildPacman(ctx context.Context, paths pathSet) error {
	localDir := x.join(pacmanLocalDir)
	entries, err := hostfs.ReadDir(localDir)
	if err != nil {
		return fmt.Errorf("pkgdb: pacman: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if err := interrupted(ctx); err != nil {
			return err
		}
		inFiles := false
		err := scanLines(filepath.Join(localDir, e.Name(), pacmanFilesName), func(line string) {
			switch {
			case strings.HasPrefix(line, "%"):
				inFiles = line == pacmanFilesHeader
			case line == "":
				inFiles = false
			case inFiles && !strings.HasSuffix(line, "/"):
				paths.add("/" + line)
			}
		})
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("pkgdb: pacman: %w", err)
		}
	}
	return nil
}

const pacmanFilesHeader = "%FILES%"

// apk: /lib/apk/db/installed holds one record per package separated by blank
// lines; "F:<dir>" sets the current directory and "R:<name>" names a file in it.
func (x *Index) buildApk(paths pathSet) error {
	dir := ""
	err := scanLines(x.join(apkInstalledFile), func(line string) {
		if line == "" {
			dir = ""
			return
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return
		}
		switch key {
		case "F":
			dir = value
		case "R":
			if value != "" {
				paths.add("/" + dir + "/" + value)
			}
		}
	})
	if err != nil {
		return fmt.Errorf("pkgdb: apk: %w", err)
	}
	return nil
}
