package mountctl

import (
	"sermo/internal/hostfs"
	"sermo/internal/mounts"
	"sermo/internal/process"
)

const (
	procFileMountInfo = "mountinfo"

	reasonMountNamespaceUnverified = "mount namespace could not be verified"
)

// mountNamespaces judges a blocker found by path. /proc links spell a path as
// its process's own mount namespace sees it, so across namespaces the string
// says nothing: a container's /backup may be unrelated to the host's. Refusing
// every other namespace would be wrong too: a hardened unit's private namespace
// receives the host's mounts, and a file held open there keeps umount busy. The
// mount tables decide, because reading them never touches the filesystem, which
// a stat on a dead NFS server would.
type mountNamespaces struct {
	readLink func(string) (string, error)
	readFile func(string) ([]byte, error)
	own      string
	table    []mounts.MountInfo
}

func hostMountNamespaces() mountNamespaces {
	return newMountNamespaces(hostfs.Readlink, hostfs.ReadFile)
}

func newMountNamespaces(readLink func(string) (string, error), readFile func(string) ([]byte, error)) mountNamespaces {
	namespaces := mountNamespaces{readLink: readLink, readFile: readFile}
	if own, err := readLink(process.SelfPath(process.ProcFileMountNamespace)); err == nil {
		namespaces.own = own
	}
	if data, err := readFile(process.SelfPath(procFileMountInfo)); err == nil {
		namespaces.table = mounts.ParseMountInfo(data)
	}
	return namespaces
}

// confirm narrows the mount paths pid matched to the ones it really uses. What
// cannot be judged stays listed with a reason that refuses every signal: a
// blocker whose evidence is missing is reported, not signalled.
func (n mountNamespaces) confirm(pid int, matches []string) (confirmed []string, signalBlock string) {
	namespace, err := n.readLink(process.PIDPath(pid, process.ProcFileMountNamespace))
	if err != nil || n.own == "" {
		return matches, reasonMountNamespaceUnverified
	}
	if namespace == n.own {
		return matches, ""
	}
	data, err := n.readFile(process.PIDPath(pid, procFileMountInfo))
	if err != nil || len(n.table) == 0 {
		return matches, reasonMountNamespaceUnverified
	}
	table := mounts.ParseMountInfo(data)
	for _, mountPath := range matches {
		ours, oursOK := deviceAt(n.table, mountPath)
		theirs, theirsOK := deviceAt(table, mountPath)
		if oursOK && theirsOK && ours == theirs {
			confirmed = append(confirmed, mountPath)
		}
	}
	return confirmed, ""
}

// deviceAt returns the filesystem that answers for path: the deepest mount over
// it, and among mounts stacked on one point the last one listed, the visible one.
func deviceAt(table []mounts.MountInfo, path string) (string, bool) {
	device, longest := "", 0
	for _, entry := range table {
		if mounts.PathUnder(path, entry.MountPoint) && len(entry.MountPoint) >= longest {
			device, longest = entry.DeviceID, len(entry.MountPoint)
		}
	}
	return device, device != ""
}
