package checks

import (
	"context"
	"time"

	"sermo/internal/process"
)

// ProcessIdentityFunc reports how a service's processes relate to its strict
// main selectors.
type ProcessIdentityFunc func() process.PrincipalIdentity

// ProcessIdentityHint is what the mismatch costs and the only remedy.
const ProcessIdentityHint = "start and restart are refused until a " + process.SectionProcesses +
	" selector names the running daemon"

// processIdentityCheck fails while an active service's processes match none of
// its strict main selectors. An operation refuses to treat such a service as
// its daemon, so a wrong selector (a launcher script instead of its
// interpreter, a unit that runs as another user) would otherwise stay
// invisible until the first restart that mattered.
type processIdentityCheck struct {
	base
	identity ProcessIdentityFunc
}

func (c processIdentityCheck) Run(_ context.Context) Result {
	start := time.Now()
	identity := c.identity()
	message := "service processes match the main selector"
	switch {
	case identity.Unverified():
		message = "no service process matches the main selector"
		if identity.Detail != "" {
			message += ": " + identity.Detail
		}
		message += "; " + ProcessIdentityHint
	case !identity.Required:
		message = "no strict main selector to verify"
	case !identity.Attributed:
		message = "no service process to verify"
	case identity.ReplacedExecutable && !identity.Trusted:
		message = "service process runs a replaced binary of the main selector"
	}
	res := c.result(!identity.Unverified(), message, start)
	res.Data = map[string]any{DataKeyType: CheckTypeProcessIdentity}
	return res
}

// buildProcessIdentityCheck takes no entry fields: it judges the service's own
// processes against its own selectors.
func buildProcessIdentityCheck(b base, deps Deps) (Check, string) {
	if deps.ProcessIdentity == nil {
		return nil, "process_identity check needs process discovery, unavailable here"
	}
	return processIdentityCheck{base: b, identity: deps.ProcessIdentity}, ""
}
