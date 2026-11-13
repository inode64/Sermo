package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
	"sermo/internal/web"
)

type smbSessionSource struct {
	check   string
	config  checks.SMBSessionConfig
	timeout time.Duration
}

func smbSessionSources(tree map[string]any) []smbSessionSource {
	entries, _ := tree[config.SectionChecks].(map[string]any)
	var sources []smbSessionSource
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		entry, _ := entries[name].(map[string]any)
		if cfgval.Disabled(entry) || cfgval.String(entry[checks.CheckKeyType]) != checks.CheckTypeSMBSessions {
			continue
		}
		timeout, _ := cfgval.ParseDuration(entry[checks.CheckKeyTimeout])
		sources = append(sources, smbSessionSource{check: name, config: checks.SMBSessionConfigFromEntry(entry), timeout: timeout})
	}
	return sources
}

func (b *WebBackend) appendSMBSessions(result *web.SessionInventory, service string, entry *webEntry) {
	if len(entry.smbSessions) == 0 {
		return
	}
	snapshots := b.snapshots.Get(service)
	var procs []process.Process
	var warnings []string
	if !entry.disabled {
		procs, warnings = entry.discoverer.Discover(entry.selectors)
	}
	byPID := smbProcessIndex(procs)
	for _, configured := range entry.smbSessions {
		source := web.SessionSource{Kind: web.SessionKindSMB, Service: service, Check: configured.check, State: web.SessionSourceCollecting}
		snapshot, ok := snapshots[configured.check]
		switch {
		case entry.disabled:
			source.State, source.Message = web.SessionSourceUnavailable, "service disabled"
		case !ok || !serviceCheckSnapshotCurrentAt(entry, configured.check, snapshot, b.webNow()):
		case snapshot.Unavailable || snapshot.Skipped || cfgval.String(snapshot.Data[checks.DataKeySampleError]) != "":
			source.State, source.Message = web.SessionSourceUnavailable, snapshot.Message
		default:
			source.State = web.SessionSourceAvailable
			for _, session := range checks.DecodeDataSlice[checks.SMBSession](snapshot.Data[checks.DataKeySMBSessions]) {
				_, err := smbSessionWorker(entry, byPID, session)
				if len(warnings) != 0 {
					err = errors.New("SMB service ownership is incomplete")
				}
				row := web.SMBSession{SMBSession: session, Service: service, Check: configured.check, CanClose: err == nil}
				if err != nil {
					row.Message = err.Error()
				}
				result.SMB = append(result.SMB, row)
			}
		}
		result.Sources = append(result.Sources, source)
	}
}

// Samba workers impersonate connected users. The service's own kernel cgroup
// can authorize such a worker, while its live parent must still match a strict
// executable/user selector. The pidfd revalidates the worker's observed UID,
// executable, generation and cgroup before delivery.
func smbSessionWorker(entry *webEntry, procs map[int]process.Process, session checks.SMBSession) (process.Process, error) {
	if session.Identity == "" || session.StartTicks == 0 {
		return process.Process{}, errors.New("local SMB worker generation is unavailable (cluster sessions are observe-only)")
	}
	worker := procs[session.PID]
	parent := procs[worker.PPID]
	if worker.StartTicks != session.StartTicks || worker.PID <= 1 || parent.PID <= 1 || parent.StartTicks == 0 || worker.MainPidfile ||
		worker.Delegated || parent.Delegated || worker.Stray || parent.Stray || worker.External || parent.External ||
		worker.SignalBlockReason != "" || parent.SignalBlockReason != "" || !worker.ExeOK || !parent.ExeOK ||
		worker.Exe != parent.Exe || worker.Cgroup != parent.Cgroup {
		return process.Process{}, errors.New("SMB worker is no longer a verified child of this service")
	}
	principals := []process.Process{parent}
	if !servicemgr.CgroupOwnedBy(worker.Cgroup, entry.unit) {
		if worker.UID != parent.UID {
			return process.Process{}, errors.New("SMB worker changed user without verified service cgroup membership")
		}
		principals = append(principals, worker)
	}
	for _, proc := range principals {
		matched, ok := entry.discoverer.StrictMatchPID(proc.PID, entry.selectors)
		if !ok || !matched.SameGeneration(proc) || matched.Exe != proc.Exe || matched.UID != proc.UID || matched.Cgroup != proc.Cgroup {
			return process.Process{}, errors.New("SMB worker requires the service's exact executable and real user")
		}
	}
	return worker, nil
}

func smbProcessIndex(procs []process.Process) map[int]process.Process {
	byPID := make(map[int]process.Process, len(procs))
	for _, proc := range procs {
		byPID[proc.PID] = proc
	}
	return byPID
}

func freshSMBSessionVerifier(entry *webEntry, deps Deps) func(context.Context, operation.SMBSessionTarget) (process.Process, error) {
	sources := make(map[string]smbSessionSource, len(entry.smbSessions))
	for _, source := range entry.smbSessions {
		sources[source.check] = source
	}
	return func(ctx context.Context, target operation.SMBSessionTarget) (process.Process, error) {
		source, ok := sources[target.Check]
		if !ok {
			return process.Process{}, errors.New("SMB session source changed; refresh and try again")
		}
		timeout := source.timeout
		if timeout <= 0 {
			timeout = deps.DefaultTimeout
		}
		if timeout <= 0 {
			timeout = DefaultEngineCheckTimeout
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		sessions, err := checks.SampleSMBSessions(ctx, deps.ExecxRunner, source.config, deps.SMBSessionIdentity)
		if err != nil {
			return process.Process{}, fmt.Errorf("refresh SMB sessions: %w", err)
		}
		for _, session := range sessions {
			if session.Identity == "" || session.Identity != target.Identity {
				continue
			}
			observation, err := entry.discoverer.Observe(entry.selectors)
			if err != nil {
				return process.Process{}, fmt.Errorf("refresh SMB service processes: %w", err)
			}
			if !observation.Trusted || len(observation.Warnings) != 0 {
				return process.Process{}, errors.New("SMB service ownership is incomplete")
			}
			worker, err := smbSessionWorker(entry, smbProcessIndex(observation.Processes), session)
			if ctx.Err() != nil {
				return process.Process{}, fmt.Errorf("verify SMB worker: %w", ctx.Err())
			}
			return worker, err
		}
		return process.Process{}, errors.New("SMB connection changed or ended; refresh and try again")
	}
}

// Host watches have no service operation boundary and remain observe-only.
func (b *WebBackend) appendHostSMBSessions(result *web.SessionInventory) {
	observation := watchObservation{at: b.webNow()}
	for _, name := range b.watchOrder {
		w := b.watches[name]
		if w == nil || w.disabled || w.serviceScoped || w.checkType != checks.CheckTypeSMBSessions {
			continue
		}
		source := web.SessionSource{Kind: web.SessionKindSMB, Check: name, State: web.SessionSourceCollecting}
		snaps := b.watchSnapshots.Get(name, checks.CheckTypeSMBSessions)
		if len(snaps) > 0 && observation.watchSnapshotCurrent(w, snaps[0]) {
			snap := snaps[0]
			source.State = web.SessionSourceAvailable
			if snap.Unavailable || snap.Skipped {
				source.State, source.Message = web.SessionSourceUnavailable, snap.Message
			} else {
				for _, session := range checks.DecodeDataSlice[checks.SMBSession](snap.Data[checks.DataKeySMBSessions]) {
					result.SMB = append(result.SMB, web.SMBSession{SMBSession: session, Check: name, Message: "Host watches are observe-only; configure the inventory on the Samba service to close connections"})
				}
			}
		}
		result.Sources = append(result.Sources, source)
	}
}

// CloseSMBSession delegates the selected worker to the operation engine.
func (b *WebBackend) CloseSMBSession(ctx context.Context, name, check, identity string) web.ActionResult {
	entry, message := b.operationEntry(name)
	if entry == nil {
		return b.operateError(name, "close SMB connection", message)
	}
	return webActionResultFrom(entry.engine.CloseSMBSession(ctx, operation.SMBSessionTarget{Check: check, Identity: identity}))
}
