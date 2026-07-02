package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"sermo/internal/mountctl"
	"sermo/internal/notify"
	"sermo/internal/process"
	"sermo/internal/web"
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
		"mount-blockers": map[string]any{
			"type":  "tty",
			"users": userValues,
		},
	}, notify.WithoutTemplates())
	if len(warnings) > 0 {
		return MountAlertDelivery{Users: users}, errors.New(strings.Join(warnings, "; "))
	}
	notifier := registry["mount-blockers"]
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
	resolved, errs := b.cfg.ResolveMount(name)
	if len(errs) > 0 {
		return mountctl.Spec{}, false, errs[0]
	}
	return mountctl.SpecFromTree(name, resolved.Tree), true, ""
}

// Mounts returns configured fstab-backed mount units with live mount/refcount status.
func (b *WebBackend) Mounts(ctx context.Context) []web.Mount {
	_ = ctx
	if b.cfg == nil || len(b.cfg.MountNames) == 0 {
		return nil
	}
	ctrl := b.mountController()
	names := append([]string(nil), b.cfg.MountNames...)
	sort.Strings(names)
	out := make([]web.Mount, 0, len(names))
	for _, name := range names {
		resolved, errs := b.cfg.ResolveMount(name)
		if len(errs) > 0 {
			continue
		}
		spec := mountctl.SpecFromTree(name, resolved.Tree)
		status, err := ctrl.ReadStatus(spec)
		if err != nil {
			out = append(out, web.Mount{
				Name:        name,
				DisplayName: spec.DisplayName,
				Category:    spec.Category,
				Path:        spec.Path,
				State:       "error",
				Refcounted:  spec.Refcount,
				Message:     err.Error(),
			})
			continue
		}
		out = append(out, web.Mount{
			Name:        status.Name,
			DisplayName: spec.DisplayName,
			Category:    spec.Category,
			Path:        status.Path,
			Mounted:     status.Mounted,
			Refcount:    status.Refcount,
			Source:      status.Source,
			State:       status.State,
			Refcounted:  spec.Refcount,
		})
	}
	return out
}

// MountBlockers reports processes currently using a configured mount unit.
func (b *WebBackend) MountBlockers(ctx context.Context, name string) web.MountBlockersResult {
	spec, ok, msg := b.mountSpec(name)
	if !ok {
		return web.MountBlockersResult{OK: false, Name: name, Message: msg}
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
			OK:      true,
			Name:    spec.Name,
			Path:    spec.Path,
			Mounted: false,
			Message: "not mounted",
		}
	}
	blockers, err := ctrl.Blockers(opCtx, spec)
	if err != nil {
		return web.MountBlockersResult{OK: false, Name: spec.Name, Path: spec.Path, Mounted: true, Message: err.Error()}
	}
	webBlockers := b.mountBlockers(spec, blockers)
	return web.MountBlockersResult{
		OK:       true,
		Name:     spec.Name,
		Path:     spec.Path,
		Mounted:  true,
		CanKill:  mountCanKill(webBlockers),
		CanAlert: len(uniqueBlockerUsers(blockers)) > 0,
		Blockers: webBlockers,
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
	case "mount":
		res, err = ctrl.Acquire(opCtx, spec)
	case "umount":
		if opts.KillBlockers && !spec.Umount.AllowSIGKILL {
			return web.MountActionResult{
				OK:      false,
				Name:    spec.Name,
				Path:    spec.Path,
				Action:  action,
				Status:  "failed",
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
	return b.mountActionResult(spec, res, err)
}

// AlertMountUsers sends a native TTY warning to users currently blocking a mount.
func (b *WebBackend) AlertMountUsers(ctx context.Context, name string) web.MountAlertResult {
	spec, ok, msg := b.mountSpec(name)
	if !ok {
		return web.MountAlertResult{OK: false, Name: name, Message: msg}
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
		OK:        err == nil && res.Status == "ok",
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
			Killable:    spec.Umount.AllowSIGKILL && spec.KillOnlyIf.Killable(p, resolve),
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
