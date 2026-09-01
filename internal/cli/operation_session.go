package cli

import (
	"context"
	"fmt"

	"sermo/internal/app"
	"sermo/internal/config"
	"sermo/internal/control"
	"sermo/internal/metrics"
	"sermo/internal/operation"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
)

// manualOperationRunner is the caller-neutral manual action seam. Production
// uses one operationSession; tests may still inject App.Operate.
type manualOperationRunner struct {
	operate     func(context.Context, options, *config.Config, config.Resolved, string, string) (operation.Result, error)
	activeAfter func(context.Context, options, *config.Config, config.Resolved, string, string, operation.Result, error) bool
}

// operationSession owns the backend detection, manager and prepared service
// runtimes for one CLI command. A cascade prepares each target at most once.
type operationSession struct {
	app        App
	opts       options
	cfg        *config.Config
	backend    servicemgr.Backend
	manager    servicemgr.Manager
	resolver   servicemgr.UnitResolver
	eventStore *state.Store
	prepared   map[string]*preparedOperation
}

type preparedOperation struct {
	target          control.Target
	runtime         app.ServiceRuntime
	recordOperation func(operation.Result)
}

func (a App) prepareManualOperationRunner(ctx context.Context, opts options, cfg *config.Config, resolved config.Resolved, service, action string, actionStore *state.Store) (manualOperationRunner, func(), error) {
	closeRunner := func() {}
	if a.Operate != nil {
		runner := manualOperationRunner{
			operate: a.Operate,
			activeAfter: func(context.Context, options, *config.Config, config.Resolved, string, string, operation.Result, error) bool {
				return false
			},
		}
		if action != actionReload {
			return runner, closeRunner, nil
		}
		session, err := a.newOperationSession(ctx, opts, cfg, nil)
		if err != nil {
			return manualOperationRunner{}, closeRunner, err
		}
		if err := session.reloadSupported(ctx, service, resolved); err != nil {
			return manualOperationRunner{}, closeRunner, err
		}
		return runner, closeRunner, nil
	}

	eventStore := actionStore
	if eventStore == nil {
		var err error
		eventStore, err = openStateStore(ctx, cfg)
		if err != nil {
			return manualOperationRunner{}, closeRunner, fmt.Errorf("operation event store unavailable: %w", err)
		}
		closeRunner = func() { _ = eventStore.Close() }
	}
	session, err := a.newOperationSession(ctx, opts, cfg, eventStore)
	if err != nil {
		closeRunner()
		return manualOperationRunner{}, func() {}, err
	}
	if action == actionReload {
		if err := session.reloadSupported(ctx, service, resolved); err != nil {
			closeRunner()
			return manualOperationRunner{}, func() {}, err
		}
	}
	return manualOperationRunner{operate: session.operate, activeAfter: session.activeAfterPostflightFailure}, closeRunner, nil
}

func (a App) newOperationSession(ctx context.Context, opts options, cfg *config.Config, eventStore *state.Store) (*operationSession, error) {
	prepareTimeout := engineDefaultTimeout(cfg)
	prepareCtx, cancel := context.WithTimeout(ctx, prepareTimeout)
	defer cancel()
	detection, err := a.Detector.Detect(prepareCtx, opts.backend)
	if err != nil {
		return nil, fmt.Errorf("backend detection failed: %w", err)
	}
	manager, err := a.NewManager(detection.Backend)
	if err != nil {
		return nil, fmt.Errorf("service manager unavailable: %w", err)
	}
	resolver := servicemgr.NewUnitResolver()
	resolver.Runner = a.Runner
	resolver.Manager = manager
	return &operationSession{
		app: a, opts: opts, cfg: cfg, backend: detection.Backend, manager: manager,
		resolver: resolver, eventStore: eventStore, prepared: map[string]*preparedOperation{},
	}, nil
}

func (s *operationSession) prepare(ctx context.Context, service string, resolved config.Resolved) (*preparedOperation, error) {
	if prepared := s.prepared[service]; prepared != nil {
		return prepared, nil
	}
	target, err := s.app.resolveControlTarget(ctx, s.opts, service, resolved.Tree, s.backend, s.manager, s.resolver)
	if err != nil {
		return nil, err
	}
	prepared := &preparedOperation{target: target}
	buildRuntime := s.app.buildServiceRuntime
	if buildRuntime == nil {
		buildRuntime = app.BuildServiceRuntime
	}
	prepared.runtime = buildRuntime(ctx, app.ServiceRuntimeConfig{
		Service: service,
		Unit:    target.Unit,
		Tree:    resolved.Tree,
		Deps: app.Deps{
			Backend:          target.Backend,
			Manager:          target.Manager,
			BackendPIDs:      target.BackendPIDs,
			Runtime:          s.cfg.Global.RuntimeDir(),
			DefaultTimeout:   engineDefaultTimeout(s.cfg),
			OperationTimeout: s.opts.timeout,
			Collector:        metrics.New(metrics.OSReader{}),
			ExecxRunner:      s.app.Runner,
			UserLookup:       app.EngineUserLookup(s.cfg, s.app.Runner),
		},
		LibraryBaseline: map[string]string{},
		LockReclaimed: func(service, reason string) {
			fmt.Fprintf(s.app.Stderr, "reclaimed stale operation lock for %s (%s)\n", service, reason)
		},
		RecordOperation: func(result operation.Result) {
			if prepared.recordOperation != nil {
				prepared.recordOperation(result)
			}
		},
	})
	s.prepared[service] = prepared
	return prepared, nil
}

func (s *operationSession) operate(ctx context.Context, opts options, _ *config.Config, resolved config.Resolved, service, action string) (operation.Result, error) {
	prepared, err := s.prepare(ctx, service, resolved)
	if err != nil {
		return operation.Result{}, err
	}
	var eventErr error
	prepared.recordOperation = func(result operation.Result) {
		if s.eventStore == nil {
			return
		}
		eventErr = recordManualActionEvent(ctx, s.eventStore, app.OperationEventRecord(result))
	}
	defer func() { prepared.recordOperation = nil }()
	result := runEngineAction(ctx, prepared.runtime.Engine, opts, action)
	if eventErr != nil {
		return result, fmt.Errorf("%s applied with result %q, but its audit event could not be recorded: %w",
			action, result.Status, eventErr)
	}
	if result.Message == "unknown action" && result.Status == operation.ResultFailed {
		return operation.Result{}, fmt.Errorf("unknown action %q", action)
	}
	return result, nil
}

func (s *operationSession) reloadSupported(ctx context.Context, service string, resolved config.Resolved) error {
	prepared, err := s.prepare(ctx, service, resolved)
	if err != nil {
		return fmt.Errorf("control target failed: %w", err)
	}
	canReload, err := app.ServiceReloadSupported(ctx, resolved.Tree, prepared.target.Manager, prepared.target.Unit)
	if err != nil {
		return fmt.Errorf("reload support unavailable: %w", err)
	}
	if !canReload {
		return fmt.Errorf("check reload support: %w", operation.UnsupportedReloadError(prepared.target.Unit))
	}
	return nil
}

func (s *operationSession) activeAfterPostflightFailure(ctx context.Context, _ options, _ *config.Config, resolved config.Resolved, service, action string, result operation.Result, opErr error) bool {
	return app.ServiceActiveAfterPostflightFailure(ctx, action, result, opErr, func(statusCtx context.Context) (servicemgr.Status, error) {
		prepared, err := s.prepare(statusCtx, service, resolved)
		if err != nil {
			return servicemgr.StatusUnknown, err
		}
		return prepared.runtime.CheckDeps.Status(statusCtx)
	})
}
