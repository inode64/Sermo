package checks

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/process"
)

// Supervised process checks use exact identities and an explicit unified cgroup.
const (
	CheckTypeSupervisedProcess = "supervised_process"
	CheckKeyCgroup             = "cgroup"
	CheckKeySupervisor         = "supervisor"
	CheckKeyRespawnGrace       = "respawn_grace"
	supervisedProcessGrace     = 8 * time.Second
	supervisedProcessTimeout   = 10 * time.Second
)

type supervisedProcessCheck struct {
	base
	spec   process.SupervisedProcessSpec
	sample func(context.Context, process.SupervisedProcessSpec) (process.SupervisedProcessSample, error)
}

// SupervisedProcessConfig is shared by validation and runtime construction.
func SupervisedProcessConfig(entry map[string]any) (process.SupervisedProcessSpec, error) {
	supervisor, ok := entry[CheckKeySupervisor].(map[string]any)
	if !ok {
		return process.SupervisedProcessSpec{}, errors.New("supervisor requires exe and user")
	}
	spec := process.SupervisedProcessSpec{
		Cgroup: cfgval.AsString(entry[CheckKeyCgroup]), Exe: cfgval.AsString(entry[CheckKeyExe]), User: cfgval.AsString(entry[CheckKeyUser]),
		SupervisorExe: cfgval.AsString(supervisor[CheckKeyExe]), SupervisorUser: cfgval.AsString(supervisor[CheckKeyUser]), RespawnGrace: supervisedProcessGrace,
	}
	for _, target := range []struct{ name, value string }{{CheckKeyExe, spec.Exe}, {CheckKeyCgroup, spec.Cgroup}, {CheckKeySupervisor + "." + CheckKeyExe, spec.SupervisorExe}} {
		if !filepath.IsAbs(target.value) || filepath.Clean(target.value) != target.value || target.value == "/" || strings.ContainsRune(target.value, 0) {
			return spec, fmt.Errorf("%s must be an absolute clean non-root path", target.name)
		}
	}
	if spec.User == "" || spec.SupervisorUser == "" {
		return spec, errors.New("worker and supervisor users are required")
	}
	if spec.Exe == spec.SupervisorExe && spec.User == spec.SupervisorUser {
		return spec, errors.New("worker and supervisor identities must differ")
	}
	if raw, present := entry[CheckKeyRespawnGrace]; present {
		spec.RespawnGrace = cfgval.Duration(raw)
		if spec.RespawnGrace <= 0 {
			return spec, errors.New("respawn_grace must be positive")
		}
	}
	timeout := supervisedProcessTimeout
	if raw, present := entry[CheckKeyTimeout]; present {
		timeout = cfgval.Duration(raw)
	}
	if timeout <= spec.RespawnGrace {
		return spec, errors.New("timeout must exceed respawn_grace; set an explicit timeout when increasing the grace")
	}
	return spec, nil
}

func buildSupervisedProcessCheck(b base, entry map[string]any) (Check, string) {
	spec, err := SupervisedProcessConfig(entry)
	if err != nil {
		return nil, err.Error()
	}
	return supervisedProcessCheck{base: b, spec: spec, sample: process.WaitSupervisedProcess}, ""
}

func (c supervisedProcessCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	sample, err := c.sample(ctx, c.spec)
	if err != nil {
		if errors.Is(err, process.ErrSupervisedNotReady) {
			return c.result(false, err.Error(), run.start)
		}
		return c.unavailableResult(err.Error(), run.start)
	}
	result := c.result(true, "one verified worker and supervisor in the configured cgroup", run.start)
	result.Data = map[string]any{DataKeyPID: sample.WorkerPID, "supervisor_pid": sample.SupervisorPID, "respawn_wait_seconds": sample.Waited.Seconds()}
	return result
}
