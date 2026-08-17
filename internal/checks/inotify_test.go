package checks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestIsInotifyFDTarget(t *testing.T) {
	for _, tc := range []struct {
		target string
		want   bool
	}{
		{"anon_inode:inotify", true},
		{"anon_inode:[inotify]", true},
		{"anon_inode:[eventfd]", false},
		{"anon_inode:[eventpoll]", false},
		{"pipe:[12345]", false},
		{"/var/log/messages", false},
		{"", false},
	} {
		if got := isInotifyFDTarget(tc.target); got != tc.want {
			t.Errorf("isInotifyFDTarget(%q) = %v, want %v", tc.target, got, tc.want)
		}
	}
}

func TestCountInotifyWatchLines(t *testing.T) {
	// The real shape, from /proc/<pid>/fdinfo/<fd> of an inotify descriptor.
	body := "pos:\t0\nflags:\t02004000\nmnt_id:\t15\n" +
		"inotify wd:2 ino:38125e sdev:800001 mask:fce\n" +
		"inotify wd:1 ino:2 sdev:800001 mask:fce\n"
	got, err := countInotifyWatchLines(strings.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 2 {
		t.Fatalf("watches = %d, want 2", got)
	}
	if got, err := countInotifyWatchLines(strings.NewReader("pos:\t0\nflags:\t02004000\n")); err != nil || got != 0 {
		t.Fatalf("watch-less fdinfo = %d, %v; want 0, nil", got, err)
	}
}

func TestTopHoldersIsDeterministic(t *testing.T) {
	holders := topHolders(map[string]uint64{"dbus-daemon": 1005, "udisksd": 3, "polkitd": 3, "acpid": 1}, 3)
	want := []InotifyHolder{{"dbus-daemon", 1005}, {"polkitd", 3}, {"udisksd", 3}}
	if len(holders) != len(want) {
		t.Fatalf("holders = %+v, want %+v", holders, want)
	}
	for i := range want {
		if holders[i] != want[i] {
			t.Fatalf("holders = %+v, want %+v (ties break by name)", holders, want)
		}
	}
}

func TestInotifyCheckRun(t *testing.T) {
	// The measured bk1 incident: uid 0 at the instance ceiling while the watch
	// limit is almost untouched.
	exhausted := InotifySample{
		MaxInstances: 1024,
		MaxWatches:   524288,
		WatchesRead:  true,
		Users: []InotifyUserUsage{
			{UID: 0, Instances: 1024, Watches: 3803, Holders: []InotifyHolder{{"dbus-daemon", 1005}}},
			{UID: 1000, Instances: 2, Watches: 12},
		},
	}
	tests := []struct {
		name     string
		entry    map[string]any
		sample   InotifySample
		err      error
		wantOK   bool
		wantMsg  string
		wantData map[string]any
	}{
		{
			// The whole point of the headline field: one predicate must see the
			// binding limit whichever of the two it is.
			name:    "instance exhaustion fires the headline predicate",
			entry:   map[string]any{fieldUsedPct: map[string]any{CheckKeyOp: ">=", CheckKeyValue: "80%"}},
			sample:  exhausted,
			wantOK:  true,
			wantMsg: "inotify instances 1024/1024 uid 0, watches 3803/524288 uid 0; top dbus-daemon (1005)",
			wantData: map[string]any{
				DataKeyInstances: uint64(1024), DataKeyInstancesMax: uint64(1024),
				DataKeyInstancesUID: uint32(0), DataKeyDimension: inotifyDimInstances,
				DataKeyHolders: "dbus-daemon (1005)", DataKeyUsers: uint64(2),
			},
		},
		{
			// Each dimension keeps its own worst user: root leaks instances while a
			// desktop user holds every watch, and one "worst user" would mislabel one.
			name:  "the two dimensions can have different worst users",
			entry: map[string]any{fieldUsedPct: map[string]any{CheckKeyOp: ">=", CheckKeyValue: "80%"}},
			sample: InotifySample{
				MaxInstances: 1024, MaxWatches: 8192, WatchesRead: true,
				Users: []InotifyUserUsage{
					{UID: 0, Instances: 1000, Watches: 10},
					{UID: 1000, Instances: 4, Watches: 8000},
				},
			},
			wantOK:  true,
			wantMsg: "inotify instances 1000/1024 uid 0, watches 8000/8192 uid 1000",
		},
		{
			name:   "healthy host stays quiet",
			entry:  map[string]any{fieldUsedPct: map[string]any{CheckKeyOp: ">=", CheckKeyValue: "80%"}},
			sample: InotifySample{MaxInstances: 1024, MaxWatches: 524288, WatchesRead: true, Users: []InotifyUserUsage{{UID: 0, Instances: 36, Watches: 400}}},
			wantOK: false,
		},
		{
			// Predicates are ANDed, which is why the prefixed fields are for
			// operators who know which limit they care about.
			name: "both prefixed predicates must hold",
			entry: map[string]any{
				fieldInstancesUsedPct: map[string]any{CheckKeyOp: ">=", CheckKeyValue: 80},
				fieldWatchesUsedPct:   map[string]any{CheckKeyOp: ">=", CheckKeyValue: 80},
			},
			sample: exhausted,
			wantOK: false,
		},
		{
			name:     "the watch dimension can be the binding one",
			entry:    map[string]any{fieldUsedPct: map[string]any{CheckKeyOp: ">=", CheckKeyValue: "80%"}},
			sample:   InotifySample{MaxInstances: 1024, MaxWatches: 8192, WatchesRead: true, Users: []InotifyUserUsage{{UID: 1000, Instances: 5, Watches: 8000}}},
			wantOK:   true,
			wantData: map[string]any{DataKeyDimension: inotifyDimWatches},
		},
		{
			// An unknown limit omits its pct/free, so a predicate on them cannot
			// hold — the same contract the fds/pids/conntrack checks keep.
			name:   "unknown instance limit cannot fire a percentage",
			entry:  map[string]any{fieldInstancesUsedPct: map[string]any{CheckKeyOp: ">=", CheckKeyValue: 1}},
			sample: InotifySample{MaxWatches: 8192, WatchesRead: true, Users: []InotifyUserUsage{{UID: 0, Instances: 99}}},
			wantOK: false,
		},
		{
			name:   "absolute predicate still works without a limit",
			entry:  map[string]any{fieldInstances: map[string]any{CheckKeyOp: ">=", CheckKeyValue: 99}},
			sample: InotifySample{MaxWatches: 8192, WatchesRead: true, Users: []InotifyUserUsage{{UID: 0, Instances: 99}}},
			wantOK: true,
		},
		{
			name:    "sampler error is unavailable",
			entry:   map[string]any{fieldUsedPct: map[string]any{CheckKeyOp: ">=", CheckKeyValue: "80%"}},
			err:     errors.New("read /proc: permission denied"),
			wantMsg: "read /proc: permission denied",
		},
		{
			name:    "no kernel limits at all is unavailable, never a quiet ok",
			entry:   map[string]any{fieldUsedPct: map[string]any{CheckKeyOp: ">=", CheckKeyValue: "80%"}},
			sample:  InotifySample{Users: []InotifyUserUsage{{UID: 0, Instances: 5}}},
			wantMsg: "kernel exposes no inotify limits",
		},
		{
			// Without root the fd tables are unreadable, which is exactly the
			// blindness this check exists to remove: say so instead of ok.
			name:    "unreadable fd tables report a lower bound",
			entry:   map[string]any{fieldUsedPct: map[string]any{CheckKeyOp: ">=", CheckKeyValue: "80%"}},
			sample:  InotifySample{MaxInstances: 1024, WatchesRead: true, Unreadable: 400, Users: []InotifyUserUsage{{UID: 0, Instances: 2}}},
			wantMsg: "400 process fd table(s) unreadable",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check, errs := buildInotifyCheck(base{name: "watch-inotify", timeout: time.Second}, tc.entry, Deps{
				Samplers: Samplers{InotifySampler: func(context.Context, bool) (InotifySample, error) {
					return tc.sample, tc.err
				}},
			})
			if errs != "" {
				t.Fatalf("build failed: %s", errs)
			}
			res := check.Run(context.Background())
			if res.OK != tc.wantOK {
				t.Fatalf("OK = %v, want %v (%+v)", res.OK, tc.wantOK, res)
			}
			if tc.wantMsg != "" && !strings.Contains(res.Message, tc.wantMsg) {
				t.Fatalf("message = %q, want substring %q", res.Message, tc.wantMsg)
			}
			for key, want := range tc.wantData {
				if got := res.Data[key]; got != want {
					t.Fatalf("data[%s] = %v (%T), want %v (%T)", key, got, got, want, want)
				}
			}
		})
	}
}

// needsWatches keeps the expensive dimension out of a check that never asks
// about it, the way the hdparm check only runs the timings a predicate needs.
func TestInotifyWatchCountingIsPredicateGated(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry map[string]any
		want  bool
	}{
		{"instances only", map[string]any{fieldInstancesUsedPct: map[string]any{CheckKeyOp: ">=", CheckKeyValue: 80}}, false},
		{"headline needs both", map[string]any{fieldUsedPct: map[string]any{CheckKeyOp: ">=", CheckKeyValue: 80}}, true},
		{"watches asked for", map[string]any{fieldWatches: map[string]any{CheckKeyOp: ">=", CheckKeyValue: 1}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got bool
			check, errs := buildInotifyCheck(base{name: "w", timeout: time.Second}, tc.entry, Deps{
				Samplers: Samplers{InotifySampler: func(_ context.Context, countWatches bool) (InotifySample, error) {
					got = countWatches
					return InotifySample{MaxInstances: 1024, WatchesRead: countWatches}, nil
				}},
			})
			if errs != "" {
				t.Fatalf("build failed: %s", errs)
			}
			check.Run(context.Background())
			if got != tc.want {
				t.Fatalf("countWatches = %v, want %v", got, tc.want)
			}
		})
	}
}

// readInotifyUsage walks a real directory tree, so it is exercised against a
// fixture procfs rather than the host.
func TestReadInotifyUsageWalksAFixtureProcfs(t *testing.T) {
	root := t.TempDir()
	writeInotifyLimits(t, root, "1024", "524288")
	// uid 0 with two inotify fds (one carrying two watches) plus a pipe.
	writeFixturePID(t, root, 10, 0, "dbus-daemon", map[string]string{
		"3": "anon_inode:inotify",
		"4": "anon_inode:[inotify]",
		"5": "pipe:[999]",
	}, map[string]string{
		"3": "pos:\t0\ninotify wd:2 ino:1 sdev:1 mask:fce\ninotify wd:1 ino:2 sdev:1 mask:fce\n",
		"4": "pos:\t0\ninotify wd:1 ino:3 sdev:1 mask:fce\n",
	})
	// A second process of the same user, so per-user totals add up.
	writeFixturePID(t, root, 11, 0, "dbus-daemon", map[string]string{"3": "anon_inode:inotify"}, nil)
	// A different user.
	writeFixturePID(t, root, 12, 1000, "syncthing", map[string]string{"7": "anon_inode:inotify"}, nil)
	// A process holding nothing, and a non-numeric entry.
	writeFixturePID(t, root, 13, 0, "sleep", map[string]string{"1": "pipe:[7]"}, nil)
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}

	sample, err := readInotifyUsage(context.Background(), root, true)
	if err != nil {
		t.Fatalf("readInotifyUsage: %v", err)
	}
	if sample.MaxInstances != 1024 || sample.MaxWatches != 524288 {
		t.Fatalf("limits = %d/%d, want 1024/524288", sample.MaxInstances, sample.MaxWatches)
	}
	if len(sample.Users) != 2 {
		t.Fatalf("users = %+v, want uid 0 and uid 1000 only", sample.Users)
	}
	root0 := sample.Users[0]
	if root0.UID != 0 || root0.Instances != 3 || root0.Watches != 3 {
		t.Fatalf("uid 0 = %+v, want 3 instances and 3 watches", root0)
	}
	if len(root0.Holders) != 1 || root0.Holders[0] != (InotifyHolder{"dbus-daemon", 3}) {
		t.Fatalf("uid 0 holders = %+v, want dbus-daemon (3)", root0.Holders)
	}
	if sample.Users[1].UID != 1000 || sample.Users[1].Instances != 1 {
		t.Fatalf("uid 1000 = %+v, want 1 instance", sample.Users[1])
	}

	// Without the watch dimension no fdinfo is read at all.
	instancesOnly, err := readInotifyUsage(context.Background(), root, false)
	if err != nil {
		t.Fatalf("readInotifyUsage: %v", err)
	}
	if instancesOnly.WatchesRead || instancesOnly.Users[0].Watches != 0 {
		t.Fatalf("watches were counted without a predicate: %+v", instancesOnly.Users[0])
	}
}

func TestReadInotifyUsageHonoursCancellation(t *testing.T) {
	root := t.TempDir()
	writeInotifyLimits(t, root, "1024", "524288")
	for pid := 100; pid < 200; pid++ {
		writeFixturePID(t, root, pid, 0, "x", map[string]string{"3": "anon_inode:inotify"}, nil)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readInotifyUsage(ctx, root, false); err == nil {
		t.Fatal("a cancelled walk must fail rather than report a partial host")
	}
}

func TestParseStatusEffectiveUID(t *testing.T) {
	// Effective uid is the second field: the kernel charges inotify to it.
	uid, ok := parseStatusEffectiveUID("Name:\tsu\nUid:\t1000\t0\t0\t0\n")
	if !ok || uid != 0 {
		t.Fatalf("uid = %d, %v; want 0, true", uid, ok)
	}
	if _, ok := parseStatusEffectiveUID("Name:\tsh\n"); ok {
		t.Fatal("status without a Uid line must not parse")
	}
	if _, ok := parseStatusEffectiveUID("Uid:\t1000\n"); ok {
		t.Fatal("status without an effective uid must not parse")
	}
}

func writeInotifyLimits(t *testing.T, root, instances, watches string) {
	t.Helper()
	dir := filepath.Join(root, "sys", "fs", "inotify")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"max_user_instances": instances, "max_user_watches": watches} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFixturePID(t *testing.T, root string, pid int, uid uint32, command string, fds, fdinfo map[string]string) {
	t.Helper()
	pidPath := filepath.Join(root, strconv.Itoa(pid))
	fdDir := filepath.Join(pidPath, "fd")
	if err := os.MkdirAll(fdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Dangling symlinks: the fd targets are kernel labels, not real paths.
	for fd, target := range fds {
		if err := os.Symlink(target, filepath.Join(fdDir, fd)); err != nil {
			t.Fatal(err)
		}
	}
	if len(fdinfo) > 0 {
		infoDir := filepath.Join(pidPath, "fdinfo")
		if err := os.MkdirAll(infoDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for fd, body := range fdinfo {
			if err := os.WriteFile(filepath.Join(infoDir, fd), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	status := "Name:\t" + command + "\nUid:\t" + strconv.FormatUint(uint64(uid), 10) + "\t" + strconv.FormatUint(uint64(uid), 10) + "\t0\t0\n"
	if err := os.WriteFile(filepath.Join(pidPath, "status"), []byte(status), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pidPath, "comm"), []byte(command+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
