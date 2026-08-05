package process

import "testing"

func TestIdentityFilterMatchesTerminalScopedOwners(t *testing.T) {
	filter, err := NewIdentityFilter("", "deploy", "operators")
	if err != nil {
		t.Fatal(err)
	}
	id := Identity{PID: 10, UID: 1000, GID: 2000}
	match, err := filter.Match(id,
		func(string) (uint32, bool) { return 1000, true },
		func(string) (uint32, bool) { return 2000, true },
	)
	if err != nil || match != IdentityMatched {
		t.Fatalf("owner-only terminal filter = %v, %v; want match", match, err)
	}
}

func TestIdentityFilterUsesExactExecutableAndFailsClosed(t *testing.T) {
	filter, err := NewIdentityFilter("/opt/sermo-test/mysqldump", "backup", "")
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(string) (uint32, bool) { return 1001, true }

	matched, err := filter.Match(Identity{PID: 1, UID: 1001, Exe: "/opt/sermo-test/mysqldump", ExeOK: true}, resolve, nil)
	if err != nil || matched != IdentityMatched {
		t.Fatalf("exact executable = %v, %v; want match", matched, err)
	}
	wrong, err := filter.Match(Identity{PID: 1, UID: 1001, Exe: "/opt/sermo-test/other", ExeOK: true}, resolve, nil)
	if err != nil || wrong != IdentityNoMatch {
		t.Fatalf("different executable = %v, %v; want no match", wrong, err)
	}
	unknown, err := filter.Match(Identity{PID: 1, UID: 1001, ExeOK: false}, resolve, nil)
	if err != nil || unknown != IdentityUnknown {
		t.Fatalf("unreadable executable = %v, %v; want unknown", unknown, err)
	}
}

func TestIdentityFilterRejectsEmptyAndUnknownOwner(t *testing.T) {
	if _, err := NewIdentityFilter("", "", ""); err == nil {
		t.Fatal("empty filter must fail")
	}
	filter, err := NewIdentityFilter("", "missing", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := filter.Match(Identity{}, func(string) (uint32, bool) { return 0, false }, nil); err == nil {
		t.Fatal("unresolved configured user must fail closed")
	}
}
