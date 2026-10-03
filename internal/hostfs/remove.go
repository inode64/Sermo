package hostfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// RemoveNoSymlinkAncestors removes a path through a pinned parent directory.
// Every ancestor must be a real directory, including when it is replaced during
// traversal. The final component may itself be a symlink: only that link is
// removed. Recursive deletion stays confined to the pinned parent.
func RemoveNoSymlinkAncestors(path string, recursive bool) error {
	if err := check(path); err != nil {
		return err
	}
	if path == string(filepath.Separator) {
		return fmt.Errorf("%w: cannot remove filesystem root", ErrPath)
	}
	root, err := openDirectoryNoSymlinks(filepath.Dir(path))
	if err != nil {
		if recursive && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open cleanup parent without symlinks for %s: %w", path, err)
	}
	defer func() { _ = root.Close() }()
	if recursive {
		err = root.RemoveAll(filepath.Base(path))
	} else {
		err = root.Remove(filepath.Base(path))
	}
	if err != nil {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

func openDirectoryNoSymlinks(path string) (*os.Root, error) {
	root, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, fmt.Errorf("open filesystem root: %w", err)
	}
	for part := range strings.SplitSeq(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		child, err := openChildDirectoryNoSymlinks(root, part)
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		root = child
	}
	return root, nil
}

func openChildDirectoryNoSymlinks(parent *os.Root, name string) (*os.Root, error) {
	// OpenFile supplies O_NOFOLLOW, which OpenRoot does not expose. Compare the
	// two pinned descriptors before using the child Root, so replacement between
	// these opens cannot redirect cleanup to another directory.
	file, err := parent.OpenFile(name, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open directory %s without symlinks: %w", name, err)
	}
	defer func() { _ = file.Close() }()
	before, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat directory %s: %w", name, err)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("open child root %s: %w", name, err)
	}
	after, err := child.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = child.Close()
		return nil, fmt.Errorf("directory %s changed during cleanup: %w", name, errors.Join(err, os.ErrInvalid))
	}
	return child, nil
}
