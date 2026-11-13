package config

import "testing"

func TestValidateSMBSessions(t *testing.T) {
	for _, tt := range []struct {
		binary, path, count string
		want                string
	}{
		{binary: "/usr/bin/smbstatus", path: "/etc/samba/smb.conf", count: `{op: ">=", value: 0}`},
		{binary: "smbstatus", count: `{op: ">=", value: 0}`, want: "binary must be an absolute"},
		{binary: "/usr/bin/smbstatus", path: "relative.conf", count: `{op: ">=", value: 0}`, want: "path must be an absolute"},
		{binary: "/usr/bin/smbstatus", count: "bad", want: "count must be a mapping"},
	} {
		issues := validateService(t, "name: smb\nservice: smbd\nchecks:\n  sessions:\n    type: smb_sessions\n    binary: "+tt.binary+"\n    path: '"+tt.path+"'\n    count: "+tt.count+"\n")
		if tt.want == "" {
			mustNotHave(t, issues, "checks.sessions")
		} else {
			mustHave(t, issues, tt.want)
		}
	}
}
