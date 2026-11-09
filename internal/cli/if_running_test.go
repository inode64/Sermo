package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"sermo/internal/config"
	"sermo/internal/operation"
	"sermo/internal/servicemgr"
)

func TestIfRunningSkipsInactiveThroughEngine(t *testing.T) {
	for _, action := range []string{actionRestart, actionStop, actionReload} {
		t.Run(action, func(t *testing.T) {
			global := writeActionConfig(t)
			var stdout, stderr bytes.Buffer
			var actions []string
			noReload := false
			a := hermeticApp(t, App{Detector: fakeBackendDetector{detection: servicemgr.BackendSystemd}, NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
				return fakeManager{actions: &actions, status: servicemgr.ServiceStatus{Status: servicemgr.StatusInactive}, supportsReload: &noReload}, nil
			}, Runner: statusUnitRunner{known: "web.service"}, Stdout: &stdout, Stderr: &stderr})
			code := a.Run(t.Context(), []string{"--config", global, action, "web", "--if-running", "--json"})
			if code != exitSuccess {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			var out operation.Result
			if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out.Status != operation.ResultSkipped || len(actions) != 0 {
				t.Fatalf("result=%+v actions=%v", out, actions)
			}
			store := openTestStateStore(t, global)
			defer func() { _ = store.Close() }()
			_, found, err := store.MonitorState("web")
			if err != nil || found {
				t.Fatalf("skipped action changed monitoring: found=%v err=%v", found, err)
			}
			_, found = readOperationSettling(t, global, "web")
			if found {
				t.Fatal("skipped action left settling marker")
			}
		})
	}
}

func TestSkippedActionExitAndText(t *testing.T) {
	global := writeActionConfig(t)
	var stdout bytes.Buffer
	a := actionApp(t, operation.Result{Service: "web", Action: actionRestart, Status: operation.ResultSkipped, Message: "service is not running; action skipped"}, nil, &stdout, nil)
	if code := a.Run(context.Background(), []string{"--config", global, "restart", "web", "--if-running"}); code != exitSuccess {
		t.Fatalf("exit=%d", code)
	}
	if got := stdout.String(); got != "web restart skipped\nreason: service is not running; action skipped\n" {
		t.Fatalf("stdout=%q", got)
	}
}

func TestIfRunningPropagatesToCascadeTargets(t *testing.T) {
	global := writeCascadeConfig(t)
	var stdout bytes.Buffer
	a := actionApp(t, operation.Result{}, nil, &stdout, nil)
	calls := map[string]int{}
	a.Operate = func(_ context.Context, opts options, _ *config.Config, _ config.Resolved, service, action string) (operation.Result, error) {
		if !opts.ifRunning {
			t.Fatal("cascade lost --if-running")
		}
		calls[service]++
		status := operation.ResultSkipped
		if service == "db" {
			status = operation.ResultOK
		}
		return operation.Result{Service: service, Action: action, Status: status}, nil
	}
	if code := a.Run(t.Context(), []string{"--config", global, "restart", "web", "--if-running", "--json"}); code != exitSuccess {
		t.Fatalf("exit=%d output=%s", code, stdout.String())
	}
	var out actionJSON
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if calls["web"] != 1 || calls["db"] != 1 || out.Status != operation.ResultSkipped || len(out.Cascade) != 1 || out.Cascade[0].Status != operation.ResultOK {
		t.Fatalf("calls=%v result=%+v", calls, out)
	}
}
