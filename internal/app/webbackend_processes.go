package app

import (
	"sermo/internal/process"
	"sermo/internal/web"
)

func processToWeb(p process.Process) web.Process {
	return web.Process{
		PID:         p.PID,
		PPID:        p.PPID,
		User:        p.User,
		Exe:         p.Exe,
		ExeResolved: p.ExeOK,
		Role:        p.Role,
		Source:      p.Source,
		Cmdline:     p.Cmdline,
	}
}

// procMetricReader is the subset of metrics.OSReader the process table needs;
// injectable so aggregation is testable without real /proc.
type procMetricReader interface {
	ProcessRSS(pid int) (uint64, bool)
	ProcessIO(pid int) (read, write uint64, ok bool)
	ProcessFDs(pid int) (uint64, bool)
	ProcessThreads(pid int) (uint64, bool)
}

// aggregateProcesses builds the per-process rows and the service total. Because
// procs is the whole discovered tree (matched processes plus their children),
// the total reflects the service's workers and helpers, not just its main
// process. The total is nil when there are no processes.
func aggregateProcesses(procs []process.Process, r procMetricReader) ([]web.Process, *web.ProcessTotals) {
	if len(procs) == 0 {
		return nil, nil
	}
	out := make([]web.Process, 0, len(procs))
	totals := web.ProcessTotals{Count: len(procs)}
	for i := range procs {
		wp := processToWeb(procs[i])
		if rss, ok := r.ProcessRSS(procs[i].PID); ok {
			wp.RSS = uintToInt64(rss)
			totals.RSS += uintToInt64(rss)
		}
		if rd, wr, ok := r.ProcessIO(procs[i].PID); ok {
			wp.IORead, wp.IOWrite = uintToInt64(rd), uintToInt64(wr)
			totals.IORead += uintToInt64(rd)
			totals.IOWrite += uintToInt64(wr)
		}
		if n, ok := r.ProcessFDs(procs[i].PID); ok {
			wp.FDs = uintToInt64(n)
			totals.FDs += uintToInt64(n)
		}
		if n, ok := r.ProcessThreads(procs[i].PID); ok {
			wp.Threads = uintToInt64(n)
			totals.Threads += uintToInt64(n)
		}
		out = append(out, wp)
	}
	return out, &totals
}

// attachLiveCPU folds the per-cycle live CPU sample into a service's detail.
func attachLiveCPU(d *web.Detail, live *LiveMetrics, service string) {
	if live == nil {
		return
	}
	sample, ok := live.Get(service)
	if !ok {
		return
	}
	if sample.PerProcCPU != nil {
		for i := range d.Processes {
			pid := d.Processes[i].PID
			pct, ok := sample.PerProcCPU[pid]
			if !ok {
				continue
			}
			d.Processes[i].CPU = pct
			d.Processes[i].HasCPU = true
			// MaxCore rides on the same sample: without a per-thread measurement it
			// is the process rate itself, flagged inexact so the UI shows it as a
			// bound rather than a reading.
			if maxCore, ok := sample.PerProcMaxCore[pid]; ok {
				d.Processes[i].MaxCore = maxCore
				d.Processes[i].MaxCoreExact = sample.PerProcMaxCoreExact[pid]
			}
		}
	}
	attachLiveTotals(d.ProcessTotals, live, service)
}

func attachLiveTotals(totals *web.ProcessTotals, live *LiveMetrics, service string) {
	if totals == nil || live == nil {
		return
	}
	sample, ok := live.Get(service)
	if !ok {
		return
	}
	totals.NumCPU = sample.NumCPU
	if sample.CPUReady {
		totals.CPU = sample.CPU
		totals.CPUThread = sample.CPUThread
		totals.HasCPU = true
	}
}
