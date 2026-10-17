package process

import (
	"crypto/sha256"
	"fmt"
)

// SignalIdentity binds a displayed process to its executable, ownership and
// generation. It is evidence to revalidate, never permission to send a signal.
func SignalIdentity(p Process) string {
	return fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "%d:%d:%d:%q:%t:%q:%d:%d:%q",
		p.PID, p.StartTicks, p.UID, p.Exe, p.ExeOK, p.ExePrev, p.ExeFile.Device, p.ExeFile.Inode, p.Cgroup)))
}
