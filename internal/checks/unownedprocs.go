package checks

// UnownedProcess is one row an unowned_processes watch publishes under
// DataKeyProcesses: the process identity the dashboard shows and the kill
// button echoes back (PID plus StartTicks), its resource readings, why it is
// listed and whether an admin may signal it. It never carries the command
// line. The JSON tags are the one schema, live and persisted.
type UnownedProcess struct {
	PID         int     `json:"pid"`
	StartTicks  uint64  `json:"start_ticks,omitempty"`
	User        string  `json:"user,omitempty"`
	UID         uint32  `json:"uid"`
	Exe         string  `json:"exe,omitempty"`
	ExeResolved bool    `json:"exe_resolved"`
	ExePrevious string  `json:"exe_previous,omitempty"`
	RSS         uint64  `json:"rss,omitempty"`
	CPU         float64 `json:"cpu,omitempty"`
	HasCPU      bool    `json:"has_cpu,omitempty"`
	Reason      string  `json:"reason"`
	CanKill     bool    `json:"can_kill"`
	KillReason  string  `json:"kill_reason,omitempty"`
}

// UnownedProcessesFromData rehydrates the rows an unowned_processes snapshot
// published, live or decoded from the persisted JSON. It returns a new slice:
// the decoded one may be the live slice the watch published, which readers
// share and must never modify.
func UnownedProcessesFromData(data map[string]any) []UnownedProcess {
	decoded := DecodeDataSlice[UnownedProcess](data[DataKeyProcesses])
	out := make([]UnownedProcess, 0, len(decoded))
	for i := range decoded {
		if decoded[i].PID > 0 {
			out = append(out, decoded[i])
		}
	}
	return out
}
