package app

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"sermo/internal/checks"
	"sermo/internal/operation"
	"sermo/internal/rules"
)

// A slot covers a supervisor executable across all automatic restart rules.
type supervisorRenewals struct {
	mu     sync.Mutex
	owners map[string]string
}

func (r *supervisorRenewals) claim(service string, paths map[string]string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, path := range paths {
		if owner := r.owners[path]; owner != "" && owner != service {
			return owner
		}
	}
	for _, path := range paths {
		r.owners[path] = service
	}
	return ""
}

func (r *supervisorRenewals) release(service string, paths map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, path := range paths {
		if r.owners[path] == service {
			delete(r.owners, path)
		}
	}
}

type serviceRenewal struct {
	group  *supervisorRenewals
	probes map[string]checks.Built

	// A cascade can operate this service during its own observation. Only a
	// check started after the operation may release the slot.
	mu       sync.Mutex
	pending  map[string]string
	finished time.Time
}

func (s *serviceRenewal) observe(service string, cache map[string]checks.Result, ran map[string]bool, started time.Time) {
	if s == nil || !s.mu.TryLock() {
		return
	}
	defer s.mu.Unlock()
	if len(s.pending) == 0 || started.Before(s.finished) {
		return
	}
	for name := range s.pending {
		result := cache[name]
		if !result.OK || result.Skipped || result.Unavailable || (ran != nil && !ran[name]) {
			return
		}
	}
	s.group.release(service, s.pending)
	s.pending = nil
}

// begin uses the rule cycle's fresh samples. A cascade target has no access
// to that worker's mutable cache and samples its own required probes instead.
func (s *serviceRenewal) begin(ctx context.Context, service string, cache map[string]checks.Result, ran map[string]bool) (map[string]string, string) {
	if s == nil {
		return nil, ""
	}
	var paths map[string]string
	for name, probe := range s.probes {
		result, ok := cache[name]
		if !ok || ran != nil && !ran[name] {
			result = checks.Run(ctx, []checks.Built{probe}, 1)[0]
		}
		if result.Unavailable || result.Skipped {
			return nil, "restart waits for a verified supervised process observation: " + name
		}
		if path, _ := result.Data[checks.DataKeyReplacedSupervisor].(string); path != "" {
			if paths == nil {
				paths = make(map[string]string)
			}
			paths[name] = path
		}
	}
	if len(paths) == 0 {
		return nil, ""
	}
	if !s.mu.TryLock() {
		return nil, "supervisor renewal in progress on " + service
	}
	if owner := s.group.claim(service, paths); owner != "" {
		s.mu.Unlock()
		return nil, "supervisor renewal waits for a verified worker and supervisor on " + owner
	}
	return paths, ""
}

func (s *serviceRenewal) finish(service string, paths map[string]string, ran bool) {
	if len(paths) == 0 {
		return
	}
	defer s.mu.Unlock()
	if ran {
		s.finished = time.Now()
		if s.pending == nil {
			s.pending = paths
		} else {
			maps.Copy(s.pending, paths)
		}
		return
	}
	// A refused retry must keep any reservation from an earlier attempt.
	held := slices.Collect(maps.Values(s.pending))
	maps.DeleteFunc(paths, func(_ string, path string) bool { return slices.Contains(held, path) })
	s.group.release(service, paths)
}

// Reload runs after the previous generation has drained.
func (s *serviceRenewal) restore(service string, saved map[string]string) {
	if s == nil || len(saved) == 0 {
		return
	}
	pending := maps.Clone(saved)
	maps.DeleteFunc(pending, func(name, _ string) bool { _, ok := s.probes[name]; return !ok })
	s.group.claim(service, pending)
	s.pending = pending
}

func (w *Worker) operateCascadeTarget(ctx context.Context, action string) operation.Result {
	if w.renewal == nil || action != string(rules.ActionRestart) {
		return w.operateForRemediation(ctx, action)
	}
	paths, wait := w.renewal.begin(ctx, w.Service, nil, nil)
	if wait != "" {
		return operation.Result{Service: w.Service, Action: action, Status: operation.ResultBlocked, Message: wait}
	}
	result, recovered := operateRecovering(ctx, w.operateForRemediation, w.Service, action)
	w.renewal.finish(w.Service, paths, result.RecordsRemediation())
	if recovered != nil {
		panic(recovered)
	}
	return result
}
