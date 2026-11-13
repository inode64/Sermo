package checks

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/process"
)

// SMBSession groups the logical sessions served by one Samba worker. Resource
// accounting and SIGTERM both operate on that worker, not an individual login.
type SMBSession struct {
	PID          int    `json:"pid"`
	StartTicks   uint64 `json:"start_ticks,omitempty"`
	User         string `json:"user"`
	Client       string `json:"client"`
	SessionCount int    `json:"session_count"`
	Identity     string `json:"identity,omitempty"`
}

// SMBSessionConfig selects Samba's local status database through its own tool.
// Path, when set, is the smb.conf used by this instance.
type SMBSessionConfig struct {
	Binary string
	Path   string
}

// SMBSessionConfigFromEntry is shared by check construction and manual refresh.
func SMBSessionConfigFromEntry(entry map[string]any) SMBSessionConfig {
	return SMBSessionConfig{Binary: cfgval.String(entry[CheckKeyBinary]), Path: cfgval.String(entry[CheckKeyPath])}
}

// Validate rejects paths that cannot select an explicit local Samba instance.
func (c SMBSessionConfig) Validate() error {
	if !filepath.IsAbs(c.Binary) || filepath.Clean(c.Binary) != c.Binary {
		return errors.New("binary must be an absolute, clean smbstatus path")
	}
	if c.Path != "" && (!filepath.IsAbs(c.Path) || filepath.Clean(c.Path) != c.Path) {
		return errors.New("path must be an absolute, clean smb.conf path")
	}
	return nil
}

type smbServerID struct {
	PID      string `json:"pid"`
	TaskID   string `json:"task_id"`
	VNN      string `json:"vnn"`
	UniqueID string `json:"unique_id"`
}

type smbLogin struct {
	ID       string      `json:"session_id"`
	Server   smbServerID `json:"server_id"`
	User     string      `json:"username"`
	Remote   string      `json:"remote_machine"`
	Hostname string      `json:"hostname"`
}

// Samba's non-cluster server IDs use UINT32_MAX. A CTDB PID may belong to a
// different host, so cluster rows never acquire local metrics or signal rights.
const smbLocalVNN = "4294967295"

// SampleSMBSessions reads a bounded, read-only inventory. The process generation
// is observation only; a close must refresh this inventory and authorize the
// worker against the service's strict process selectors.
func SampleSMBSessions(ctx context.Context, runner execx.Runner, config SMBSessionConfig, identity func(int) (process.Identity, bool)) ([]SMBSession, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read SMB sessions: %w", err)
	}
	args := []string{"--json", "--processes"}
	if config.Path != "" {
		args = append(args, "--configfile="+config.Path)
	}
	result, err := execx.RunnerOrDefault(runner).Run(ctx, config.Binary, args...)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("read SMB sessions: %w", ctx.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("read SMB sessions: %w", err)
	}
	if result.ExitCode != execx.ExitCodeSuccess {
		return nil, fmt.Errorf("read SMB sessions: exit %d", result.ExitCode)
	}
	if len(result.Stdout) >= execx.MaxCommandOutput {
		return nil, errors.New("SMB session output exceeds the command output limit")
	}
	if identity == nil {
		identity = process.OSReader{}.Identity
	}
	return parseSMBSessions(ctx, result.Stdout, identity)
}

func parseSMBSessions(ctx context.Context, output string, identity func(int) (process.Identity, bool)) ([]SMBSession, error) {
	var document struct {
		Sessions map[string]smbLogin `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(output), &document); err != nil {
		return nil, fmt.Errorf("decode smbstatus JSON: %w", err)
	}
	if document.Sessions == nil {
		return nil, errors.New("smbstatus JSON has no sessions object")
	}
	groups := make(map[smbServerID][]smbLogin)
	localServers := make(map[int]smbServerID)
	for _, key := range slices.Sorted(maps.Keys(document.Sessions)) {
		login := document.Sessions[key]
		pid, err := strconv.Atoi(login.Server.PID)
		if err != nil || pid <= 1 || pid > math.MaxInt32 || login.ID != key || login.User == "" || !login.validIDs() {
			return nil, errors.New("smbstatus returned an incomplete session identity")
		}
		if login.Server.VNN == smbLocalVNN {
			if previous, ok := localServers[pid]; ok && previous != login.Server {
				return nil, errors.New("smbstatus returned conflicting generations for one local worker")
			}
			localServers[pid] = login.Server
		}
		groups[login.Server] = append(groups[login.Server], login)
	}
	sessions := make([]SMBSession, 0, len(groups))
	for server, logins := range groups {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("read SMB worker identities: %w", err)
		}
		pid, _ := strconv.Atoi(server.PID)
		session := SMBSession{PID: pid, SessionCount: len(logins)}
		users, clients := make([]string, 0, len(logins)), make([]string, 0, len(logins))
		for _, login := range logins {
			users = append(users, login.User)
			clients = append(clients, smbClientIP(login))
		}
		slices.Sort(users)
		slices.Sort(clients)
		session.User, session.Client = strings.Join(slices.Compact(users), ", "), strings.Join(slices.Compact(clients), ", ")
		if server.VNN == smbLocalVNN && server.TaskID == "0" {
			if id, ok := identity(pid); ok && id.StartTicksOK && id.StartTicks != 0 && id.State != process.ProcStateZombie {
				session.StartTicks = id.StartTicks
			}
		}
		if session.StartTicks != 0 {
			// Sorted logins bind every affected username, address and Samba
			// generation; a changed group invalidates a displayed close request.
			encoded, err := json.Marshal(logins)
			if err != nil {
				return nil, fmt.Errorf("encode SMB identity: %w", err)
			}
			session.Identity = fmt.Sprintf("%x:%d", sha256.Sum256(encoded), session.StartTicks)
		}
		sessions = append(sessions, session)
	}
	slices.SortFunc(sessions, func(a, b SMBSession) int { return a.PID - b.PID })
	return sessions, nil
}

func (s smbLogin) validIDs() bool {
	for _, value := range []string{s.ID, s.Server.UniqueID, s.Server.VNN, s.Server.TaskID} {
		if _, err := strconv.ParseUint(value, 10, 64); err != nil {
			return false
		}
	}
	return s.ID != "0" && s.Server.UniqueID != "0"
}

func smbClientIP(login smbLogin) string {
	if addr, err := netip.ParseAddr(login.Remote); err == nil {
		return addr.String()
	}
	for _, prefix := range []string{"ipv4:", "ipv6:"} {
		if endpoint, ok := strings.CutPrefix(login.Hostname, prefix); ok {
			if addr, err := netip.ParseAddrPort(endpoint); err == nil {
				return addr.Addr().String()
			}
		}
	}
	return login.Remote
}

type smbSessionsCheck struct {
	base
	preds    []levelPred
	config   SMBSessionConfig
	runner   execx.Runner
	identity func(int) (process.Identity, bool)
}

func (c smbSessionsCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	sessions, err := SampleSMBSessions(ctx, c.runner, c.config, c.identity)
	if err != nil {
		failure := execx.FormatContextOrError(err, c.timeout)
		res := c.unavailableResult("SMB sessions: "+failure, run.start)
		res.Data = map[string]any{DataKeySampleError: failure}
		return res
	}
	count := 0
	for _, session := range sessions {
		count += session.SessionCount
	}
	values := map[string]float64{DataKeyCount: float64(count)}
	res := c.grade(c.result(levelPredsHold(c.preds, values), fmt.Sprintf("%d SMB session(s) in %d worker(s)", count, len(sessions)), run.start), values)
	res.Data = map[string]any{DataKeyCount: count, DataKeySMBSessions: sessions, DataKeyValue: float64(count), DataKeyUnit: metrics.MetricUnitSessions}
	return res
}

func buildSMBSessionsCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, SMBSessionPredFields, "smb_sessions check")
	if errs != "" {
		return nil, errs
	}
	config := SMBSessionConfigFromEntry(entry)
	if err := config.Validate(); err != nil {
		return nil, "smb_sessions check: " + err.Error()
	}
	return smbSessionsCheck{base: b, preds: preds, config: config, runner: deps.Runner, identity: deps.SMBSessionIdentity}, ""
}
