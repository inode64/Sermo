// Package mountctl controls fstab-backed mount points with Sermo runtime
// refcounts and conservative unmount escalation.
package mountctl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/execx"
	"sermo/internal/locks"
	"sermo/internal/process"
)

const (
	// DefaultTermTimeout is the wait after TERM when unmount escalation is enabled.
	DefaultTermTimeout = 12 * time.Second
	// DefaultKillTimeout is the wait after KILL when unmount escalation is enabled.
	DefaultKillTimeout = 5 * time.Second
	// DefaultCommandTimeout bounds individual mount/umount command invocations.
	DefaultCommandTimeout = 30 * time.Second
	// DefaultLockTTL bounds the per-mount operation lock.
	DefaultLockTTL = 5 * time.Minute
)

// UmountSpec controls unmount escalation after a normal umount fails.
type UmountSpec struct {
	TermTimeout  time.Duration
	KillTimeout  time.Duration
	AllowSIGKILL bool
	AllowLazy    bool
}

// Spec is one fstab-backed mount unit.
type Spec struct {
	Name        string
	DisplayName string
	Category    string
	Path        string
	Refcount    bool
	Umount      UmountSpec
	KillOnlyIf  process.KillSelector
}

// State is the persisted runtime refcount for one mount.
type State struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Refcount  int       `json:"refcount"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Result describes one mount or umount operation.
type Result struct {
	Name      string            `json:"name"`
	Path      string            `json:"path"`
	Action    string            `json:"action"`
	Status    string            `json:"status"`
	Message   string            `json:"message"`
	Mounted   bool              `json:"mounted"`
	Refcount  int               `json:"refcount"`
	Lazy      bool              `json:"lazy,omitempty"`
	Signalled []int             `json:"signalled,omitempty"`
	Blockers  []process.Process `json:"blockers,omitempty"`
}

// Status is the read-only view for status/list.
type Status struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Mounted  bool   `json:"mounted"`
	Refcount int    `json:"refcount"`
	Source   string `json:"source"`
	State    string `json:"state"`
}

// FstabEntry is one mount target declared in an fstab file.
type FstabEntry struct {
	Source  string
	Path    string
	FSType  string
	Options string
}

// Controller executes mount operations. All host access is injectable for tests.
type Controller struct {
	Runtime        string
	Runner         execx.Runner
	Mounts         checks.MountSamplerFunc
	InFstab        func(string) (bool, error)
	DiscoverUsers  func(string) ([]process.Process, error)
	Signaler       process.Signaler
	ResolveUser    process.UserResolver
	UserLookup     *process.UserLookup
	Sleep          func(time.Duration)
	Now            func() time.Time
	CommandTimeout time.Duration
	LockTTL        time.Duration
}

// SpecFromTree reads a resolved kind: mount body.
func SpecFromTree(name string, tree map[string]any) Spec {
	umount, _ := tree["umount"].(map[string]any)
	spec := Spec{
		Name:        name,
		DisplayName: cfgval.String(tree["display_name"]),
		Category:    cfgval.String(tree["category"]),
		Path:        filepath.Clean(cfgval.String(tree["path"])),
		Refcount:    true,
		Umount: UmountSpec{
			TermTimeout: DefaultTermTimeout,
			KillTimeout: DefaultKillTimeout,
		},
	}
	if ref, ok := tree["refcount"].(bool); ok {
		spec.Refcount = ref
	}
	if d := cfgval.Duration(umount["term_timeout"]); d > 0 {
		spec.Umount.TermTimeout = d
	}
	if d := cfgval.Duration(umount["kill_timeout"]); d > 0 {
		spec.Umount.KillTimeout = d
	}
	if b, ok := umount["allow_sigkill"].(bool); ok {
		spec.Umount.AllowSIGKILL = b
	}
	if b, ok := umount["allow_lazy"].(bool); ok {
		spec.Umount.AllowLazy = b
	}
	if sp, ok := tree["stop_policy"].(map[string]any); ok {
		if force, _ := sp["force_kill"].(bool); force {
			spec.Umount.AllowSIGKILL = true
		}
		if koi, ok := sp["kill_only_if"].(map[string]any); ok {
			spec.KillOnlyIf.Users = cfgval.StringList(koi["users"])
			spec.KillOnlyIf.ExeAny = cfgval.StringList(koi["exe_any"])
		}
	}
	return spec
}

// EphemeralSpec returns the safe default spec for a path not present in config.
func EphemeralSpec(path string) Spec {
	clean := filepath.Clean(path)
	return Spec{
		Name:     IDForPath(clean),
		Path:     clean,
		Refcount: true,
		Umount: UmountSpec{
			TermTimeout: DefaultTermTimeout,
			KillTimeout: DefaultKillTimeout,
		},
	}
}

// IDForPath derives a simple stable identifier from a mount path.
func IDForPath(path string) string {
	clean := strings.Trim(filepath.Clean(path), "/")
	if clean == "" || clean == "." {
		return "root"
	}
	var b strings.Builder
	for _, r := range clean {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Acquire increments the mount refcount and mounts the path on the 0->1 edge.
func (c Controller) Acquire(ctx context.Context, spec Spec) (Result, error) {
	return c.withLock(spec, func() (Result, error) {
		state, err := c.readState(spec)
		if err != nil {
			return Result{}, err
		}
		prev := state.Refcount
		if spec.Refcount {
			state.Refcount++
		}
		mounted, err := c.isMounted(spec.Path)
		if err != nil {
			return Result{}, err
		}
		mountedHere := false
		if !mounted {
			ok, err := c.inFstab(spec.Path)
			if err != nil {
				return Result{}, err
			}
			if !ok {
				return Result{}, fmt.Errorf("%s is not declared in /etc/fstab", spec.Path)
			}
			if err := c.run(ctx, "mount", spec.Path); err != nil {
				if spec.Refcount {
					state.Refcount = prev
					_ = c.writeState(spec, state)
				}
				return Result{}, err
			}
			mountedHere = true
		}
		if err := c.writeState(spec, state); err != nil {
			if mountedHere {
				// We mounted on this call but could not persist the new refcount.
				// Unmount so we never leave a mounted filesystem recorded as
				// refcount 0, which a later Release would then unmount out from
				// under a still-active user.
				_ = c.run(ctx, "umount", spec.Path)
			}
			return Result{}, err
		}
		mounted, _ = c.isMounted(spec.Path)
		msg := "mounted"
		if mounted && prev > 0 {
			msg = "acquired, already mounted"
		}
		return Result{Name: spec.Name, Path: spec.Path, Action: "mount", Status: "ok", Message: msg, Mounted: mounted, Refcount: state.Refcount}, nil
	})
}

// Release decrements the mount refcount and unmounts the path when it reaches 0.
func (c Controller) Release(ctx context.Context, spec Spec) (Result, error) {
	return c.withLock(spec, func() (Result, error) {
		state, err := c.readState(spec)
		if err != nil {
			return Result{}, err
		}
		if spec.Refcount && state.Refcount > 0 {
			state.Refcount--
		}
		if spec.Refcount && state.Refcount > 0 {
			if err := c.writeState(spec, state); err != nil {
				return Result{}, err
			}
			mounted, _ := c.isMounted(spec.Path)
			return Result{Name: spec.Name, Path: spec.Path, Action: "umount", Status: "ok", Message: "released, still in use", Mounted: mounted, Refcount: state.Refcount}, nil
		}

		unmount, err := c.unmount(ctx, spec)
		state.Refcount = 0
		if werr := c.writeState(spec, state); werr != nil && err == nil {
			err = werr
		}
		if err != nil {
			unmount.Status = "failed"
			unmount.Refcount = state.Refcount
			return unmount, err
		}
		unmount.Refcount = state.Refcount
		return unmount, nil
	})
}

// ReadStatus reports the current mount status and refcount.
func (c Controller) ReadStatus(spec Spec) (Status, error) {
	state, err := c.readState(spec)
	if err != nil {
		return Status{}, err
	}
	mounted, err := c.isMounted(spec.Path)
	if err != nil {
		return Status{}, err
	}
	st := "inactive"
	if mounted {
		st = "active"
	}
	return Status{Name: spec.Name, Path: spec.Path, Mounted: mounted, Refcount: state.Refcount, Source: "fstab", State: st}, nil
}

func (c Controller) withLock(spec Spec, fn func() (Result, error)) (Result, error) {
	ttl := c.LockTTL
	if ttl <= 0 {
		ttl = DefaultLockTTL
	}
	locker := locks.NewOperationLocker(filepath.Join(c.runtime(), "mounts", "ops"))
	handle, err := locker.Acquire(stateID(spec), ttl)
	if err != nil {
		return Result{}, err
	}
	defer handle.Release()
	return fn()
}

func (c Controller) unmount(ctx context.Context, spec Spec) (Result, error) {
	mounted, err := c.isMounted(spec.Path)
	if err != nil {
		return Result{}, err
	}
	if !mounted {
		return Result{Name: spec.Name, Path: spec.Path, Action: "umount", Status: "ok", Message: "already unmounted", Mounted: false}, nil
	}
	if err := c.run(ctx, "umount", spec.Path); err == nil {
		return Result{Name: spec.Name, Path: spec.Path, Action: "umount", Status: "ok", Message: "unmounted", Mounted: false}, nil
	}
	// Only treat the path as unmounted when the recheck succeeds and reports so;
	// a read failure must not be mistaken for "unmounted" and skip escalation.
	if ok, rerr := c.isMounted(spec.Path); rerr == nil && !ok {
		return Result{Name: spec.Name, Path: spec.Path, Action: "umount", Status: "ok", Message: "unmounted", Mounted: false}, nil
	}

	blockers, derr := c.discoverUsers(ctx, spec.Path)
	result := Result{Name: spec.Name, Path: spec.Path, Action: "umount", Status: "failed", Message: "mount is busy", Mounted: true, Blockers: blockers}
	if derr != nil {
		// A discovery failure must not masquerade as "no blockers": surface it so
		// the operator knows escalation could not be attempted, rather than
		// silently reporting a clean busy mount.
		timeout := c.CommandTimeout
		if timeout <= 0 {
			timeout = DefaultCommandTimeout
		}
		result.Message = fmt.Sprintf("mount is busy (could not enumerate blockers: %s)", execx.FormatContextOrError(derr, timeout))
	}
	if spec.Umount.AllowSIGKILL && len(blockers) > 0 {
		reaper := process.Reaper{
			Rediscover: func() []process.Process {
				procs, _ := c.discoverUsers(ctx, spec.Path)
				return procs
			},
			Signaler:    c.signaler(),
			ResolveUser: c.resolveUser(),
			Sleep:       c.Sleep,
		}
		reaped := reaper.Reap(ctx, blockers, process.KillPolicy{
			TermTimeout: spec.Umount.TermTimeout,
			KillTimeout: spec.Umount.KillTimeout,
			ForceKill:   true,
			KillOnlyIf:  spec.KillOnlyIf,
		})
		result.Signalled = reaped.Signalled
		result.Blockers = reaped.Remaining
		if err := c.run(ctx, "umount", spec.Path); err == nil {
			return Result{Name: spec.Name, Path: spec.Path, Action: "umount", Status: "ok", Message: "unmounted after signalling blockers", Mounted: false, Signalled: reaped.Signalled}, nil
		}
	}
	if spec.Umount.AllowLazy {
		if err := c.run(ctx, "umount", "-l", spec.Path); err == nil {
			result.Status = "ok"
			result.Message = "lazy unmounted"
			result.Mounted = false
			result.Lazy = true
			return result, nil
		}
	}
	return result, fmt.Errorf("%s", result.Message)
}

func (c Controller) readState(spec Spec) (State, error) {
	path := c.statePath(spec)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{Name: spec.Name, Path: spec.Path}, nil
		}
		return State{}, fmt.Errorf("read mount state %s: %w", path, err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("parse mount state %s: %w", path, err)
	}
	if state.Refcount < 0 {
		state.Refcount = 0
	}
	state.Name = spec.Name
	state.Path = spec.Path
	return state, nil
}

func (c Controller) writeState(spec Spec, state State) error {
	dir := filepath.Join(c.runtime(), "mounts", "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create mount state dir %s: %w", dir, err)
	}
	state.Name = spec.Name
	state.Path = spec.Path
	state.UpdatedAt = c.now()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	path := c.statePath(spec)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write mount state %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace mount state %s: %w", path, err)
	}
	return nil
}

func (c Controller) statePath(spec Spec) string {
	return filepath.Join(c.runtime(), "mounts", "state", stateID(spec)+".json")
}

func stateID(spec Spec) string {
	if spec.Name != "" {
		return IDForPath(spec.Name)
	}
	return IDForPath(spec.Path)
}

func (c Controller) run(ctx context.Context, name string, args ...string) error {
	runner := c.Runner
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	timeout := c.CommandTimeout
	if timeout <= 0 {
		timeout = DefaultCommandTimeout
	}
	res, err := execx.Run(ctx, runner, timeout, name, args...)
	if err != nil {
		msg := execx.OperatorFailure(err, res, timeout)
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return nil
}

func (c Controller) isMounted(path string) (bool, error) {
	mounts := c.Mounts
	if mounts == nil {
		mounts = checks.DefaultMounts
	}
	entries, err := mounts()
	if err != nil {
		return false, err
	}
	cleanPath := filepath.Clean(path)
	for _, mount := range entries {
		if filepath.Clean(mount.MountPoint) == cleanPath {
			return true, nil
		}
	}
	return false, nil
}

func (c Controller) inFstab(path string) (bool, error) {
	if c.InFstab != nil {
		return c.InFstab(path)
	}
	return PathInFstab(path)
}

func (c Controller) discoverUsers(ctx context.Context, path string) ([]process.Process, error) {
	if c.DiscoverUsers != nil {
		return c.DiscoverUsers(path)
	}
	return usersWithLookup(ctx, path, c.userLookup())
}

func (c Controller) signaler() process.Signaler {
	if c.Signaler != nil {
		return c.Signaler
	}
	return process.OSSignaler{}
}

func (c Controller) resolveUser() process.UserResolver {
	if c.ResolveUser != nil {
		return c.ResolveUser
	}
	return c.userLookup().ResolveUser
}

func (c Controller) userLookup() *process.UserLookup {
	if c.UserLookup != nil {
		return c.UserLookup
	}
	return process.DefaultUserLookup()
}

func (c Controller) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Controller) runtime() string {
	if c.Runtime != "" {
		return c.Runtime
	}
	return "/run/sermo"
}

// FstabEntries reads fstabPath and returns its mount entries. An empty path
// means /etc/fstab.
func FstabEntries(fstabPath string) ([]FstabEntry, error) {
	if fstabPath == "" {
		fstabPath = "/etc/fstab"
	}
	data, err := os.ReadFile(fstabPath)
	if err != nil {
		return nil, err
	}
	var entries []FstabEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		entry := FstabEntry{
			Source: unescapeFstab(fields[0]),
			Path:   filepath.Clean(unescapeFstab(fields[1])),
		}
		if len(fields) > 2 {
			entry.FSType = fields[2]
		}
		if len(fields) > 3 {
			entry.Options = fields[3]
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// PathInFstab reports whether path is a mountpoint in /etc/fstab.
func PathInFstab(path string) (bool, error) {
	entries, err := FstabEntries("/etc/fstab")
	if err != nil {
		return false, err
	}
	cleanPath := filepath.Clean(path)
	for _, entry := range entries {
		if filepath.Clean(entry.Path) == cleanPath {
			return true, nil
		}
	}
	return false, nil
}

// usersWithLookup is the context-aware scan: it walks /proc and stops early if
// ctx is cancelled, so a hung mount (e.g. a dead NFS server stalling readlink on
// a /proc fd) cannot block umount escalation past the operation deadline.
func usersWithLookup(ctx context.Context, mountPath string, lookup *process.UserLookup) ([]process.Process, error) {
	if lookup == nil {
		lookup = process.DefaultUserLookup()
	}
	reader := process.OSReader{LookupUserName: lookup.Username}
	pids, err := reader.PIDs()
	if err != nil {
		return nil, err
	}
	cleanMount := filepath.Clean(mountPath)
	var out []process.Process
	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if !pidUsesPath(ctx, pid, cleanMount) {
			continue
		}
		if id, ok := reader.Identity(pid); ok {
			out = append(out, process.Process{
				PID:     id.PID,
				PPID:    id.PPID,
				User:    id.User,
				UID:     id.UID,
				Exe:     id.Exe,
				ExeOK:   id.ExeOK,
				Cmdline: id.Cmdline,
				Role:    "mount-user",
				Source:  "mount",
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

func pidUsesPath(ctx context.Context, pid int, mountPath string) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	base := filepath.Join("/proc", fmt.Sprint(pid))
	for _, name := range []string{"cwd", "root"} {
		if err := ctx.Err(); err != nil {
			return false
		}
		if linkUnderMount(ctx, filepath.Join(base, name), mountPath) {
			return true
		}
	}
	fdDir := filepath.Join(base, "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return false
		}
		if linkUnderMount(ctx, filepath.Join(fdDir, entry.Name()), mountPath) {
			return true
		}
	}
	return false
}

func linkUnderMount(ctx context.Context, link, mountPath string) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	target, err := os.Readlink(link)
	if err != nil || !filepath.IsAbs(target) {
		return false
	}
	target = strings.TrimSuffix(target, " (deleted)")
	return pathUnderMount(filepath.Clean(target), mountPath)
}

func pathUnderMount(path, mountPath string) bool {
	if mountPath == "/" {
		return strings.HasPrefix(path, "/")
	}
	return path == mountPath || strings.HasPrefix(path, mountPath+"/")
}

func unescapeFstab(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}
