package checks

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"sermo/internal/cfgval"
	"sermo/internal/execx"
	"sermo/internal/output"
)

const (
	glusterCommand         = "gluster"
	glusterModeScriptFlag  = "--mode=script"
	glusterXMLFlag         = "--xml"
	glusterPeerInCluster   = "Peer in Cluster"
	glusterVolumeStarted   = "Started"
	glusterSelfHealDaemon  = "Self-heal Daemon"
	glusterCLIExitSuccess  = 0
	glusterStatusOnline    = 1
	glusterPeerStateMember = 3
)

// glusterClusterCheck queries the local Gluster CLI's XML output. The CLI owns
// the authenticated management RPC, while this check only invokes its read-only
// status commands with a bounded context.
type glusterClusterCheck struct {
	base
	runner  execx.Runner
	peers   []string
	volumes map[string]glusterVolumeExpectation
}

type glusterVolumeExpectation struct {
	bricks               int
	selfHeal             bool
	maxHealEntries       *int
	maxSplitBrainEntries *int
}

type glusterCLIOutput struct {
	OpRet    string                   `xml:"opRet"`
	OpErrstr string                   `xml:"opErrstr"`
	Peers    []glusterPeerXML         `xml:"peerStatus>peer"`
	Volumes  []glusterVolumeXML       `xml:"volInfo>volumes>volume"`
	Status   []glusterVolumeStatusXML `xml:"volStatus>volumes>volume"`
	Heals    []glusterHealBrickXML    `xml:"healInfo>bricks>brick"`
}

type glusterPeerXML struct {
	Hostname  string   `xml:"hostname"`
	Hostnames []string `xml:"hostnames>hostname"`
	Connected int      `xml:"connected"`
	State     int      `xml:"state"`
	StateStr  string   `xml:"stateStr"`
}

type glusterVolumeXML struct {
	Name       string `xml:"name"`
	Status     int    `xml:"status"`
	StatusStr  string `xml:"statusStr"`
	BrickCount int    `xml:"brickCount"`
}

type glusterVolumeStatusXML struct {
	Name  string                 `xml:"volName"`
	Nodes []glusterVolumeNodeXML `xml:"node"`
}

type glusterVolumeNodeXML struct {
	Hostname string `xml:"hostname"`
	Path     string `xml:"path"`
	Status   int    `xml:"status"`
}

type glusterHealBrickXML struct {
	Name            string `xml:"name"`
	Status          string `xml:"status"`
	NumberOfEntries int    `xml:"numberOfEntries"`
}

func (c glusterClusterCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()

	peers, err := c.command(ctx, "peer", "status")
	if err != nil {
		return c.unavailableResult("gluster_cluster: "+err.Error(), run.start)
	}
	volumes, err := c.command(ctx, "volume", "info")
	if err != nil {
		return c.unavailableResult("gluster_cluster: "+err.Error(), run.start)
	}
	statuses, err := c.command(ctx, "volume", "status")
	if err != nil {
		return c.unavailableResult("gluster_cluster: "+err.Error(), run.start)
	}

	observation := glusterClusterObservation{expectedPeers: len(c.peers), expectedVolumes: len(c.volumes)}
	c.evaluatePeers(peers.Peers, &observation)
	c.evaluateVolumes(volumes.Volumes, statuses.Status, &observation)
	if err := c.evaluateHeals(ctx, &observation); err != nil {
		return c.unavailableResult("gluster_cluster: "+err.Error(), run.start)
	}
	return c.resultFor(observation, run.start)
}

type glusterClusterObservation struct {
	expectedPeers, connectedPeers   int
	expectedVolumes, startedVolumes int
	expectedBricks, onlineBricks    int
	selfHealOnline, selfHealTotal   int
	healEntries, splitBrainEntries  int
	disconnectedPeers, issues       []string
}

func (c glusterClusterCheck) evaluatePeers(peers []glusterPeerXML, observation *glusterClusterObservation) {
	byName := make(map[string]glusterPeerXML, len(peers))
	for _, peer := range peers {
		for _, name := range peer.names() {
			byName[name] = peer
		}
		if !peer.healthy() {
			observation.disconnectedPeers = append(observation.disconnectedPeers, peer.label())
		}
	}
	for _, name := range c.peers {
		peer, present := byName[name]
		if !present {
			observation.issues = append(observation.issues, "peer "+name+" is absent")
			continue
		}
		if peer.healthy() {
			observation.connectedPeers++
			continue
		}
		observation.issues = append(observation.issues, "peer "+name+" is disconnected")
	}
	for _, name := range observation.disconnectedPeers {
		observation.issues = append(observation.issues, "peer "+name+" is disconnected")
	}
}

func (c glusterClusterCheck) evaluateVolumes(volumes []glusterVolumeXML, statuses []glusterVolumeStatusXML, observation *glusterClusterObservation) {
	byName := make(map[string]glusterVolumeXML, len(volumes))
	for _, volume := range volumes {
		byName[volume.Name] = volume
	}
	statusByName := make(map[string]glusterVolumeStatusXML, len(statuses))
	for _, status := range statuses {
		statusByName[status.Name] = status
	}
	for _, name := range slices.Sorted(maps.Keys(c.volumes)) {
		expectation := c.volumes[name]
		observation.expectedBricks += expectation.bricks
		volume, present := byName[name]
		if !present {
			observation.issues = append(observation.issues, "volume "+name+" is absent")
			continue
		}
		if volume.started() {
			observation.startedVolumes++
		} else {
			observation.issues = append(observation.issues, "volume "+name+" is not started")
		}
		if volume.BrickCount != expectation.bricks {
			observation.issues = append(observation.issues, fmt.Sprintf("volume %s has %d bricks (want %d)", name, volume.BrickCount, expectation.bricks))
		}
		status, present := statusByName[name]
		if !present {
			observation.issues = append(observation.issues, "volume "+name+" has no runtime status")
			continue
		}
		c.evaluateVolumeStatus(name, expectation, status.Nodes, observation)
	}
}

func (c glusterClusterCheck) evaluateVolumeStatus(name string, expectation glusterVolumeExpectation, nodes []glusterVolumeNodeXML, observation *glusterClusterObservation) {
	selfHealFound := false
	for _, node := range nodes {
		if node.Hostname == glusterSelfHealDaemon {
			selfHealFound = true
			observation.selfHealTotal++
			if node.Status != glusterStatusOnline {
				observation.issues = append(observation.issues, "volume "+name+" self-heal daemon is offline")
			} else {
				observation.selfHealOnline++
			}
			continue
		}
		if node.Status != glusterStatusOnline {
			observation.issues = append(observation.issues, "volume "+name+" brick "+node.label()+" is offline")
			continue
		}
		observation.onlineBricks++
	}
	if expectation.selfHeal && !selfHealFound {
		observation.issues = append(observation.issues, "volume "+name+" has no self-heal daemon")
	}
}

func (c glusterClusterCheck) evaluateHeals(ctx context.Context, observation *glusterClusterObservation) error {
	for _, name := range slices.Sorted(maps.Keys(c.volumes)) {
		expectation := c.volumes[name]
		if expectation.maxHealEntries != nil {
			heal, err := c.command(ctx, "volume", "heal", name, "info")
			if err != nil {
				return err
			}
			entries := glusterHealEntries(heal.Heals)
			observation.healEntries += entries
			if entries > *expectation.maxHealEntries {
				observation.issues = append(observation.issues, fmt.Sprintf("volume %s has %d pending self-heal entries (limit %d)", name, entries, *expectation.maxHealEntries))
			}
		}
		if expectation.maxSplitBrainEntries != nil {
			splitBrain, err := c.command(ctx, "volume", "heal", name, "info", "split-brain")
			if err != nil {
				return err
			}
			entries := glusterHealEntries(splitBrain.Heals)
			observation.splitBrainEntries += entries
			if entries > *expectation.maxSplitBrainEntries {
				observation.issues = append(observation.issues, fmt.Sprintf("volume %s has %d split-brain entries (limit %d)", name, entries, *expectation.maxSplitBrainEntries))
			}
		}
	}
	return nil
}

func glusterHealEntries(bricks []glusterHealBrickXML) int {
	entries := 0
	for _, brick := range bricks {
		entries += brick.NumberOfEntries
	}
	return entries
}

func (c glusterClusterCheck) resultFor(observation glusterClusterObservation, start time.Time) Result {
	issues := uniqueGlusterStrings(observation.issues)
	r := c.result(len(issues) == 0, "gluster cluster healthy", start)
	if len(issues) > 0 {
		r.Message = "gluster cluster: " + strings.Join(issues, "; ")
	}
	r.Data = map[string]any{
		DataKeyGlusterPeersConnected:    observation.connectedPeers,
		DataKeyGlusterPeersExpected:     observation.expectedPeers,
		DataKeyGlusterVolumesStarted:    observation.startedVolumes,
		DataKeyGlusterVolumesExpected:   observation.expectedVolumes,
		DataKeyGlusterBricksOnline:      observation.onlineBricks,
		DataKeyGlusterBricksExpected:    observation.expectedBricks,
		DataKeyGlusterSelfHealOnline:    observation.selfHealOnline,
		DataKeyGlusterSelfHealTotal:     observation.selfHealTotal,
		DataKeyGlusterHealEntries:       observation.healEntries,
		DataKeyGlusterSplitBrainEntries: observation.splitBrainEntries,
	}
	if len(observation.disconnectedPeers) > 0 {
		r.Data[DataKeyGlusterPeersDisconnected] = uniqueGlusterStrings(observation.disconnectedPeers)
	}
	if len(issues) > 0 {
		r.Data[DataKeyGlusterIssues] = issues
	}
	return r
}

func (c glusterClusterCheck) command(ctx context.Context, args ...string) (glusterCLIOutput, error) {
	argv := append([]string{glusterModeScriptFlag, glusterXMLFlag}, args...)
	result, runErr := c.runner.Run(ctx, glusterCommand, argv...)
	if result.ExitCode == execx.ExitCodeRunFailure {
		return glusterCLIOutput{}, errors.New(execx.OperatorFailureOr(runErr, result, c.timeout, execx.CommandDidNotStart))
	}
	if result.ExitCode != glusterCLIExitSuccess {
		detail := output.FirstNonEmptyLine(result.Stderr)
		if detail == "" && runErr != nil {
			detail = runErr.Error()
		}
		if detail == "" {
			detail = "no diagnostic output"
		}
		return glusterCLIOutput{}, fmt.Errorf("%s: exit %d: %s", strings.Join(args, " "), result.ExitCode, detail)
	}
	var response glusterCLIOutput
	if err := xml.Unmarshal([]byte(result.Stdout), &response); err != nil {
		return glusterCLIOutput{}, fmt.Errorf("%s: parse XML: %w", strings.Join(args, " "), err)
	}
	return response, response.validate(args)
}

func (response glusterCLIOutput) validate(args []string) error {
	opRet := strings.TrimSpace(response.OpRet)
	if opRet == "" {
		return fmt.Errorf("%s: XML response has no opRet", strings.Join(args, " "))
	}
	code, err := strconv.Atoi(opRet)
	if err != nil {
		return fmt.Errorf("%s: XML response has invalid opRet %q", strings.Join(args, " "), opRet)
	}
	if code == glusterCLIExitSuccess {
		return nil
	}
	detail := strings.TrimSpace(response.OpErrstr)
	if detail == "" {
		detail = "no diagnostic output"
	}
	return fmt.Errorf("%s: gluster error %d: %s", strings.Join(args, " "), code, detail)
}

func (peer glusterPeerXML) healthy() bool {
	return peer.Connected == glusterStatusOnline && (peer.State == glusterPeerStateMember || strings.EqualFold(strings.TrimSpace(peer.StateStr), glusterPeerInCluster))
}

func (peer glusterPeerXML) names() []string {
	return uniqueGlusterStrings(append([]string{peer.Hostname}, peer.Hostnames...))
}

func (peer glusterPeerXML) label() string {
	if name := strings.TrimSpace(peer.Hostname); name != "" {
		return name
	}
	for _, name := range peer.Hostnames {
		if name = strings.TrimSpace(name); name != "" {
			return name
		}
	}
	return "unknown"
}

func (volume glusterVolumeXML) started() bool {
	return volume.Status == glusterStatusOnline || strings.EqualFold(strings.TrimSpace(volume.StatusStr), glusterVolumeStarted)
}

func (node glusterVolumeNodeXML) label() string {
	if node.Path == "" {
		return node.Hostname
	}
	return node.Hostname + ":" + node.Path
}

func parseGlusterClusterConfig(entry map[string]any) ([]string, map[string]glusterVolumeExpectation, error) {
	peers, err := parseGlusterPeers(entry[CheckKeyPeers])
	if err != nil {
		return nil, nil, err
	}
	volumes, err := parseGlusterVolumes(entry[CheckKeyVolumes])
	if err != nil {
		return nil, nil, err
	}
	if len(peers) == 0 && len(volumes) == 0 {
		return nil, nil, fmt.Errorf("requires peers and/or volumes")
	}
	return peers, volumes, nil
}

func parseGlusterPeers(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	peers, err := cfgval.StrictStringArray(raw)
	if err != nil || len(peers) == 0 {
		return nil, fmt.Errorf("peers must be a non-empty list of names")
	}
	for i := range peers {
		peers[i] = strings.TrimSpace(peers[i])
		if peers[i] == "" {
			return nil, fmt.Errorf("peers must not contain empty names")
		}
	}
	normalized := uniqueGlusterStrings(peers)
	if len(normalized) != len(peers) {
		return nil, fmt.Errorf("peers must not contain duplicate names")
	}
	return normalized, nil
}

func parseGlusterVolumes(raw any) (map[string]glusterVolumeExpectation, error) {
	if raw == nil {
		return nil, nil
	}
	entries, ok := raw.(map[string]any)
	if !ok || len(entries) == 0 {
		return nil, fmt.Errorf("volumes must be a non-empty mapping")
	}
	volumes := make(map[string]glusterVolumeExpectation, len(entries))
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		if !IsGlusterVolumeName(name) {
			return nil, fmt.Errorf("volume %q has an unsafe name", name)
		}
		rawExpectation, ok := entries[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("volume %s must be a mapping", name)
		}
		for _, key := range slices.Sorted(maps.Keys(rawExpectation)) {
			if !IsGlusterClusterVolumeField(key) {
				return nil, fmt.Errorf("volume %s field %s is not supported", name, key)
			}
		}
		bricks, ok := cfgval.Int(rawExpectation[CheckKeyBricks])
		if !ok || bricks <= 0 {
			return nil, fmt.Errorf("volume %s requires positive bricks", name)
		}
		expectation := glusterVolumeExpectation{bricks: bricks}
		if rawSelfHeal, present := rawExpectation[CheckKeySelfHeal]; present {
			value, ok := rawSelfHeal.(bool)
			if !ok {
				return nil, fmt.Errorf("volume %s self_heal must be a boolean", name)
			}
			expectation.selfHeal = value
		}
		var err error
		if expectation.maxHealEntries, err = parseGlusterLimit(name, CheckKeyMaxHealEntries, rawExpectation); err != nil {
			return nil, err
		}
		if expectation.maxSplitBrainEntries, err = parseGlusterLimit(name, CheckKeyMaxSplitBrainEntries, rawExpectation); err != nil {
			return nil, err
		}
		volumes[name] = expectation
	}
	return volumes, nil
}

// IsGlusterClusterVolumeField reports whether key is accepted by one volume
// entry in a gluster_cluster check.
func IsGlusterClusterVolumeField(key string) bool {
	switch key {
	case CheckKeyBricks, CheckKeySelfHeal, CheckKeyMaxHealEntries, CheckKeyMaxSplitBrainEntries:
		return true
	default:
		return false
	}
}

func parseGlusterLimit(volume, key string, entry map[string]any) (*int, error) {
	raw, present := entry[key]
	if !present {
		return nil, nil
	}
	limit, ok := cfgval.Int(raw)
	if !ok || limit < 0 {
		return nil, fmt.Errorf("volume %s %s must be a non-negative integer", volume, key)
	}
	return &limit, nil
}

// IsGlusterVolumeName reports whether name is safe to pass as one argv element
// to the local Gluster CLI.
func IsGlusterVolumeName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func uniqueGlusterStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}
