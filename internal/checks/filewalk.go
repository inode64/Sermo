package checks

import (
	"io/fs"
	"strings"
)

// IsHiddenDescendant reports whether entry is a hidden path discovered below
// root. A hidden root named explicitly by the operator is not a descendant and
// remains included.
func IsHiddenDescendant(root, path string, entry fs.DirEntry) bool {
	return path != root && strings.HasPrefix(entry.Name(), ".")
}
