package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/process"
)

// DrainOutputSpec keeps captured bytes in a private, bounded regular file.
type DrainOutputSpec struct{ LogFile string }

// OutputDrainer is injectable so watch tests never touch a real process.
type OutputDrainer func(context.Context, process.TerminalOutput, string) (process.OutputDrain, error)

func parseDrainOutput(then map[string]any, checkType string) (string, error) {
	raw, present := then[config.WatchThenKeyDrainOutput]
	if !present {
		return "", nil
	}
	if checkType != checks.CheckTypeTerminalOutput {
		return "", errors.New("then.drain_output is only valid on a terminal_output watch")
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return "", errors.New("then.drain_output must be a mapping")
	}
	for key := range m {
		if key != config.WatchDrainOutputKeyLogFile {
			return "", fmt.Errorf("then.drain_output.%s is not supported", key)
		}
	}
	path := cfgval.String(m[config.WatchDrainOutputKeyLogFile])
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("then.drain_output.log_file must be an absolute clean path")
	}
	return path, nil
}

func (w *Watch) runDrainOutput(ctx context.Context, res checks.Result, emitSkipped bool) {
	targets, ok := res.Data[checks.DataKeyTerminalOutput].([]process.TerminalOutput)
	if !ok || len(targets) == 0 {
		w.skipNativeAction(eventKindOutputDrainSkipped, "no current stable terminal output evidence", emitSkipped)
		return
	}
	at, allowed := w.allowNativeAction(eventKindOutputDrainSkipped, emitSkipped)
	if !allowed {
		return
	}
	// One target per policy window bounds recovery I/O even when several jobs
	// satisfy the same selector. Every remaining target stays visible.
	target := targets[0]
	result, err := w.OutputDrainer(ctx, target, w.DrainOutput.LogFile)
	w.policyState.Record(at, w.Policy)
	message := fmt.Sprintf("pid %d: preserved %d/%d bytes in %s at offset %d (limited=%t)", target.Owner.PID, result.Bytes, result.ReadBytes, result.LogFile, result.LogOffset, result.Limited)
	kind := eventKindOutputDrain
	if errors.Is(err, process.ErrTerminalOutputChanged) || (err == nil && result.Bytes == 0) {
		kind = eventKindOutputDrainSkipped
	} else if err != nil {
		kind = eventKindOutputDrainFailed
	}
	if err != nil {
		message += "; " + err.Error()
	}
	w.emit(Event{Watch: w.Name, Kind: kind, Message: message})
}

func serviceTerminalOutput(discoverer process.Discoverer, selectors []process.Selector, sample checks.TerminalOutputFunc) checks.TerminalOutputFunc {
	return func(ctx context.Context, requested []process.Selector) ([]process.TerminalOutput, error) {
		observed, err := discoverer.ObserveCached(selectors)
		if err != nil {
			return nil, fmt.Errorf("observe service output ownership: %w", err)
		}
		if uncertain := process.UncertainWarnings(observed.Warnings); len(uncertain) > 0 {
			return nil, fmt.Errorf("incomplete service output ownership: %v", uncertain)
		}
		owned := map[int]process.Process{}
		for _, p := range observed.Processes {
			if p.Source != process.SourceChild && !p.Delegated && !p.Stray && !p.External {
				owned[p.PID] = p
			}
		}
		if len(owned) == 0 {
			return nil, nil
		}
		targets, err := sample(ctx, requested)
		if err != nil {
			return nil, fmt.Errorf("observe terminal output: %w", err)
		}
		var result []process.TerminalOutput
		for _, target := range targets {
			matches := func(p process.Process) bool {
				return target.Owner == (process.OutputIdentity{PID: p.PID, StartTicks: p.StartTicks, Exe: p.Exe, UID: p.UID, Cgroup: p.Cgroup})
			}
			previous, ok := owned[target.Owner.PID]
			if !ok || !matches(previous) {
				continue
			}
			if live, strict := discoverer.StrictMatchPID(target.Owner.PID, selectors); strict && matches(live) {
				result = append(result, target)
			}
		}
		return result, nil
	}
}
