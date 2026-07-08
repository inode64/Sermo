package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/mountctl"
	"sermo/internal/notify"
	"sermo/internal/process"
	"sermo/internal/state"
	"sermo/internal/web"
)

// mountUsageTTL bounds the dashboard's process-usage scan for mount units. The
// scan walks /proc/cwd, /proc/root and /proc/fd for every process, so dashboard
// refreshes share one sample for a short window while still updating quickly
// after users leave a mount.
const (
	mountBlockersNotifierName = "mount-blockers"
	mountUsageTTL             = 15 * time.Second
)

// MountAlertDelivery reports which mount-blocking users were targeted by a
// console alert.
type MountAlertDelivery struct {
	Users     []string
	Delivered int
}

// MountUserAlerter sends an operator-generated alert to users blocking a mount.
type MountUserAlerter interface {
	AlertMountUsers(ctx context.Context, spec mountctl.Spec, blockers []process.Process) (MountAlertDelivery, error)
}

type ttyMountUserAlerter struct{}

func (ttyMountUserAlerter) AlertMountUsers(ctx context.Context, spec mountctl.Spec, blockers []process.Process) (MountAlertDelivery, error) {
	users := uniqueBlockerUsers(blockers)
	if len(users) == 0 {
		return MountAlertDelivery{}, nil
	}
	userValues := make([]any, 0, len(users))
	for _, user := range users {
		userValues = append(userValues, user)
	}
	registry, warnings := notify.Build(map[string]any{
		mountBlockersNotifierName: map[string]any{
			notify.KeyType:  notify.TypeTTY,
			notify.KeyUsers: userValues,
		},
	}, notify.WithoutTemplates())
	if len(warnings) > 0 {
		return MountAlertDelivery{Users: users}, errors.New(strings.Join(warnings, "; "))
	}
	notifier := registry[mountBlockersNotifierName]
	if notifier == nil {
		return MountAlertDelivery{Users: users}, errors.New("tty notifier unavailable")
	}
	msg := notify.Message{
		Subject: "Sermo mount unit is blocked",
		Body: fmt.Sprintf(
			"A Sermo operator requested unmount of %s (%s), but one of your processes is using that path. Please leave the directory or close open files before retry.",
			spec.Name,
			spec.Path,
		),
	}
	if err := notifier.Send(ctx, msg); err != nil {
		return MountAlertDelivery{Users: users}, err
	}
	return MountAlertDelivery{Users: users, Delivered: len(users)}, nil
}

func (b *WebBackend) mountController() mountctl.Controller {
	ctrl := mountctl.Controller{
		Runner:         b.execRunner,
		DiscoverUsers:  b.mountUsers,
		Signaler:       b.mountSignaler,
		CommandTimeout: b.mountTimeout(),
		Mounts:         b.mountSampler,
	}
	if b.userLookup != nil {
		ctrl.ResolveUser = b.userLookup.ResolveUser
		ctrl.UserLookup = b.userLookup
	}
	if b.cfg != nil {
		ctrl.Runtime = b.cfg.Global.RuntimeDir()
	}
	return ctrl
}

func (b *WebBackend) mountTimeout() time.Duration {
	timeout := b.defaultTimeout
	if timeout <= 0 {
		timeout = b.operationTimeout
	}
	if timeout <= 0 {
		timeout = mountctl.DefaultCommandTimeout
	}
	return timeout
}

func (b *WebBackend) mountContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, b.mountTimeout())
}

func (b *WebBackend) mountSpec(name string) (mountctl.Spec, bool, string) {
	if b.cfg == nil {
		return mountctl.Spec{}, false, "no configuration loaded"
	}
	resolved, errs := b.cfg.ResolveStorage(name)
	if len(errs) > 0 {
		return mountctl.Spec{}, false, errs[0]
	}
	if _, ok := resolved.Tree[config.StorageKeyMount].(map[string]any); !ok {
		return mountctl.Spec{}, false, "storage watch " + name + " has no mount block"
	}
	return mountctl.SpecFromStorageTree(name, resolved.Tree), true, ""
}

// Mounts returns configured fstab-backed mount units with live mount/refcount status.
func (b *WebBackend) Mounts(ctx context.Context) []web.Mount {
	if b.cfg == nil {
		return nil
	}
	ctrl := b.mountController()
	names := b.cfg.StorageMountNames()
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	type mountRow struct {
		name   string
		spec   mountctl.Spec
		status mountctl.Status
		err    error
	}
	rows := make([]mountRow, 0, len(names))
	specs := make([]mountctl.Spec, 0, len(names))
	mounted := map[string]bool{}
	for _, name := range names {
		resolved, errs := b.cfg.ResolveStorage(name)
		if len(errs) > 0 {
			continue
		}
		spec := mountctl.SpecFromStorageTree(name, resolved.Tree)
		status, err := ctrl.ReadStatus(spec)
		rows = append(rows, mountRow{name: name, spec: spec, status: status, err: err})
		if err != nil {
			continue
		}
		specs = append(specs, spec)
		if status.Mounted {
			mounted[spec.Path] = true
		}
	}
	usage, usageErrors := b.mountUsageCached(ctx, specs, mounted)
	out := make([]web.Mount, 0, len(rows))
	for _, row := range rows {
		if row.err != nil {
			umountReason := mountctl.UmountDisabledReason(row.spec.Path)
			out = append(out, web.Mount{
				Name:         row.name,
				DisplayName:  row.spec.DisplayName,
				Category:     row.spec.Category,
				Path:         row.spec.Path,
				State:        backendStatusError,
				Refcounted:   row.spec.Refcount,
				CanUmount:    umountReason == "",
				UmountReason: umountReason,
				Message:      row.err.Error(),
			})
			continue
		}
		umountReason := mountctl.UmountDisabledReason(row.spec.Path)
		out = append(out, web.Mount{
			Name:         row.status.Name,
			DisplayName:  row.spec.DisplayName,
			Category:     row.spec.Category,
			Path:         row.status.Path,
			Mounted:      row.status.Mounted,
			Refcount:     row.status.Refcount,
			State:        row.status.State,
			Refcounted:   row.spec.Refcount,
			CanUmount:    umountReason == "",
			UmountReason: umountReason,
			Blockers:     b.mountBlockers(row.spec, usage[row.spec.Path]),
			BlockerError: usageErrors[row.spec.Path],
		})
	}
	return out
}

func (b *WebBackend) mountUsageCached(ctx context.Context, specs []mountctl.Spec, mounted map[string]bool) (map[string][]process.Process, map[string]string) {
	paths := mountedMountPaths(specs, mounted)
	if len(paths) == 0 {
		return nil, nil
	}
	now := b.webNow()
	b.mountUsageMu.Lock()
	defer b.mountUsageMu.Unlock()
	if b.mountUsage != nil && now.Sub(b.mountUsageAt) < mountUsageTTL && mountUsageCovers(b.mountUsage, paths) {
		return b.mountUsage, b.mountUsageErrors
	}
	usage := map[string][]process.Process{}
	usageErrors := map[string]string{}
	opCtx, cancel := b.mountContext(ctx)
	defer cancel()
	if b.mountUsers == nil {
		byMount, err := mountctl.ProcessesByMount(opCtx, paths, b.userLookup)
		if err != nil {
			for _, path := range paths {
				usageErrors[path] = err.Error()
			}
		}
		usage = byMount
	} else {
		for _, path := range paths {
			if err := opCtx.Err(); err != nil {
				usageErrors[path] = err.Error()
				continue
			}
			blockers, err := b.mountUsers(path)
			if err != nil {
				usageErrors[path] = err.Error()
				continue
			}
			usage[path] = blockers
		}
	}
	usage = ensureMountUsagePaths(usage, paths)
	if ctx.Err() != nil {
		// A cancelled request marks every path with its cancellation error;
		// caching that would show a bogus blocker error to every viewer for
		// the full TTL. Prefer the previous complete cache when there is one.
		if b.mountUsage != nil && mountUsageCovers(b.mountUsage, paths) {
			return b.mountUsage, b.mountUsageErrors
		}
		return usage, usageErrors
	}
	b.mountUsage = usage
	b.mountUsageErrors = usageErrors
	b.mountUsageAt = now
	return usage, usageErrors
}

func ensureMountUsagePaths(usage map[string][]process.Process, paths []string) map[string][]process.Process {
	if usage == nil {
		usage = map[string][]process.Process{}
	}
	for _, path := range paths {
		if _, ok := usage[path]; !ok {
			usage[path] = nil
		}
	}
	return usage
}

func mountedMountPaths(specs []mountctl.Spec, mounted map[string]bool) []string {
	seen := map[string]struct{}{}
	paths := make([]string, 0, len(specs))
	for _, spec := range specs {
		if !mountctl.CanUmountPath(spec.Path) {
			continue
		}
		if !mounted[spec.Path] {
			continue
		}
		if _, ok := seen[spec.Path]; ok {
			continue
		}
		seen[spec.Path] = struct{}{}
		paths = append(paths, spec.Path)
	}
	sort.Strings(paths)
	return paths
}

func mountUsageCovers(usage map[string][]process.Process, paths []string) bool {
	for _, path := range paths {
		if _, ok := usage[path]; !ok {
			return false
		}
	}
	return true
}

func (b *WebBackend) invalidateMountUsage() {
	b.mountUsageMu.Lock()
	b.mountUsage = nil
	b.mountUsageErrors = nil
	b.mountUsageAt = time.Time{}
	b.mountUsageMu.Unlock()
}

// MountBlockers reports processes currently using a configured mount unit.
func (b *WebBackend) MountBlockers(ctx context.Context, name string) web.MountBlockersResult {
	spec, ok, msg := b.mountSpec(name)
	if !ok {
		return web.MountBlockersResult{OK: false, Name: name, Message: msg}
	}
	if reason := mountctl.UmountDisabledReason(spec.Path); reason != "" {
		return web.MountBlockersResult{
			OK:           true,
			Name:         spec.Name,
			Path:         spec.Path,
			Mounted:      true,
			CanUmount:    false,
			UmountReason: reason,
			Message:      reason,
		}
	}
	opCtx, cancel := b.mountContext(ctx)
	defer cancel()
	ctrl := b.mountController()
	status, err := ctrl.ReadStatus(spec)
	if err != nil {
		return web.MountBlockersResult{OK: false, Name: name, Path: spec.Path, Message: err.Error()}
	}
	if !status.Mounted {
		return web.MountBlockersResult{
			OK:        true,
			Name:      spec.Name,
			Path:      spec.Path,
			Mounted:   false,
			CanUmount: true,
			Message:   "not mounted",
		}
	}
	blockers, err := ctrl.Blockers(opCtx, spec)
	if err != nil {
		return web.MountBlockersResult{OK: false, Name: spec.Name, Path: spec.Path, Mounted: true, Message: err.Error()}
	}
	webBlockers := b.mountBlockers(spec, blockers)
	return web.MountBlockersResult{
		OK:        true,
		Name:      spec.Name,
		Path:      spec.Path,
		Mounted:   true,
		CanUmount: true,
		CanKill:   mountCanKill(webBlockers),
		CanAlert:  len(uniqueBlockerUsers(blockers)) > 0,
		Blockers:  webBlockers,
	}
}

// MountAction runs mount or umount for a configured mount unit.
func (b *WebBackend) MountAction(ctx context.Context, name, action string, opts web.MountActionOptions) web.MountActionResult {
	spec, ok, msg := b.mountSpec(name)
	if !ok {
		return web.MountActionResult{OK: false, Name: name, Action: action, Message: msg}
	}
	opCtx, cancel := b.mountContext(ctx)
	defer cancel()
	ctrl := b.mountController()
	var (
		res mountctl.Result
		err error
	)
	switch action {
	case mountctl.ActionMount:
		res, err = ctrl.Acquire(opCtx, spec)
	case mountctl.ActionUmount:
		if reason := mountctl.UmountDisabledReason(spec.Path); reason != "" {
			return web.MountActionResult{
				OK:      false,
				Name:    spec.Name,
				Path:    spec.Path,
				Action:  action,
				Status:  mountctl.ResultFailed,
				Message: reason,
				Mounted: true,
			}
		}
		if opts.KillBlockers && !spec.Umount.AllowSIGKILL {
			return web.MountActionResult{
				OK:      false,
				Name:    spec.Name,
				Path:    spec.Path,
				Action:  action,
				Status:  mountctl.ResultFailed,
				Message: "kill blockers is not allowed by this mount policy",
			}
		}
		runSpec := spec
		if !opts.KillBlockers {
			runSpec.Umount.AllowSIGKILL = false
		}
		res, err = ctrl.Release(opCtx, runSpec)
	default:
		return web.MountActionResult{OK: false, Name: spec.Name, Path: spec.Path, Action: action, Message: "unknown mount action " + action}
	}
	b.invalidateMountUsage()
	out := b.mountActionResult(spec, res, err)
	b.syncStorageMountMonitoring(spec.Name, action, out.OK)
	return out
}

func (b *WebBackend) syncStorageMountMonitoring(storage, action string, resultOK bool) {
	w, ok := b.watches[storage]
	if !ok || w.checkType != checks.CheckTypeStorage {
		return
	}
	change, err := SyncStorageMountMonitoring(
		b.store,
		storage,
		action,
		resultOK,
		w.monitorMode,
		w.disabled,
		state.SourceWebMountUmount,
		state.SourceWeb,
	)
	if err != nil {
		b.emitWatchMonitorEvent(storage, action, eventKindError, "", err.Error())
		return
	}
	if change.Changed {
		b.emitWatchMonitorEvent(storage, change.Action, eventKindAction, eventStatusOK, change.Message)
	}
}

// AlertMountUsers sends a native TTY warning to users currently blocking a mount.
func (b *WebBackend) AlertMountUsers(ctx context.Context, name string) web.MountAlertResult {
	spec, ok, msg := b.mountSpec(name)
	if !ok {
		return web.MountAlertResult{OK: false, Name: name, Message: msg}
	}
	if reason := mountctl.UmountDisabledReason(spec.Path); reason != "" {
		return web.MountAlertResult{OK: false, Name: spec.Name, Path: spec.Path, Message: reason}
	}
	opCtx, cancel := b.mountContext(ctx)
	defer cancel()
	ctrl := b.mountController()
	blockers, err := ctrl.Blockers(opCtx, spec)
	if err != nil {
		return web.MountAlertResult{OK: false, Name: spec.Name, Path: spec.Path, Message: err.Error()}
	}
	users := uniqueBlockerUsers(blockers)
	if len(users) == 0 {
		return web.MountAlertResult{OK: true, Name: spec.Name, Path: spec.Path, Message: "no logged-in blocking users found"}
	}
	alerter := b.mountAlerter
	if alerter == nil {
		alerter = ttyMountUserAlerter{}
	}
	delivery, err := alerter.AlertMountUsers(opCtx, spec, blockers)
	if err != nil {
		return web.MountAlertResult{
			OK:        false,
			Name:      spec.Name,
			Path:      spec.Path,
			Users:     delivery.Users,
			Delivered: delivery.Delivered,
			Message:   err.Error(),
		}
	}
	return web.MountAlertResult{
		OK:        true,
		Name:      spec.Name,
		Path:      spec.Path,
		Users:     delivery.Users,
		Delivered: delivery.Delivered,
		Message:   "alert sent",
	}
}

func (b *WebBackend) mountActionResult(spec mountctl.Spec, res mountctl.Result, err error) web.MountActionResult {
	if res.Name == "" {
		res.Name = spec.Name
	}
	if res.Path == "" {
		res.Path = spec.Path
	}
	out := web.MountActionResult{
		OK:        err == nil && res.Status == mountctl.ResultOK,
		Name:      res.Name,
		Path:      res.Path,
		Action:    res.Action,
		Status:    res.Status,
		Message:   res.Message,
		Mounted:   res.Mounted,
		Refcount:  res.Refcount,
		Lazy:      res.Lazy,
		Signalled: res.Signalled,
		Blockers:  b.mountBlockers(spec, res.Blockers),
	}
	if out.Message == "" && err != nil {
		out.Message = err.Error()
	}
	return out
}

func (b *WebBackend) mountBlockers(spec mountctl.Spec, blockers []process.Process) []web.MountBlocker {
	if len(blockers) == 0 {
		return nil
	}
	resolve := b.resolveUser
	out := make([]web.MountBlocker, 0, len(blockers))
	for _, p := range blockers {
		out = append(out, web.MountBlocker{
			PID:         p.PID,
			PPID:        p.PPID,
			User:        p.User,
			UID:         p.UID,
			Exe:         p.Exe,
			ExeResolved: p.ExeOK,
			Cmdline:     p.Cmdline,
			Killable:    mountctl.CanUmountPath(spec.Path) && spec.Umount.AllowSIGKILL && spec.KillOnlyIf.Killable(p, resolve),
		})
	}
	return out
}

func (b *WebBackend) resolveUser(name string) (uint32, bool) {
	if b.userLookup != nil {
		return b.userLookup.ResolveUser(name)
	}
	return process.DefaultUserLookup().ResolveUser(name)
}

func mountCanKill(blockers []web.MountBlocker) bool {
	for _, blocker := range blockers {
		if blocker.Killable {
			return true
		}
	}
	return false
}

func uniqueBlockerUsers(blockers []process.Process) []string {
	seen := map[string]struct{}{}
	for _, p := range blockers {
		user := strings.TrimSpace(p.User)
		if user == "" {
			continue
		}
		seen[user] = struct{}{}
	}
	users := make([]string, 0, len(seen))
	for user := range seen {
		users = append(users, user)
	}
	sort.Strings(users)
	return users
}
