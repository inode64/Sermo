package cli

import (
	"bytes"
	"strings"
	"testing"

	"sermo/internal/operation"
	"sermo/internal/process"
)

func TestDeletedResidualReporting(t *testing.T) {
	t.Parallel()
	p := process.Process{PID: 200, User: "squid", Role: "pinger", ExePrev: "/usr/libexec/squid/pinger", External: true,
		SignalBlockReason: "cannot verify service ownership"}
	line := formatProcess(p)
	for _, want := range []string{"pid=200", "user=squid", "role=pinger", "exe_previous=\"/usr/libexec/squid/pinger (deleted)\"", "external=true", "blocked=\"cannot verify service ownership\""} {
		if !strings.Contains(line, want) {
			t.Errorf("process output %q missing %q", line, want)
		}
	}
	var out bytes.Buffer
	a := App{Stdout: &out}
	a.printOperation(options{}, operation.Result{Service: "squid", Action: actionRestart, Status: operation.ResultOrphanProcesses,
		Processes: []process.Process{p}, Signals: []process.SignalAttempt{{PID: 200, Signal: "SIGTERM", Error: "permission denied"}}})
	for _, want := range []string{"exe_previous=", "blocked: cannot verify service ownership", "signal pid=200 SIGTERM", "permission denied"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("action output %q missing %q", out.String(), want)
		}
	}
}
