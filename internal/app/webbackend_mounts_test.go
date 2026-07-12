package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/mountctl"
	"sermo/internal/process"
	"sermo/internal/state"
	"sermo/internal/web"
)

type webMountRunner struct {
	mounted   *bool
	busy      bool
	signalled *int
	calls     []string
}

func (r *webMountRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	r.calls = append(r.calls, strings.Join(append([]string{name}, args...), " "))
	switch name {
	case "mount":
		*r.mounted = true
		return execx.Result{}, nil
	case "umount":
		if r.busy && (r.signalled == nil || *r.signalled == 0) {
			return execx.Result{ExitCode: 32}, fmt.Errorf("run umount: exit code 32")
		}
		*r.mounted = false
		return execx.Result{}, nil
	default:
		return execx.Result{}, fmt.Errorf("unexpected command %s", name)
	}
}

type webMountSignaler struct {
	signalled *int
}

func (s webMountSignaler) Signal(int, syscall.Signal) error {
	(*s.signalled)++
	return nil
}

type fakeMountAlerter struct {
	called bool
}

func (a *fakeMountAlerter) AlertMountUsers(_ context.Context, _ mountctl.Spec, blockers []process.Process) (MountAlertDelivery, error) {
	a.called = true
	users := uniqueBlockerUsers(blockers)
	return MountAlertDelivery{Users: users, Delivered: len(users)}, nil
}

func TestWebBackendMounts(t *testing.T) {
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	cfg := &config.Config{
		Global: config.Global{Runtime: runtime, Raw: map[string]any{
			"paths": map[string]any{"runtime": runtime},
			"watches": map[string]any{
				"mount-backup": map[string]any{
					"name":  "mount-backup",
					"check": map[string]any{"type": "storage", "path": "/mnt/backup", "mounted": true},
					"mount": map[string]any{},
				},
			},
		}},
	}
	b, warns := NewWebBackend(t.Context(), cfg, Deps{
		MountSampler: func() ([]checks.Mount, error) {
			return []checks.Mount{{MountPoint: "/mnt/backup", Device: "/dev/sdb1"}}, nil
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	mounts := b.Mounts(context.Background())
	if len(mounts) != 1 {
		t.Fatalf("mounts = %+v, want one entry", mounts)
	}
	if mounts[0].Name != "mount-backup" || mounts[0].Path != "/mnt/backup" || !mounts[0].Mounted {
		t.Fatalf("mount = %+v", mounts[0])
	}
	if mounts[0].State != "active" || mounts[0].Refcount != 0 || !mounts[0].CanUmount {
		t.Fatalf("mount state/refcount = %+v", mounts[0])
	}
}

func TestWebBackendMountsIncludesUsageAndCaches(t *testing.T) {
	now := time.Unix(100, 0)
	calls := 0
	cfg := mountTestConfig(t)
	b, warns := NewWebBackend(t.Context(), cfg, Deps{
		Now: func() time.Time { return now },
		MountSampler: func() ([]checks.Mount, error) {
			return []checks.Mount{{MountPoint: "/mnt/backup", Device: "/dev/sdb1"}}, nil
		},
		MountDiscoverUsers: func(path string) ([]process.Process, error) {
			calls++
			if path != "/mnt/backup" {
				t.Fatalf("MountDiscoverUsers path = %q, want /mnt/backup", path)
			}
			return []process.Process{{
				PID: 123, User: "backup", UID: 1000, Exe: "/usr/bin/rsync", ExeOK: true, Source: "mount",
			}}, nil
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	mounts := b.Mounts(context.Background())
	if calls != 1 {
		t.Fatalf("usage discovery calls = %d, want 1", calls)
	}
	if len(mounts) != 1 || len(mounts[0].Blockers) != 1 {
		t.Fatalf("mounts = %+v, want one blocker", mounts)
	}
	if !mounts[0].Blockers[0].Killable || mounts[0].Blockers[0].User != "backup" {
		t.Fatalf("blocker = %+v, want policy-killable backup user", mounts[0].Blockers[0])
	}

	mounts = b.Mounts(context.Background())
	if calls != 1 || len(mounts) != 1 || len(mounts[0].Blockers) != 1 {
		t.Fatalf("cached mounts = %+v calls=%d, want cache hit with one blocker", mounts, calls)
	}
	now = now.Add(mountUsageTTL + time.Nanosecond)
	_ = b.Mounts(context.Background())
	if calls != 2 {
		t.Fatalf("usage discovery calls after ttl = %d, want 2", calls)
	}
}

func TestWebBackendMountUsageCacheIgnoresCancelledRequests(t *testing.T) {
	now := time.Unix(100, 0)
	calls := 0
	cfg := mountTestConfig(t)
	b, warns := NewWebBackend(t.Context(), cfg, Deps{
		Now: func() time.Time { return now },
		MountSampler: func() ([]checks.Mount, error) {
			return []checks.Mount{{MountPoint: "/mnt/backup", Device: "/dev/sdb1"}}, nil
		},
		MountDiscoverUsers: func(string) ([]process.Process, error) {
			calls++
			return []process.Process{{
				PID: 123, User: "backup", UID: 1000, Exe: "/usr/bin/rsync", ExeOK: true, Source: "mount",
			}}, nil
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	// A cancelled request cannot probe blockers; it reports the cancellation
	// for its own viewer but must not cache it for everyone else.
	mounts := b.Mounts(cancelled)
	if calls != 0 || len(mounts) != 1 || mounts[0].BlockerError == "" {
		t.Fatalf("cancelled mounts = %+v calls=%d, want uncached blocker error", mounts, calls)
	}

	mounts = b.Mounts(context.Background())
	if calls != 1 || len(mounts) != 1 || mounts[0].BlockerError != "" || len(mounts[0].Blockers) != 1 {
		t.Fatalf("mounts after cancelled request = %+v calls=%d, want fresh probe with one blocker", mounts, calls)
	}

	// After expiry a cancelled request is served the previous complete usage
	// instead of replacing it with cancellation errors.
	now = now.Add(mountUsageTTL + time.Nanosecond)
	mounts = b.Mounts(cancelled)
	if calls != 1 || len(mounts) != 1 || mounts[0].BlockerError != "" || len(mounts[0].Blockers) != 1 {
		t.Fatalf("cancelled mounts after expiry = %+v calls=%d, want previous usage", mounts, calls)
	}
}

func TestWebBackendMountsReportsUsageError(t *testing.T) {
	cfg := mountTestConfig(t)
	b, warns := NewWebBackend(t.Context(), cfg, Deps{
		MountSampler: func() ([]checks.Mount, error) {
			return []checks.Mount{{MountPoint: "/mnt/backup", Device: "/dev/sdb1"}}, nil
		},
		MountDiscoverUsers: func(string) ([]process.Process, error) {
			return nil, fmt.Errorf("proc scan failed")
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	mounts := b.Mounts(context.Background())
	if len(mounts) != 1 {
		t.Fatalf("mounts = %+v, want one entry", mounts)
	}
	if mounts[0].State != "active" || mounts[0].BlockerError != "proc scan failed" {
		t.Fatalf("mount = %+v, want active mount with blocker error", mounts[0])
	}
}

func TestWebBackendRootMountCannotUnmountOrScanBlockers(t *testing.T) {
	mounted := true
	signalled := 0
	scans := 0
	runner := &webMountRunner{mounted: &mounted}
	cfg := rootMountTestConfig(t)
	b, warns := NewWebBackend(t.Context(), cfg, Deps{
		ExecxRunner: runner,
		MountSampler: func() ([]checks.Mount, error) {
			return []checks.Mount{{MountPoint: "/", Device: "/dev/sda1"}}, nil
		},
		MountDiscoverUsers: func(string) ([]process.Process, error) {
			scans++
			return []process.Process{{
				PID: 1, User: "root", UID: 0, Exe: "/sbin/init", ExeOK: true, Source: "mount",
			}}, nil
		},
		MountSignaler: webMountSignaler{signalled: &signalled},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	reason := mountctl.UmountDisabledReason("/")

	mounts := b.Mounts(context.Background())
	if len(mounts) != 1 {
		t.Fatalf("mounts = %+v, want one root mount", mounts)
	}
	if mounts[0].CanUmount || mounts[0].UmountReason != reason || len(mounts[0].Blockers) != 0 {
		t.Fatalf("root mount = %+v, want unmount disabled without blockers", mounts[0])
	}
	if scans != 0 {
		t.Fatalf("root mount usage scans = %d, want none", scans)
	}

	blockers := b.MountBlockers(context.Background(), "mount-root")
	if !blockers.OK || blockers.CanUmount || blockers.CanKill || blockers.CanAlert || blockers.Message != reason || len(blockers.Blockers) != 0 {
		t.Fatalf("root blockers = %+v, want read-only disabled response", blockers)
	}
	res := b.MountAction(context.Background(), "mount-root", "umount", web.MountActionOptions{KillBlockers: true})
	if res.OK || res.Message != reason || !res.Mounted {
		t.Fatalf("root kill umount = %+v, want blocked mounted result", res)
	}
	alert := b.AlertMountUsers(context.Background(), "mount-root")
	if alert.OK || alert.Message != reason {
		t.Fatalf("root alert = %+v, want blocked alert", alert)
	}
	if len(runner.calls) != 0 || signalled != 0 || scans != 0 || !mounted {
		t.Fatalf("commands=%v signals=%d scans=%d mounted=%t, want no host action", runner.calls, signalled, scans, mounted)
	}
}

func TestWebBackendMountBlockersMarksPolicyKillable(t *testing.T) {
	cfg := mountTestConfig(t)
	b, warns := NewWebBackend(t.Context(), cfg, Deps{
		MountSampler: func() ([]checks.Mount, error) {
			return []checks.Mount{{MountPoint: "/mnt/backup", Device: "/dev/sdb1"}}, nil
		},
		MountDiscoverUsers: func(string) ([]process.Process, error) {
			return []process.Process{{
				PID: 123, User: "backup", UID: 1000, Exe: "/usr/bin/rsync", ExeOK: true, Source: "mount",
			}}, nil
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	res := b.MountBlockers(context.Background(), "mount-backup")
	if !res.OK || !res.CanUmount || !res.CanKill || !res.CanAlert || len(res.Blockers) != 1 {
		t.Fatalf("MountBlockers = %+v", res)
	}
	if !res.Blockers[0].Killable || res.Blockers[0].User != "backup" {
		t.Fatalf("blocker = %+v, want policy-killable backup user", res.Blockers[0])
	}
}

func TestWebBackendUnmountDoesNotSignalUnlessRequested(t *testing.T) {
	mounted := true
	signalled := 0
	runner := &webMountRunner{mounted: &mounted, busy: true, signalled: &signalled}
	cfg := mountTestConfig(t)
	b, warns := NewWebBackend(t.Context(), cfg, Deps{
		ExecxRunner: runner,
		MountSampler: func() ([]checks.Mount, error) {
			if mounted {
				return []checks.Mount{{MountPoint: "/mnt/backup", Device: "/dev/sdb1"}}, nil
			}
			return nil, nil
		},
		MountDiscoverUsers: func(string) ([]process.Process, error) {
			if signalled > 0 {
				return nil, nil
			}
			return []process.Process{{
				PID: 123, User: "backup", UID: 1000, Exe: "/usr/bin/rsync", ExeOK: true, Source: "mount",
			}}, nil
		},
		MountSignaler: webMountSignaler{signalled: &signalled},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	res := b.MountAction(context.Background(), "mount-backup", "umount", web.MountActionOptions{})
	if res.OK || signalled != 0 || !mounted {
		t.Fatalf("normal umount = %+v signalled=%d mounted=%t, want busy without signal", res, signalled, mounted)
	}

	res = b.MountAction(context.Background(), "mount-backup", "umount", web.MountActionOptions{KillBlockers: true})
	if !res.OK || signalled == 0 || mounted {
		t.Fatalf("kill umount = %+v signalled=%d mounted=%t", res, signalled, mounted)
	}
}

func TestWebBackendMountActionSyncsStorageWatchMonitoring(t *testing.T) {
	mounted := true
	store := newFakeStore()
	store.active[watchMonitorKey("mount-backup")] = true
	var events []Event
	cfg := mountTestConfig(t)
	b, warns := NewWebBackend(t.Context(), cfg, Deps{
		Monitor:     store,
		ExecxRunner: &webMountRunner{mounted: &mounted},
		MountSampler: func() ([]checks.Mount, error) {
			if mounted {
				return []checks.Mount{{MountPoint: "/mnt/backup", Device: "/dev/sdb1"}}, nil
			}
			return nil, nil
		},
		Emit: func(e Event) { events = append(events, e) },
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	res := b.MountAction(context.Background(), "mount-backup", "umount", web.MountActionOptions{})
	if !res.OK || mounted {
		t.Fatalf("umount = %+v mounted=%t", res, mounted)
	}
	if store.active[watchMonitorKey("mount-backup")] || store.source[watchMonitorKey("mount-backup")] != state.SourceWebMountUmount {
		t.Fatalf("watch after umount active=%v source=%q", store.active[watchMonitorKey("mount-backup")], store.source[watchMonitorKey("mount-backup")])
	}
	watches := b.Watches(context.Background())
	if len(watches) != 1 || watches[0].State != TargetStateDisabled || watches[0].Storage == nil || watches[0].Storage.Mounted {
		t.Fatalf("watch view after umount = %+v", watches)
	}

	mounted = true // already-mounted path avoids using the host /etc/fstab in the test.
	res = b.MountAction(context.Background(), "mount-backup", "mount", web.MountActionOptions{})
	if !res.OK {
		t.Fatalf("mount = %+v", res)
	}
	if !store.active[watchMonitorKey("mount-backup")] || store.source[watchMonitorKey("mount-backup")] != state.SourceWeb {
		t.Fatalf("watch after mount active=%v source=%q", store.active[watchMonitorKey("mount-backup")], store.source[watchMonitorKey("mount-backup")])
	}
	if len(events) != 2 || events[0].Action != "unmonitor" || events[1].Action != "monitor" {
		t.Fatalf("monitor events = %+v", events)
	}
}

func TestWebBackendMountActionPreservesManualUnmonitor(t *testing.T) {
	mounted := true
	store := newFakeStore()
	store.active[watchMonitorKey("mount-backup")] = false
	store.source[watchMonitorKey("mount-backup")] = state.SourceWeb
	cfg := mountTestConfig(t)
	b, warns := NewWebBackend(t.Context(), cfg, Deps{
		Monitor:     store,
		ExecxRunner: &webMountRunner{mounted: &mounted},
		MountSampler: func() ([]checks.Mount, error) {
			return []checks.Mount{{MountPoint: "/mnt/backup", Device: "/dev/sdb1"}}, nil
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	res := b.MountAction(context.Background(), "mount-backup", "mount", web.MountActionOptions{})
	if !res.OK {
		t.Fatalf("mount = %+v", res)
	}
	if store.active[watchMonitorKey("mount-backup")] || store.source[watchMonitorKey("mount-backup")] != state.SourceWeb {
		t.Fatalf("manual unmonitor was not preserved: active=%v source=%q", store.active[watchMonitorKey("mount-backup")], store.source[watchMonitorKey("mount-backup")])
	}
}

func TestWebBackendMountActionDryRunDoesNotRunCommands(t *testing.T) {
	mounted := true
	store := newFakeStore()
	store.active[watchMonitorKey("mount-backup")] = true
	runner := &webMountRunner{mounted: &mounted}
	cfg := dryRunMountTestConfig(t)
	b, warns := NewWebBackend(t.Context(), cfg, Deps{
		Monitor:     store,
		ExecxRunner: runner,
		MountSampler: func() ([]checks.Mount, error) {
			if mounted {
				return []checks.Mount{{MountPoint: "/mnt/backup", Device: "/dev/sdb1"}}, nil
			}
			return nil, nil
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	res := b.MountAction(context.Background(), "mount-backup", "umount", web.MountActionOptions{})
	if !res.OK || res.Message != "dry-run: would run umount" || !res.Mounted || !mounted {
		t.Fatalf("dry-run umount = %+v mounted=%t, want simulated mounted success", res, mounted)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("dry-run umount ran commands: %v", runner.calls)
	}
	if !store.active[watchMonitorKey("mount-backup")] {
		t.Fatal("dry-run umount must not disable storage watch monitoring")
	}

	mounted = false
	res = b.MountAction(context.Background(), "mount-backup", "mount", web.MountActionOptions{})
	if !res.OK || res.Message != "dry-run: would run mount" || res.Mounted || mounted {
		t.Fatalf("dry-run mount = %+v mounted=%t, want simulated unmounted success", res, mounted)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("dry-run mount ran commands: %v", runner.calls)
	}
}

func TestWebBackendAlertMountUsers(t *testing.T) {
	cfg := mountTestConfig(t)
	alerter := &fakeMountAlerter{}
	b, warns := NewWebBackend(t.Context(), cfg, Deps{
		MountSampler: func() ([]checks.Mount, error) {
			return []checks.Mount{{MountPoint: "/mnt/backup", Device: "/dev/sdb1"}}, nil
		},
		MountDiscoverUsers: func(string) ([]process.Process, error) {
			return []process.Process{{PID: 123, User: "backup", UID: 1000}}, nil
		},
		MountUserAlerter: alerter,
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	res := b.AlertMountUsers(context.Background(), "mount-backup")
	if !res.OK || !alerter.called || res.Delivered != 1 || len(res.Users) != 1 || res.Users[0] != "backup" {
		t.Fatalf("AlertMountUsers = %+v called=%v", res, alerter.called)
	}
}

func TestWebBackendAlertMountUsersDryRunDoesNotNotify(t *testing.T) {
	cfg := dryRunMountTestConfig(t)
	alerter := &fakeMountAlerter{}
	scans := 0
	b, warns := NewWebBackend(t.Context(), cfg, Deps{
		MountSampler: func() ([]checks.Mount, error) {
			return []checks.Mount{{MountPoint: "/mnt/backup", Device: "/dev/sdb1"}}, nil
		},
		MountDiscoverUsers: func(string) ([]process.Process, error) {
			scans++
			return []process.Process{{PID: 123, User: "backup", UID: 1000}}, nil
		},
		MountUserAlerter: alerter,
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	res := b.AlertMountUsers(context.Background(), "mount-backup")
	if !res.OK || res.Message != mountDryRunAlertMessage || alerter.called || scans != 0 {
		t.Fatalf("dry-run AlertMountUsers = %+v called=%v scans=%d", res, alerter.called, scans)
	}
}

func mountTestConfig(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	return &config.Config{
		Global: config.Global{Runtime: runtime, Raw: map[string]any{
			"paths": map[string]any{"runtime": runtime},
			"watches": map[string]any{
				"mount-backup": map[string]any{
					"name":  "mount-backup",
					"check": map[string]any{"type": "storage", "path": "/mnt/backup", "mounted": true},
					"mount": map[string]any{
						"refcount": false,
						"umount": map[string]any{
							"allow_sigkill": true,
							"term_timeout":  time.Nanosecond.String(),
							"kill_timeout":  time.Nanosecond.String(),
						},
						"stop_policy": map[string]any{
							"kill_only_if": map[string]any{
								"users":   []any{"1000"},
								"exe_any": []any{"/usr/bin/rsync"},
							},
						},
					},
				},
			},
		}},
	}
}

func dryRunMountTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := mountTestConfig(t)
	watches, _ := cfg.Global.Raw["watches"].(map[string]any)
	mountBackup, _ := watches["mount-backup"].(map[string]any)
	mountBackup["dry_run"] = true
	return cfg
}

func rootMountTestConfig(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	return &config.Config{
		Global: config.Global{Runtime: runtime, Raw: map[string]any{
			"paths": map[string]any{"runtime": runtime},
			"watches": map[string]any{
				"mount-root": map[string]any{
					"name":  "mount-root",
					"check": map[string]any{"type": "storage", "path": "/", "mounted": true},
					"mount": map[string]any{
						"refcount": false,
						"umount": map[string]any{
							"allow_sigkill": true,
						},
						"stop_policy": map[string]any{
							"kill_only_if": map[string]any{
								"users":   []any{"root"},
								"exe_any": []any{"/sbin/init"},
							},
						},
					},
				},
			},
		}},
	}
}
