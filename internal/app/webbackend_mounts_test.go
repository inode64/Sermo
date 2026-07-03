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
		}},
		Mounts: map[string]*config.Document{
			"mount-backup": {
				Body: map[string]any{
					"name": "mount-backup",
					"path": "/mnt/backup",
				},
			},
		},
		MountNames: []string{"mount-backup"},
	}
	b, warns := NewWebBackend(cfg, Deps{
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
	if mounts[0].State != "active" || mounts[0].Refcount != 0 {
		t.Fatalf("mount state/refcount = %+v", mounts[0])
	}
}

func TestWebBackendMountsIncludesUsageAndCaches(t *testing.T) {
	now := time.Unix(100, 0)
	calls := 0
	cfg := mountTestConfig(t)
	b, warns := NewWebBackend(cfg, Deps{
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

func TestWebBackendMountsReportsUsageError(t *testing.T) {
	cfg := mountTestConfig(t)
	b, warns := NewWebBackend(cfg, Deps{
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

func TestWebBackendMountBlockersMarksPolicyKillable(t *testing.T) {
	cfg := mountTestConfig(t)
	b, warns := NewWebBackend(cfg, Deps{
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
	if !res.OK || !res.CanKill || !res.CanAlert || len(res.Blockers) != 1 {
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
	b, warns := NewWebBackend(cfg, Deps{
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

func TestWebBackendAlertMountUsers(t *testing.T) {
	cfg := mountTestConfig(t)
	alerter := &fakeMountAlerter{}
	b, warns := NewWebBackend(cfg, Deps{
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

func mountTestConfig(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	return &config.Config{
		Global: config.Global{Runtime: runtime, Raw: map[string]any{
			"paths": map[string]any{"runtime": runtime},
		}},
		Mounts: map[string]*config.Document{
			"mount-backup": {
				Body: map[string]any{
					"name":     "mount-backup",
					"path":     "/mnt/backup",
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
		MountNames: []string{"mount-backup"},
	}
}
