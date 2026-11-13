package operation

import (
	"context"
	"fmt"
)

// SMBSessionTarget identifies the complete group a Samba worker serves. The
// verifier owns the source configuration and rejects any change to that group.
type SMBSessionTarget struct {
	Check    string
	Identity string
}

// CloseSMBSession uses the same locks, guards, deadline and audit as other
// manual closes. It never invokes the service backend or escalates to SIGKILL.
func (e Engine) CloseSMBSession(ctx context.Context, target SMBSessionTarget) Result {
	return e.run(ctx, plan{action: actionCloseSession, closeSMBSession: &target})
}

func (e Engine) closeSMBSession(ctx context.Context, target SMBSessionTarget, result *Result) {
	const prefix = "close SMB connection: "
	if e.SMBSessionVerifier == nil {
		failUnavailable(result, "SMB session close is unavailable for this service")
		return
	}
	proc, err := e.SMBSessionVerifier(ctx, target)
	if err != nil {
		failSession(result, prefix, err)
		return
	}
	if e.terminateSessionProcess(ctx, proc, result, prefix) {
		result.Message = fmt.Sprintf("close SMB connection ok; pid=%d", proc.PID)
	}
}
