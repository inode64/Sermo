package checks

import (
	"context"
	"errors"
	"testing"
	"time"
)

func mountSampler(mounts ...Mount) MountSamplerFunc {
	return func() ([]Mount, error) { return mounts, nil }
}

func TestAutofsDefaultRequiresOne(t *testing.T) {
	// One autofs mountpoint present -> healthy.
	c := autofsCheck{base: base{name: "am"}, sampler: mountSampler(
		Mount{MountPoint: "/", FSType: "ext4"},
		Mount{MountPoint: "/net", FSType: "autofs"},
	)}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("expected OK with one autofs mount: %q", res.Message)
	}
	if res.Data["count"] != 1 {
		t.Fatalf("count = %v, want 1", res.Data["count"])
	}

	// None present -> unhealthy.
	c.sampler = mountSampler(Mount{MountPoint: "/", FSType: "ext4"})
	if res := c.Run(context.Background()); res.OK {
		t.Fatal("expected not-OK with no autofs mounts")
	}
}

func TestAutofsPath(t *testing.T) {
	c := autofsCheck{base: base{name: "am"}, path: "/home", sampler: mountSampler(
		Mount{MountPoint: "/net", FSType: "autofs"},
		Mount{MountPoint: "/home", FSType: "autofs"},
	)}
	if res := c.Run(context.Background()); !res.OK {
		t.Fatalf("expected OK when /home is an autofs mount: %q", res.Message)
	}

	// The configured path is not an autofs mountpoint.
	c.path = "/misc"
	if res := c.Run(context.Background()); res.OK {
		t.Fatal("expected not-OK when /misc is absent")
	}
}

func TestAutofsCountPredicate(t *testing.T) {
	c := autofsCheck{base: base{name: "am"}, op: ">=", value: 2, sampler: mountSampler(
		Mount{MountPoint: "/net", FSType: "autofs"},
		Mount{MountPoint: "/home", FSType: "autofs"},
	)}
	if res := c.Run(context.Background()); !res.OK {
		t.Fatalf("expected OK with 2 >= 2: %q", res.Message)
	}

	// Only one mount -> predicate 1 >= 2 fails.
	c.sampler = mountSampler(Mount{MountPoint: "/net", FSType: "autofs"})
	if res := c.Run(context.Background()); res.OK {
		t.Fatal("expected not-OK with 1 >= 2")
	}
}

func TestAutofsSamplerError(t *testing.T) {
	c := autofsCheck{base: base{name: "am"}, sampler: func() ([]Mount, error) {
		return nil, errors.New("boom")
	}}
	if res := c.Run(context.Background()); res.OK {
		t.Fatal("a sampler error must fail the check")
	}
}

func TestBuildAutofsCheck(t *testing.T) {
	// Default form builds and runs against the injected sampler.
	built, warns := Build(map[string]any{
		"am": map[string]any{"type": "autofs"},
	}, Deps{DefaultTimeout: time.Second, MountSampler: mountSampler(
		Mount{MountPoint: "/net", FSType: "autofs"},
	)})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("autofs check should build: warns=%v", warns)
	}
	if res := built[0].Check.Run(context.Background()); !res.OK {
		t.Fatalf("built autofs check should pass: %q", res.Message)
	}

	// path + count together is rejected.
	if _, warns := Build(map[string]any{
		"am": map[string]any{"type": "autofs", "path": "/net", "count": map[string]any{"op": ">=", "value": 1}},
	}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
		t.Fatal("path + count must be rejected")
	}

	// An invalid count op is rejected.
	if _, warns := Build(map[string]any{
		"am": map[string]any{"type": "autofs", "count": map[string]any{"op": "~~", "value": 1}},
	}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
		t.Fatal("invalid count op must be rejected")
	}
}
