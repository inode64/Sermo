package process

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"sermo/internal/hostfs"
	"sermo/internal/servicemgr"
)

const (
	supervisedCgroupRoot   = "/sys/fs/cgroup"
	supervisedMaxPIDs      = 64
	supervisedCgroupBytes  = 64 * 1024
	supervisedPollInterval = 250 * time.Millisecond
)

// ErrSupervisedNotReady means a complete observation proved a missing or
// duplicated supervisor/worker, or no worker appeared during the respawn grace.
var ErrSupervisedNotReady = errors.New("supervised process is not ready")
var errSupervisedUnstable = errors.New("supervised process changed during observation")

// SupervisedProcessSpec describes one worker and one supervisor in an exact
// unified cgroup. It is observation policy, never signal authority.
type SupervisedProcessSpec struct {
	Cgroup, Exe, User, SupervisorExe, SupervisorUser string
	RespawnGrace                                     time.Duration
}

// SupervisedProcessSample reports the worker and supervisor observed together.
type SupervisedProcessSample struct {
	WorkerPID, SupervisorPID int
	Waited                   time.Duration
}

type supervisedSource struct {
	reader   Reader
	resolve  UserResolver
	readFile func(string) ([]byte, error)
	interval time.Duration
}

type supervisedGeneration struct {
	pid   int
	ticks uint64
}
type supervisedObservation struct {
	supervisor supervisedGeneration
	worker     int
}

// WaitSupervisedProcess checks fresh identities in only the configured cgroup;
// a monitoring snapshot cannot establish continuity during a respawn wait.
func WaitSupervisedProcess(ctx context.Context, spec SupervisedProcessSpec) (SupervisedProcessSample, error) {
	if err := ctx.Err(); err != nil {
		return SupervisedProcessSample{}, fmt.Errorf("observe supervised process: %w", err)
	}
	var err error
	spec.Exe, err = hostfs.EvalSymlinksTarget(spec.Exe)
	if err != nil {
		return SupervisedProcessSample{}, fmt.Errorf("resolve worker executable: %w", err)
	}
	spec.SupervisorExe, err = hostfs.EvalSymlinksTarget(spec.SupervisorExe)
	if err != nil {
		return SupervisedProcessSample{}, fmt.Errorf("resolve supervisor executable: %w", err)
	}
	lookup := DefaultUserLookup()
	source := supervisedSource{reader: OSReader{LookupUserName: lookup.Username}, resolve: lookup.ResolveUser,
		readFile: func(path string) ([]byte, error) {
			data, err := hostfs.ReadRegularFile(path, supervisedCgroupBytes+1)
			if err != nil {
				return nil, fmt.Errorf("read supervised cgroup: %w", err)
			}
			if len(data) > supervisedCgroupBytes {
				return nil, errors.New("supervised cgroup exceeds read bound")
			}
			return data, nil
		}, interval: supervisedPollInterval}
	return source.wait(ctx, spec)
}

func (s supervisedSource) wait(ctx context.Context, spec SupervisedProcessSpec) (SupervisedProcessSample, error) {
	started := time.Now()
	workerUID, ok := s.resolve(spec.User)
	if !ok {
		return SupervisedProcessSample{}, fmt.Errorf("cannot resolve worker user %q", spec.User)
	}
	supervisorUID, ok := s.resolve(spec.SupervisorUser)
	if !ok {
		return SupervisedProcessSample{}, fmt.Errorf("cannot resolve supervisor user %q", spec.SupervisorUser)
	}
	bounded, cancel := context.WithTimeout(ctx, spec.RespawnGrace)
	defer cancel()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	var principal supervisedGeneration
	observationErr := errSupervisedUnstable
	for {
		observation, err := s.observe(bounded, spec, workerUID, supervisorUID)
		if ctx.Err() != nil {
			return SupervisedProcessSample{}, fmt.Errorf("observe supervised process: %w", ctx.Err())
		}
		// The grace deadline may interrupt the next scan; preserve the last
		// observation, but never turn an unstable snapshot into proven absence.
		if !errors.Is(err, context.DeadlineExceeded) {
			observationErr = err
		}
		if bounded.Err() != nil {
			return SupervisedProcessSample{}, supervisedRespawnError(spec.RespawnGrace, observationErr)
		}
		if err != nil && !errors.Is(err, errSupervisedUnstable) {
			return SupervisedProcessSample{}, err
		}
		if err == nil {
			if principal.pid != 0 && principal != observation.supervisor {
				return SupervisedProcessSample{}, errors.New("supervisor generation changed during verification")
			}
			principal = observation.supervisor
			if observation.worker != 0 {
				return SupervisedProcessSample{WorkerPID: observation.worker, SupervisorPID: principal.pid, Waited: time.Since(started)}, nil
			}
		}
		select {
		case <-ctx.Done():
			return SupervisedProcessSample{}, fmt.Errorf("observe supervised process: %w", ctx.Err())
		case <-bounded.Done():
			return SupervisedProcessSample{}, supervisedRespawnError(spec.RespawnGrace, observationErr)
		case <-ticker.C:
		}
	}
}

func supervisedRespawnError(grace time.Duration, observationErr error) error {
	if observationErr != nil {
		return fmt.Errorf("cannot verify supervised worker after %s: %w", grace, observationErr)
	}
	return fmt.Errorf("%w: no stable worker after %s", ErrSupervisedNotReady, grace)
}

func (s supervisedSource) observe(ctx context.Context, spec SupervisedProcessSpec, workerUID, supervisorUID uint32) (supervisedObservation, error) {
	members, err := s.members(spec.Cgroup)
	if err != nil {
		return supervisedObservation{}, err
	}
	var workers []int
	var supervisors []supervisedGeneration
	for _, pid := range members {
		if err := ctx.Err(); err != nil {
			return supervisedObservation{}, fmt.Errorf("observe supervised identity: %w", err)
		}
		id, err := s.stableIdentity(pid)
		if err != nil {
			return supervisedObservation{}, err
		}
		worker := id.Exe == spec.Exe && id.UID == workerUID
		supervisor := id.Exe == spec.SupervisorExe && id.UID == supervisorUID
		if !worker && !supervisor {
			continue
		}
		group, valid := servicemgr.UnifiedCgroupPath(id.Cgroup)
		if !valid || group != spec.Cgroup {
			return supervisedObservation{}, errors.New("supervised process has foreign or unreadable cgroup membership")
		}
		if id.State == ProcStateZombie || id.State == "X" {
			continue
		}
		if worker {
			workers = append(workers, pid)
		} else {
			supervisors = append(supervisors, supervisedGeneration{pid: pid, ticks: id.StartTicks})
		}
	}
	after, err := s.members(spec.Cgroup)
	if err != nil {
		return supervisedObservation{}, err
	}
	if !slices.Equal(members, after) {
		return supervisedObservation{}, errSupervisedUnstable
	}
	if len(supervisors) != 1 || len(workers) > 1 {
		return supervisedObservation{}, fmt.Errorf("%w: got %d supervisors and %d workers", ErrSupervisedNotReady, len(supervisors), len(workers))
	}
	observed := supervisedObservation{supervisor: supervisors[0]}
	if len(workers) == 1 {
		observed.worker = workers[0]
	}
	return observed, nil
}

func (s supervisedSource) members(group string) ([]int, error) {
	if !filepath.IsAbs(group) || filepath.Clean(group) != group || group == "/" || strings.ContainsRune(group, 0) {
		return nil, errors.New("supervised cgroup must be an absolute clean non-root path")
	}
	data, err := s.readFile(filepath.Join(supervisedCgroupRoot, group, "cgroup.procs"))
	if err != nil {
		return nil, fmt.Errorf("read supervised members: %w", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) > supervisedMaxPIDs {
		return nil, errors.New("supervised cgroup exceeds process bound")
	}
	pids := make([]int, 0, len(fields))
	for _, value := range fields {
		pid, err := strconv.Atoi(value)
		if err != nil || pid <= 1 {
			return nil, fmt.Errorf("invalid supervised PID %q", value)
		}
		pids = append(pids, pid)
	}
	slices.Sort(pids)
	if len(slices.Compact(slices.Clone(pids))) != len(pids) {
		return nil, errors.New("duplicate supervised PID")
	}
	return pids, nil
}

func (s supervisedSource) stableIdentity(pid int) (Identity, error) {
	before, ok, err := readIdentity(s.reader, pid)
	if err != nil {
		return Identity{}, err
	}
	if !ok {
		return Identity{}, errSupervisedUnstable
	}
	if !before.StartTicksOK || (!before.ExeOK && before.ExePrev == "") {
		return Identity{}, errors.New("supervised process identity is incomplete")
	}
	after, ok, err := readIdentity(s.reader, pid)
	if err != nil {
		return Identity{}, err
	}
	if !ok || !after.StartTicksOK {
		return Identity{}, errSupervisedUnstable
	}
	if before.StartTicks != after.StartTicks || before.Exe != after.Exe || before.UID != after.UID || before.Cgroup != after.Cgroup {
		return Identity{}, errSupervisedUnstable
	}
	return after, nil
}
