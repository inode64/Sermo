package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"sermo/internal/checks"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/web"
)

const (
	sshCatalogApp                  = "ssh"
	interactiveSessionCacheTTL     = 5 * time.Second
	sshSessionUnavailablePrefix    = "SSH session attribution unavailable: "
	sshSessionUnsupportedMessage   = "does not define a safely identifiable SSH session boundary"
	sshSessionSamplerFailedMessage = "load SSH sessions: "
)

type cachedSSHSessions struct {
	at     time.Time
	sample checks.SSHSessionSample
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

// CloseSSHSession sends only a graceful SIGTERM through the service operation
// engine. The engine calls its fresh verifier immediately before signalling, so
// this request cannot close a terminal merely because its old PID was reused.
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
		PID:        session.PID,
		StartTicks: session.StartTicks,
		Terminal:   session.Terminal,
	})
	return webActionResultFrom(r, name, "close SSH session")
}
