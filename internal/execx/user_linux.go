//go:build linux

package execx

import (
	"fmt"
	"os"
	"os/exec"
	osuser "os/user"
	"strconv"
	"strings"
	"syscall"
)

func prepareCommandUser(cmd *exec.Cmd, userName string) error {
	userName = strings.TrimSpace(userName)
	if userName == "" {
		return fmt.Errorf("execx: command user is empty")
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
	if uid == uint32(os.Geteuid()) && gid == uint32(os.Getegid()) {
		return nil
	}
	groups, err := commandUserGroups(u)
	if err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: groups},
	}
	return nil
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
	n, err := strconv.ParseUint(s, 10, 32)
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
