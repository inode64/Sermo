package pkgdb

import (
	"path/filepath"
	"strings"
)

// pathSet holds the FNV-1a 64-bit hash of every indexed path. A Gentoo
// database can list hundreds of thousands of files; hashing keeps the index
// a few megabytes instead of holding every string.
//
// aliases map a merged directory the database still names (/usr/sbin, /sbin,
// /lib) to the directory it is a symlink to on this host (/usr/bin, /usr/lib):
// the kernel resolves a running executable through the link, so the index
// must answer for the resolved spelling too.
type pathSet struct {
	hashes  map[uint64]struct{}
	aliases []dirAlias
}

// dirAlias records that paths under from live under to on this host.
type dirAlias struct {
	from string // "/usr/sbin"
	to   string // "/usr/bin"
}

func newPathSet(aliases []dirAlias) pathSet {
	return pathSet{hashes: map[uint64]struct{}{}, aliases: aliases}
}

// mergedDirs are the directories a merged-usr host may turn into symlinks.
var mergedDirs = []string{"/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32",
	"/usr/sbin", "/usr/bin", "/usr/lib", "/usr/lib32", "/usr/lib64", "/usr/libx32"}

// FNV-1a 64-bit parameters (Fowler–Noll–Vo).
const (
	fnvOffset64 uint64 = 14695981039346656037
	fnvPrime64  uint64 = 1099511628211
)

func hashPath(p string) uint64 {
	h := fnvOffset64
	for i := range len(p) {
		h ^= uint64(p[i])
		h *= fnvPrime64
	}
	return h
}

// add indexes a cleaned absolute path and, when its directory is a symlink on
// this host, the path as the kernel resolves it.
func (s pathSet) add(path string) {
	p := filepath.Clean(path)
	s.hashes[hashPath(p)] = struct{}{}
	for _, alias := range s.aliases {
		if rest, ok := strings.CutPrefix(p, alias.from+"/"); ok {
			s.hashes[hashPath(alias.to+"/"+rest)] = struct{}{}
		}
	}
}

func (s pathSet) has(p string) bool {
	_, ok := s.hashes[hashPath(p)]
	return ok
}
