//go:build linux

package execx

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	osuser "os/user"
	"strconv"
	"strings"
	"syscall"
)

const (
	userIDNumericBase = 10
	userIDBitSize     = 32
)

func prepareCommandUser(cmd *exec.Cmd, userName string) error {
	userName = strings.TrimSpace(userName)
	if userName == "" {
		return errors.New("execx: command user is empty")
	}
	u, err := lookupCommandUser(userName)
	if err != nil {
		return err
	}
	uid, err := parseUserID(u.Uid, "uid")
	if err != nil {
		return err
	}
	gid, err := parseUserID(u.Gid, "gid")
	if err != nil {
		return err
	}
	euid, egid := os.Geteuid(), os.Getegid()
	if euid >= 0 && egid >= 0 && uint64(euid) <= math.MaxUint32 && uint64(egid) <= math.MaxUint32 &&
		uid == uint32(euid) && gid == uint32(egid) {
		return nil
	}
	groups, err := commandUserGroups(u)
	if err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: groups},
	}
	cmd.Env = withUserIdentityEnv(cmd.Env, u)
	return nil
}

// Identity variables a login sets for its user.
const (
	envHomePrefix    = "HOME="
	envUserPrefix    = "USER="
	envLognamePrefix = "LOGNAME="
	identityEnvCount = 3
)

// withUserIdentityEnv points HOME, USER and LOGNAME at the command user, as
// runuser and su do. Inherited from the daemon they name root: HOME=/root is
// unreadable to the target user, so clients that read ~/.my.cnf, ~/.pgpass or
// ~/.psqlrc fail or pick up the wrong identity. A nil env stands for the
// inherited environment; a user without a home directory gets no HOME.
func withUserIdentityEnv(env []string, u *osuser.User) []string {
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env)+identityEnvCount)
	for _, entry := range env {
		if strings.HasPrefix(entry, envHomePrefix) || strings.HasPrefix(entry, envUserPrefix) || strings.HasPrefix(entry, envLognamePrefix) {
			continue
		}
		out = append(out, entry)
	}
	if u.HomeDir != "" {
		out = append(out, envHomePrefix+u.HomeDir)
	}
	return append(out, envUserPrefix+u.Username, envLognamePrefix+u.Username)
}

func lookupCommandUser(userName string) (*osuser.User, error) {
	var (
		u   *osuser.User
		err error
	)
	if numericUserID(userName) {
		u, err = osuser.LookupId(userName)
	} else {
		u, err = osuser.Lookup(userName)
	}
	if err != nil {
		return nil, fmt.Errorf("execx: resolve command user %q: %w", userName, err)
	}
	return u, nil
}

func numericUserID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseUserID(s, label string) (uint32, error) {
	n, err := strconv.ParseUint(s, userIDNumericBase, userIDBitSize)
	if err != nil {
		return 0, fmt.Errorf("execx: parse command user %s %q: %w", label, s, err)
	}
	return uint32(n), nil
}

func commandUserGroups(u *osuser.User) ([]uint32, error) {
	groupIDs, err := u.GroupIds()
	if err != nil {
		return nil, fmt.Errorf("execx: list groups for command user %q: %w", u.Username, err)
	}
	groups := make([]uint32, 0, len(groupIDs))
	for _, id := range groupIDs {
		gid, err := parseUserID(id, "supplementary gid")
		if err != nil {
			return nil, err
		}
		groups = append(groups, gid)
	}
	return groups, nil
}
