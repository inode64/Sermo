package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"sermo/internal/checks"
	"sermo/internal/logind"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
	"sermo/internal/web"
)

const (
	sshCatalogApp                  = "ssh"
	interactiveSessionCacheTTL     = 5 * time.Second
	sshSessionUnsupportedMessage   = "does not define a safely identifiable SSH session boundary"
	sshSessionSamplerFailedMessage = "load SSH sessions: "
)

type cachedSSHSessions struct {
	at     time.Time
	sample checks.SSHSessionSample
}

type sshSessionIdentity struct {
	pid        int
	startTicks uint64
	terminal   string
}

func sshSessionMetricKey(session web.SSHSession) string {
	return sessionMetricKey(web.SessionKindSSH, strconv.Itoa(session.PID), strconv.FormatUint(session.StartTicks, 10))
}

func sshSessionFilters(apps []string, selectors []process.Selector) []process.IdentityFilter {
	if !slices.Contains(apps, sshCatalogApp) {
		return nil
	}
	filters := make([]process.IdentityFilter, 0, len(selectors))
	for _, selector := range selectors {
		if selector.Exe == "" || selector.User == "" {
			continue
		}
		filter, err := process.NewIdentityFilter(selector.Exe, selector.User, "")
		if err == nil {
			filters = append(filters, filter)
		}
	}
	return uniqueSSHDFilters(filters)
}

func uniqueSSHDFilters(filters []process.IdentityFilter) []process.IdentityFilter {
	seen := make(map[string]bool, len(filters))
	out := make([]process.IdentityFilter, 0, len(filters))
	for _, filter := range filters {
		key := filter.Exe + "\x00" + filter.User
		if filter.Exe == "" || filter.User == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, filter)
	}
	slices.SortFunc(out, func(a, c process.IdentityFilter) int {
		if byExe := strings.Compare(a.Exe, c.Exe); byExe != 0 {
			return byExe
		}
		return strings.Compare(a.User, c.User)
	})
	return out
}

func (b *WebBackend) allSSHSessionFilters() []process.IdentityFilter {
	filters := make([]process.IdentityFilter, 0)
	for _, name := range b.order {
		if entry := b.entries[name]; entry != nil {
			filters = append(filters, entry.sshSessionFilters...)
		}
	}
	return uniqueSSHDFilters(filters)
}

func cloneSSHSessionSample(sample checks.SSHSessionSample) checks.SSHSessionSample {
	sample.SSH = slices.Clone(sample.SSH)
	sample.Issues = slices.Clone(sample.Issues)
	return sample
}

func (b *WebBackend) sshSessions(filters []process.IdentityFilter) (checks.SSHSessionSample, error) {
	filters = uniqueSSHDFilters(filters)
	if len(filters) == 0 {
		return checks.SSHSessionSample{}, errors.New(sshSessionUnsupportedMessage)
	}
	sampler := b.sshSessionSampler
	if sampler == nil {
		sampler = checks.NewSSHSessionSampler(nil, b.userLookup)
	}
	keyParts := make([]string, 0, len(filters))
	for _, filter := range filters {
		keyParts = append(keyParts, filter.Exe+"\x00"+filter.User)
	}
	key := strings.Join(keyParts, "\x00")
	now := b.webNow()
	b.sshSessionsMu.Lock()
	if cached, ok := b.sshSessionCache[key]; ok && now.Sub(cached.at) <= interactiveSessionCacheTTL {
		b.sshSessionsMu.Unlock()
		return cloneSSHSessionSample(cached.sample), nil
	}
	b.sshSessionsMu.Unlock()

	sample, err := sampler(checks.SSHSessionConfig{SSHDFilters: filters})
	if err != nil {
		return checks.SSHSessionSample{}, err
	}
	b.sshSessionsMu.Lock()
	if b.sshSessionCache == nil {
		b.sshSessionCache = map[string]cachedSSHSessions{}
	}
	b.sshSessionCache[key] = cachedSSHSessions{at: now, sample: cloneSSHSessionSample(sample)}
	b.sshSessionsMu.Unlock()
	return sample, nil
}

func freshSSHSessionVerifier(deps Deps, filters []process.IdentityFilter) func(context.Context, operation.SessionTarget) error {
	sampler := deps.SSHSessionVerifier
	if sampler == nil {
		reader := process.OSReader{ReadTTY: true}
		if deps.UserLookup != nil {
			reader.LookupUserName = deps.UserLookup.Username
			reader.LookupGroupName = deps.UserLookup.GroupName
		}
		sampler = checks.NewSSHSessionSampler(reader, deps.UserLookup)
	}
	return func(ctx context.Context, target operation.SessionTarget) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("SSH session verification context: %w", err)
		}
		sample, err := sampler(checks.SSHSessionConfig{SSHDFilters: filters})
		if err != nil {
			return fmt.Errorf("%s%w", sshSessionSamplerFailedMessage, err)
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("SSH session verification context: %w", err)
		}
		return sample.VerifySSHSession(checks.SSHSession{
			PID:        target.PID,
			StartTicks: target.StartTicks,
			Terminal:   target.Terminal,
		})
	}
}

func managedSSHSessionCloser(deps Deps, backend servicemgr.Backend) func(context.Context, operation.SessionTarget) error {
	if backend != servicemgr.BackendSystemd {
		return nil
	}
	if deps.ManagedSSHSessionCloser != nil {
		return deps.ManagedSSHSessionCloser
	}
	client := logind.NewClient()
	return func(ctx context.Context, target operation.SessionTarget) error {
		return client.CloseRemoteSSHSession(ctx, logind.Target{
			PID: target.PID, StartTicks: target.StartTicks, Terminal: target.Terminal,
		})
	}
}

func sshSessionsToWeb(sample checks.SSHSessionSample) []web.SSHSession {
	result := make([]web.SSHSession, 0, len(sample.SSH))
	for _, session := range sample.SSH {
		result = append(result, web.SSHSession{
			User:        session.User,
			Terminal:    session.Terminal,
			PID:         session.PID,
			StartTicks:  session.StartTicks,
			IdleSeconds: max(int64(session.Idle.Seconds()), 0),
			CanClose:    session.PID > 0 && session.StartTicks > 0,
		})
	}
	slices.SortFunc(result, func(a, c web.SSHSession) int {
		if byTerminal := strings.Compare(a.Terminal, c.Terminal); byTerminal != 0 {
			return byTerminal
		}
		return a.PID - c.PID
	})
	return result
}

func (b *WebBackend) appendSSHSessions(result *web.SessionInventory, seen map[sshSessionIdentity]struct{}, service string, entry *webEntry) {
	if len(entry.sshSessionFilters) == 0 {
		return
	}
	source := web.SessionSource{Kind: web.SessionKindSSH, Service: service, State: web.SessionSourceAvailable}
	sessions, err := b.sshSessions(entry.sshSessionFilters)
	if err != nil {
		source.State = web.SessionSourceUnavailable
		source.Message = err.Error()
		result.Sources = append(result.Sources, source)
		return
	}
	if len(sessions.Issues) > 0 {
		source.State = web.SessionSourcePartial
		source.Message = fmt.Sprintf("%d terminal(s) could not be attributed safely", len(sessions.Issues))
		source.Issues = make([]web.SessionIssue, 0, len(sessions.Issues))
		for _, issue := range sessions.Issues {
			canClose := entry.engine.ManagedSessionCloser != nil && issue.Remote && issue.PID > 0 && issue.StartTicks > 0
			source.Issues = append(source.Issues, web.SessionIssue{
				User: issue.User, Terminal: issue.Terminal, Message: issue.Message,
				PID: issue.PID, StartTicks: issue.StartTicks, CanClose: canClose, ManagedByLogind: canClose,
			})
		}
	}
	result.Sources = append(result.Sources, source)
	for _, session := range sshSessionsToWeb(sessions) {
		key := sshSessionIdentity{pid: session.PID, startTicks: session.StartTicks, terminal: session.Terminal}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		session.Service = service
		result.SSH = append(result.SSH, session)
	}
}

// CloseSSHSession uses the service operation engine. A verified SSH boundary is
// freshly checked before SIGTERM; an unavailable-ancestry row can select only
// the login1 closer, which independently verifies the exact managed session.
// Neither path trusts a displayed PID after it has been reused.
func (b *WebBackend) CloseSSHSession(ctx context.Context, name string, session web.SSHSession) web.ActionResult {
	e := b.entries[name]
	if e == nil {
		return b.operateError(name, "close SSH session", unknownServiceMessage+name)
	}
	if e.disabled {
		return b.operateError(name, "close SSH session", serviceSubjectPrefix+name+" is disabled in configuration")
	}
	if len(e.sshSessionFilters) == 0 {
		return b.operateError(name, "close SSH session", serviceSubjectPrefix+name+" "+sshSessionUnsupportedMessage)
	}
	r := e.engine.CloseSession(ctx, operation.SessionTarget{
		PID:             session.PID,
		StartTicks:      session.StartTicks,
		Terminal:        session.Terminal,
		ManagedByLogind: session.ManagedByLogind,
	})
	return webActionResultFrom(r)
}
