package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"

	"sermo/internal/hostfs"
)

const libraryRootAncestorLimit = 64

type libraryRootIdentity struct {
	process      ExecutableFile
	processMount uint64
	namespace    ExecutableFile
	host         ExecutableFile
}

// pinnedNamespaceRoot retains both the mount namespace and its kernel-held
// filesystem root. Walking descriptors preserves private mounts; resolving a
// procfs symlink to a host pathname would silently lose that view.
func pinnedNamespaceRoot(ctx context.Context, pid int, expected libraryRootIdentity) (string, func(), error) {
	namespacePath := PIDPath(pid, ProcFileMountNamespace)
	namespace, err := hostfs.Open(namespacePath)
	if err != nil {
		return "", nil, fmt.Errorf("pin library mount namespace: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = namespace.Close()
		}
	}()
	if err := verifyLibraryRootIdentity(namespace, expected.namespace); err != nil {
		return "", nil, err
	}
	rootPath := PIDPath(pid, ProcFileRoot)
	root, err := hostfs.OpenFile(rootPath, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", nil, fmt.Errorf("pin library process root: %w", err)
	}
	if err := verifyLibraryRootIdentity(root, expected.process); err != nil {
		_ = root.Close()
		return "", nil, err
	}
	rootMount, mountErr := hostfs.MountID(root)
	if mountErr != nil || rootMount != expected.processMount {
		_ = root.Close()
		return "", nil, errors.New("library process root mount changed or is unreadable")
	}
	// Keep the original mount alive too: otherwise an unmount could recycle
	// its numeric ID while the namespace's ancestor root remains pinned.
	processRoot := root
	walkRoot, err := hostfs.OpenFile("/proc/self/fd/"+strconv.FormatUint(uint64(processRoot.Fd()), numericIDBase), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = processRoot.Close()
		return "", nil, fmt.Errorf("duplicate pinned library process root: %w", err)
	}
	root, err = libraryRootAncestor(ctx, walkRoot, expected.host)
	if err != nil {
		_ = processRoot.Close()
		return "", nil, err
	}
	for path, identity := range map[string]ExecutableFile{rootPath: expected.process, namespacePath: expected.namespace} {
		current, statErr := statLibraryFile(path)
		if statErr != nil || current.identity != identity {
			_ = root.Close()
			_ = processRoot.Close()
			return "", nil, errors.New("library process root or namespace changed")
		}
	}
	keep = true
	return "/proc/self/fd/" + strconv.FormatUint(uint64(root.Fd()), numericIDBase), func() {
		_ = root.Close()
		_ = processRoot.Close()
		_ = namespace.Close()
	}, nil
}

func libraryDirectoryMountID(path string) (uint64, error) {
	file, err := hostfs.OpenFile(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, fmt.Errorf("open library root mount: %w", err)
	}
	defer func() { _ = file.Close() }()
	id, err := hostfs.MountID(file)
	if err != nil {
		return 0, fmt.Errorf("read library root mount identity: %w", err)
	}
	return id, nil
}

func verifyLibraryRootIdentity(file *os.File, expected ExecutableFile) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat pinned library root identity: %w", err)
	}
	current, err := libraryFileInfo(info)
	if err != nil || current.identity != expected {
		return errors.New("pinned library root identity changed")
	}
	return nil
}

// libraryRootAncestor consumes start and returns only an ancestor with the
// exact host-root device/inode. A foreign root, a cycle, cancellation or the
// traversal bound closes every descriptor and leaves the scan incomplete.
func libraryRootAncestor(ctx context.Context, start *os.File, expected ExecutableFile) (*os.File, error) {
	current := start
	for range libraryRootAncestorLimit {
		if err := ctx.Err(); err != nil {
			_ = current.Close()
			return nil, fmt.Errorf("find library namespace root: %w", err)
		}
		info, err := current.Stat()
		if err != nil {
			_ = current.Close()
			return nil, fmt.Errorf("stat library root ancestor: %w", err)
		}
		identity, err := libraryFileInfo(info)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		parent, atRoot, err := libraryRootParent(current, info)
		if err != nil {
			_ = current.Close()
			return nil, fmt.Errorf("find library root parent: %w", err)
		}
		if atRoot {
			_ = parent.Close()
			if identity.identity == expected {
				return current, nil
			}
			_ = current.Close()
			return nil, errors.New("library namespace has a foreign root")
		}
		_ = current.Close()
		current = parent
	}
	_ = current.Close()
	return nil, errors.New("library namespace root exceeds ancestor limit")
}

func libraryRootParent(current *os.File, info os.FileInfo) (*os.File, bool, error) {
	parent, err := hostfs.OpenParentDirectory(current)
	if err != nil {
		return nil, false, fmt.Errorf("open library ancestor parent: %w", err)
	}
	parentInfo, err := parent.Stat()
	if err != nil {
		_ = parent.Close()
		return nil, false, fmt.Errorf("stat library ancestor parent: %w", err)
	}
	if !os.SameFile(info, parentInfo) {
		return parent, false, nil
	}
	beforeMount, beforeErr := hostfs.MountID(current)
	afterMount, afterErr := hostfs.MountID(parent)
	if err := errors.Join(beforeErr, afterErr); err != nil {
		_ = parent.Close()
		return nil, false, fmt.Errorf("verify library ancestor mount: %w", err)
	}
	return parent, beforeMount == afterMount, nil
}
