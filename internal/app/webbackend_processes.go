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
			wp.RSS = int64(rss)
			totals.RSS += int64(rss)
		}
		if rd, wr, ok := r.ProcessIO(procs[i].PID); ok {
			wp.IORead, wp.IOWrite = int64(rd), int64(wr)
			totals.IORead += int64(rd)
			totals.IOWrite += int64(wr)
		}
		if n, ok := r.ProcessFDs(procs[i].PID); ok {
			wp.FDs = int64(n)
			totals.FDs += int64(n)
		}
		if n, ok := r.ProcessThreads(procs[i].PID); ok {
			wp.Threads = int64(n)
			totals.Threads += int64(n)
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
			if pct, ok := sample.PerProcCPU[d.Processes[i].PID]; ok {
				d.Processes[i].CPU = pct
				d.Processes[i].HasCPU = true
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
