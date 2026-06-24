package process

import "testing"

// parsePasswdLine takes name + uid from a colon-separated passwd line; a
// 3-field line is the minimum valid form, and a blank name / too-few fields /
// non-numeric uid are rejected.
func TestParsePasswdLine(t *testing.T) {
	if name, uid, ok := parsePasswdLine("root:x:0:0:root:/root:/bin/bash"); !ok || name != "root" || uid != 0 {
		t.Errorf("full line = (%q,%d,%v), want (root,0,true)", name, uid, ok)
	}
	if name, uid, ok := parsePasswdLine("alice:x:1000"); !ok || name != "alice" || uid != 1000 {
		t.Errorf("3-field line = (%q,%d,%v), want (alice,1000,true)", name, uid, ok)
	}
	if _, _, ok := parsePasswdLine("a:b"); ok {
		t.Error("two fields: want not ok")
	}
	if _, _, ok := parsePasswdLine(":x:0"); ok {
		t.Error("empty name: want not ok")
	}
	if _, _, ok := parsePasswdLine("u:x:notnum"); ok {
		t.Error("non-numeric uid: want not ok")
	}
}

func TestParseGroupLine(t *testing.T) {
	if name, gid, ok := parseGroupLine("wheel:x:10:alice,bob"); !ok || name != "wheel" || gid != 10 {
		t.Errorf("full line = (%q,%d,%v), want (wheel,10,true)", name, gid, ok)
	}
	if name, gid, ok := parseGroupLine("grp:x:42"); !ok || name != "grp" || gid != 42 {
		t.Errorf("3-field line = (%q,%d,%v), want (grp,42,true)", name, gid, ok)
	}
	if _, _, ok := parseGroupLine("a:b"); ok {
		t.Error("two fields: want not ok")
	}
	if _, _, ok := parseGroupLine(":x:0"); ok {
		t.Error("empty name: want not ok")
	}
}
