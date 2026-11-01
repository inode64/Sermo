package mountctl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"sermo/internal/process"
)

const (
	testOwnNamespace     = "mnt:[4026531841]"
	testForeignNamespace = "mnt:[4026532999]"
	testMountDevice      = "8:17"
	testOtherDevice      = "0:93"
)

// mountInfoFixture is one mountinfo table: a root filesystem and, when device
// is set, a mount at mountPath. The optional shared field sits where the kernel
// prints it, between the options and the separator.
func mountInfoFixture(mountPath, device string) []byte {
	table := "25 1 8:2 / / rw,relatime shared:1 - ext4 /dev/sda2 rw\n"
	if device != "" {
		table += fmt.Sprintf("36 25 %s / %s rw,relatime shared:9 - ext4 /dev/sdb1 rw\n", device, mountPath)
	}
	return []byte(table)
}

// holdMountPath leaves this process with an open file below a fresh directory,
// so the /proc scan of this process matches that path whatever namespace the
// test says the process lives in.
func holdMountPath(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	held, err := os.Create(filepath.Join(dir, "held"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	return dir
}

func TestProcessesByMountRequiresEvidenceAcrossMountNamespaces(t *testing.T) {
	dir := holdMountPath(t)
	pid := os.Getpid()
	ownNS := process.SelfPath(process.ProcFileMountNamespace)
	pidNS := process.PIDPath(pid, process.ProcFileMountNamespace)
	ownTable := process.SelfPath(procFileMountInfo)
	pidTable := process.PIDPath(pid, procFileMountInfo)

	tests := []struct {
		name       string
		links      map[string]string
		files      map[string][]byte
		wantListed bool
		wantBlock  string
	}{
		{
			name:       "same namespace is read directly",
			links:      map[string]string{ownNS: testOwnNamespace, pidNS: testOwnNamespace},
			wantListed: true,
		},
		{
			name:  "another filesystem at the same path is no blocker",
			links: map[string]string{ownNS: testOwnNamespace, pidNS: testForeignNamespace},
			files: map[string][]byte{
				ownTable: mountInfoFixture(dir, testMountDevice),
				pidTable: mountInfoFixture(dir, testOtherDevice),
			},
		},
		{
			name:  "a path on the namespace's root filesystem is no blocker",
			links: map[string]string{ownNS: testOwnNamespace, pidNS: testForeignNamespace},
			files: map[string][]byte{
				ownTable: mountInfoFixture(dir, testMountDevice),
				pidTable: mountInfoFixture(dir, ""),
			},
		},
		{
			// A service's private namespace is a slave of the host: its copy of
			// the mount keeps umount busy, so it stays a blocker.
			name:  "the same filesystem in another namespace is a blocker",
			links: map[string]string{ownNS: testOwnNamespace, pidNS: testForeignNamespace},
			files: map[string][]byte{
				ownTable: mountInfoFixture(dir, testMountDevice),
				pidTable: mountInfoFixture(dir, testMountDevice),
			},
			wantListed: true,
		},
		{
			name:       "an unreadable own namespace reports but never signals",
			links:      map[string]string{pidNS: testOwnNamespace},
			wantListed: true,
			wantBlock:  reasonMountNamespaceUnverified,
		},
		{
			name:       "an unreadable process namespace reports but never signals",
			links:      map[string]string{ownNS: testOwnNamespace},
			wantListed: true,
			wantBlock:  reasonMountNamespaceUnverified,
		},
		{
			name:       "an unreadable foreign mount table reports but never signals",
			links:      map[string]string{ownNS: testOwnNamespace, pidNS: testForeignNamespace},
			files:      map[string][]byte{ownTable: mountInfoFixture(dir, testMountDevice)},
			wantListed: true,
			wantBlock:  reasonMountNamespaceUnverified,
		},
		{
			name:       "an unreadable own mount table reports but never signals",
			links:      map[string]string{ownNS: testOwnNamespace, pidNS: testForeignNamespace},
			files:      map[string][]byte{pidTable: mountInfoFixture(dir, testMountDevice)},
			wantListed: true,
			wantBlock:  reasonMountNamespaceUnverified,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			namespaces := newMountNamespaces(
				func(path string) (string, error) {
					if link, ok := tc.links[path]; ok {
						return link, nil
					}
					return "", errors.New("no such link")
				},
				func(path string) ([]byte, error) {
					if data, ok := tc.files[path]; ok {
						return data, nil
					}
					return nil, errors.New("no such file")
				},
			)
			byMount, err := processesByMount(context.Background(), []string{dir}, nil, namespaces)
			if err != nil {
				t.Fatalf("processesByMount: %v", err)
			}
			blockers := byMount[dir]
			if !tc.wantListed {
				if len(blockers) != 0 {
					t.Fatalf("blockers = %+v, want none", blockers)
				}
				return
			}
			if len(blockers) != 1 || blockers[0].PID != pid {
				t.Fatalf("blockers = %+v, want this process alone", blockers)
			}
			if blockers[0].SignalBlockReason != tc.wantBlock {
				t.Fatalf("signal block = %q, want %q", blockers[0].SignalBlockReason, tc.wantBlock)
			}
		})
	}
}

func TestReleaseNeverSignalsAnUnverifiedBlocker(t *testing.T) {
	mounted := true
	runner := &fakeRunner{mounted: &mounted, busy: true}
	c := testController(t, &mounted, runner)
	signaler := &fakeSignaler{}
	c.Signaler = signaler
	c.ResolveUser = func(string) (uint32, bool) { return 1000, true }
	c.DiscoverUsers = func(string) ([]process.Process, error) {
		return []process.Process{{
			PID: 123, StartTicks: 7, Exe: "/usr/bin/rsync", ExeOK: true, UID: 1000,
			SignalBlockReason: reasonMountNamespaceUnverified,
		}}, nil
	}
	spec := EphemeralSpec("/mnt/backup")
	spec.KillOnlyIf = process.NewKillSelector([]string{"backup"}, []string{"/usr/bin/rsync"})

	res, err := c.ReleaseWithOptions(context.Background(), spec, ReleaseOptions{KillBlockers: true})
	if err == nil {
		t.Fatal("Release with an unverified blocker succeeded")
	}
	if signaler.calls != 0 || len(res.Blockers) != 1 {
		t.Fatalf("Release = %+v with %d signals, want the blocker reported and never signalled", res, signaler.calls)
	}
}

func TestParseMountInfoReadsEscapedMountPoints(t *testing.T) {
	table := parseMountInfo([]byte("36 25 8:17 / /mnt/my\\040data rw - ext4 /dev/sdb1 rw\nshort line\n"))
	if len(table) != 1 || table[0].point != "/mnt/my data" || table[0].device != "8:17" {
		t.Fatalf("parseMountInfo = %+v", table)
	}
}

func TestDeviceAtPrefersDeepestThenTopmostMount(t *testing.T) {
	table := []mountEntry{
		{point: "/", device: "8:2"},
		{point: "/mnt", device: "8:3"},
		{point: "/mnt/data", device: "8:17"},
		{point: "/mnt/data", device: "0:93"},
	}
	for path, want := range map[string]string{"/mnt/data": "0:93", "/mnt/data/sub": "0:93", "/mnt/other": "8:3", "/srv": "8:2"} {
		if got, ok := deviceAt(table, path); !ok || got != want {
			t.Fatalf("deviceAt(%q) = %q, %t, want %q", path, got, ok, want)
		}
	}
	if got, ok := deviceAt(nil, "/srv"); ok {
		t.Fatalf("deviceAt(empty) = %q, want not found", got)
	}
}
