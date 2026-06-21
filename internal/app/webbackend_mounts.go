package app

import (
	"context"
	"sort"

	"sermo/internal/mountctl"
	"sermo/internal/web"
)

func (b *WebBackend) mountController() mountctl.Controller {
	timeout := b.defaultTimeout
	if timeout <= 0 {
		timeout = b.operationTimeout
	}
	if timeout <= 0 {
		timeout = mountctl.DefaultCommandTimeout
	}
	ctrl := mountctl.Controller{
		Runner:         b.execRunner,
		ResolveUser:    b.userLookup.ResolveUser,
		UserLookup:     b.userLookup,
		CommandTimeout: timeout,
		Mounts:         b.mountSampler,
	}
	if b.cfg != nil {
		ctrl.Runtime = b.cfg.Global.RuntimeDir()
	}
	return ctrl
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
