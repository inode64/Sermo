package operation

import (
	"context"
	"errors"
	"fmt"

	"sermo/internal/servicemgr"
)

// containerCreator is a backend primitive, invoked only after the operation's
// locks, preflight and guards. Creation never starts the new container.
type containerCreator interface {
	Create(ctx context.Context) (string, error)
	StartCreated(ctx context.Context, id string) error
	VerifyCreated(ctx context.Context, id string) error
}

func (e Engine) verifyCreatedContainer(ctx context.Context, result *Result) bool {
	if result.CreatedContainer == "" {
		return true
	}
	creator, ok := e.Manager.(containerCreator)
	if !ok {
		result.Status, result.Message = ResultFailed, "Docker creation capability disappeared"
		return false
	}
	if err := creator.VerifyCreated(ctx, result.CreatedContainer); err != nil {
		return failPhase(ctx, result, "operation timed out verifying created container", "verify created container: ", err)
	}
	return true
}

func (e Engine) prepareDockerStart(ctx context.Context, p plan, result *Result) (plan, bool) {
	if e.Backend != string(servicemgr.BackendDocker) || (p.action != actionStart && p.action != actionRestart) || e.Manager == nil {
		return p, true
	}
	status, err := e.Manager.Status(ctx, e.Unit)
	missing := errors.Is(err, servicemgr.ErrNotFound)
	if err != nil && !missing {
		return p, failPhase(ctx, result, "operation timed out querying Docker", "query Docker state: ", err)
	}
	if !missing && status.Status == servicemgr.StatusActive {
		return p, true
	}
	if !missing && status.Status != servicemgr.StatusInactive {
		result.Status = ResultBlocked
		result.Message = fmt.Sprintf("Docker state %q is not a stopped container; wait for a stable state or use resume for a paused container", status.Status)
		return p, false
	}
	if p.action != actionStart {
		result.RequestedAction = p.action
		result.Action = actionStart
		p.action = actionStart
	}
	p.stop = false
	if missing {
		creator, ok := e.Manager.(containerCreator)
		if !ok {
			result.Status, result.Message = ResultFailed, "Docker container does not exist and has no creation definition"
			return p, false
		}
		result.CreatedContainer, err = creator.Create(ctx)
		if err != nil {
			return p, failPhase(ctx, result, "operation timed out creating Docker container", "create Docker container: ", err)
		}
	}
	return p, true
}
