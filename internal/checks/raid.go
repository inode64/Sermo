package checks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RaidMemberStatus is one md member's kernel-reported integrity state.
type RaidMemberStatus struct {
	Name      string
	State     string
	Errors    string
	BadBlocks string
}

// RaidArrayStatus is one Linux software-RAID array observation.
type RaidArrayStatus struct {
	Name          string
	Degraded      bool
	Recovering    bool
	Operation     string
	SyncAction    string
	ProgressPct   float64
	HasProgress   bool
	MismatchCount string
	Members       []RaidMemberStatus
}

// RaidStatus summarizes the Linux software-RAID (md) state.
type RaidStatus struct {
	Arrays        int
	Degraded      int
	Recovering    int
	DegradedNames []string
	Details       []RaidArrayStatus
}

// RaidTransition is one observed RAID lifecycle or sysfs change. It is exposed
// through Result.Data for the host-watch notification dispatcher.
type RaidTransition struct {
	Event       string
	Array       string
	Member      string
	Field       string
	Old         string
	New         string
	Operation   string
	Progress    float64
	HasProgress bool
}

// RAID notification event names. They are configuration values under
// then.notify_on for raid watches.
const (
	RaidNotifyOnDegraded    = "on_degraded"
	RaidNotifyOnRecovering  = "on_recovering"
	RaidNotifyOnGood        = "on_good"
	RaidNotifyOnArrayChange = "on_array_change"
)

// RaidNotifyEvents is the allowed `then.notify_on` vocabulary for raid watches.
var RaidNotifyEvents = []string{RaidNotifyOnDegraded, RaidNotifyOnRecovering, RaidNotifyOnGood, RaidNotifyOnArrayChange}

// RaidSamplerFunc reads the current md RAID status. Injected for tests; the
// default parses /proc/mdstat and the related read-only sysfs attributes.
type RaidSamplerFunc func() (RaidStatus, error)

// raidCheck reports the health of Linux md software-RAID arrays. With no predicate
// it is a condition check that alerts when any array is degraded; predicates on
// `degraded`/`recovering`/`arrays` override that. An optional `array` selector
// evaluates one named md device. (A host with no md arrays never alerts.)
type raidCheck struct {
	base
	sampler      RaidSamplerFunc
	preds        []levelPred
	array        string
	sysfsChanges bool
	previous     map[string]RaidArrayStatus
}

func (c *raidCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultRaidSampler
	}
	st, err := sampler()
	if err != nil {
		return c.result(false, "raid: "+err.Error(), start)
	}

	detail, present := raidArray(st, c.array)
	values := map[string]float64{
		fieldDegraded:   float64(st.Degraded),
		fieldRecovering: float64(st.Recovering),
		fieldArrays:     float64(st.Arrays),
	}
	if c.array != "" && present {
		values[fieldDegraded] = boolFloat(detail.Degraded)
		values[fieldRecovering] = boolFloat(detail.Recovering)
		values[fieldArrays] = 1
	}
	ok := st.Degraded > 0 // default alert condition
	if c.array != "" {
		ok = !present || detail.Degraded
	}
	if len(c.preds) > 0 {
		ok = !present || levelPredsHold(c.preds, values)
	}

	msg := raidMessage(st, c.array, detail, present)
	r := c.result(ok, msg, start)
	r.Data = raidResultData(st, c.array, detail, present)
	if c.previous == nil {
		c.previous = map[string]RaidArrayStatus{}
	} else {
		transitions := raidTransitions(c.previous, st.Details, c.array, c.sysfsChanges)
		if len(transitions) > 0 {
			r.Data[DataKeyRaidTransitions] = transitions
		}
	}
	c.previous = raidDetailsByName(st.Details)
	return r
}

func boolFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

func raidArray(st RaidStatus, name string) (RaidArrayStatus, bool) {
	if name == "" {
		return RaidArrayStatus{}, true
	}
	for _, detail := range st.Details {
		if detail.Name == name {
			return detail, true
		}
	}
	return RaidArrayStatus{}, false
}

func raidMessage(st RaidStatus, array string, detail RaidArrayStatus, present bool) string {
	if array != "" {
		if !present {
			return "raid " + array + ": array not found"
		}
		msg := fmt.Sprintf("raid %s: %s", array, raidArrayState(detail))
		if detail.Operation != "" {
			msg += " (" + detail.Operation
			if detail.HasProgress {
				msg += fmt.Sprintf(" %.1f%%", detail.ProgressPct)
			}
			msg += ")"
		}
		return msg
	}
	msg := fmt.Sprintf("raid: %d arrays, %d degraded, %d recovering", st.Arrays, st.Degraded, st.Recovering)
	if len(st.DegradedNames) > 0 {
		msg += " (" + strings.Join(st.DegradedNames, ", ") + ")"
	}
	return msg
}

func raidArrayState(detail RaidArrayStatus) string {
	if detail.Degraded {
		return "degraded"
	}
	return "good"
}

func raidResultData(st RaidStatus, array string, detail RaidArrayStatus, present bool) map[string]any {
	data := map[string]any{
		DataKeyArrays: st.Arrays, DataKeyDegraded: st.Degraded, DataKeyRecovering: st.Recovering,
		DataKeyRaidMembers: st.Details,
	}
	if len(st.DegradedNames) > 0 {
		data[DataKeyDegradedArrays] = strings.Join(st.DegradedNames, ",")
	}
	if array == "" {
		return data
	}
	data[DataKeyArray] = array
	data[DataKeyPresent] = present
	if !present {
		return data
	}
	data[DataKeyDegraded] = boolFloat(detail.Degraded)
	data[DataKeyRecovering] = boolFloat(detail.Recovering)
	data[DataKeyRaidOperation] = detail.Operation
	data[DataKeyRaidMismatchCount] = detail.MismatchCount
	if detail.HasProgress {
		data[DataKeyRaidProgressPct] = detail.ProgressPct
	}
	return data
}

// RaidTransitions returns the typed transition list from a RAID check result.
func RaidTransitions(result Result) []RaidTransition {
	transitions, _ := result.Data[DataKeyRaidTransitions].([]RaidTransition)
	return transitions
}

// SampleRaid returns one live md RAID observation using the default sampler.
func SampleRaid() (RaidStatus, error) { return defaultRaidSampler() }

// defaultRaidSampler reads mdstat, then enriches each discovered array with
// read-only sysfs member state. Missing sysfs data is normal on partial kernels.
func defaultRaidSampler() (RaidStatus, error) {
	b, err := os.ReadFile(procMDStatPath)
	if err != nil {
		if os.IsNotExist(err) {
			return RaidStatus{}, nil
		}
		return RaidStatus{}, err
	}
	st := parseMdstat(string(b))
	enrichRaidSysfs(&st, raidSysBlockPath)
	return st, nil
}

var (
	mdHeadRe      = regexp.MustCompile(`^(md\w+)\s*:`)
	mdRatioRe     = regexp.MustCompile(`\[(\d+)/(\d+)\]`)
	mdStatusRe    = regexp.MustCompile(`\[([U_]+)\]`)
	mdProgressRe  = regexp.MustCompile(`\b(recovery|resync|reshape|check)\s*=\s*([0-9]+(?:\.[0-9]+)?)%`)
	mdArrayNameRe = regexp.MustCompile(`^md[[:alnum:]_]+$`)
)

const (
	mdArrayNameGroup     = 1
	mdRatioTotalGroup    = 1
	mdRatioActiveGroup   = 2
	mdStatusMapGroup     = 1
	mdOperationGroup     = 1
	mdProgressValueGroup = 2
	mdMemberPrefix       = "dev-"
	raidSysBlockPath     = "/sys/block"
	raidSyncActionFile   = "sync_action"
	raidSyncActionIdle   = "idle"
	raidSyncActionResync = "resync"
)

// parseMdstat parses /proc/mdstat. An array is degraded when its active count
// is short or its [U_…] map has a down member. recovery/resync/reshape/check
// are reported as active operations; only the first three are reconstruction.
func parseMdstat(s string) RaidStatus {
	var st RaidStatus
	var cur RaidArrayStatus
	flush := func() {
		if cur.Name == "" {
			return
		}
		st.Arrays++
		if cur.Degraded {
			st.Degraded++
			st.DegradedNames = append(st.DegradedNames, cur.Name)
		}
		if cur.Recovering {
			st.Recovering++
		}
		st.Details = append(st.Details, cur)
		cur = RaidArrayStatus{}
	}
	for line := range strings.SplitSeq(s, checkLineSeparator) {
		trimmed := strings.TrimSpace(line)
		if h := mdHeadRe.FindStringSubmatch(trimmed); h != nil {
			flush()
			cur.Name = h[mdArrayNameGroup]
			continue
		}
		if cur.Name == "" {
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "unused devices") {
			flush()
			continue
		}
		if m := mdRatioRe.FindStringSubmatch(line); m != nil {
			total, _ := strconv.Atoi(m[mdRatioTotalGroup])
			active, _ := strconv.Atoi(m[mdRatioActiveGroup])
			if active < total {
				cur.Degraded = true
			}
		}
		if m := mdStatusRe.FindStringSubmatch(line); m != nil && strings.Contains(m[mdStatusMapGroup], "_") {
			cur.Degraded = true
		}
		if m := mdProgressRe.FindStringSubmatch(line); m != nil {
			cur.Operation = m[mdOperationGroup]
			cur.Recovering = true
			cur.ProgressPct, _ = strconv.ParseFloat(m[mdProgressValueGroup], 64)
			cur.HasProgress = true
		}
	}
	flush()
	return st
}

func enrichRaidSysfs(st *RaidStatus, root string) {
	if st == nil || len(st.Details) == 0 {
		return
	}
	for i := range st.Details {
		detail := &st.Details[i]
		mdPath := filepath.Join(root, detail.Name, "md")
		detail.SyncAction = readRaidSysfsValue(filepath.Join(mdPath, raidSyncActionFile))
		detail.MismatchCount = readRaidSysfsValue(filepath.Join(mdPath, "mismatch_cnt"))
		entries, err := os.ReadDir(mdPath)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), mdMemberPrefix) {
				continue
			}
			memberPath := filepath.Join(mdPath, entry.Name())
			detail.Members = append(detail.Members, RaidMemberStatus{
				Name: strings.TrimPrefix(entry.Name(), mdMemberPrefix), State: readRaidSysfsValue(filepath.Join(memberPath, "state")),
				Errors: readRaidSysfsValue(filepath.Join(memberPath, "errors")), BadBlocks: readRaidSysfsValue(filepath.Join(memberPath, "bad_blocks")),
			})
		}
		sort.Slice(detail.Members, func(i, j int) bool { return detail.Members[i].Name < detail.Members[j].Name })
	}
}

// SetRaidRebuildState pauses or resumes a Linux md reconstruction through its
// sync_action sysfs attribute. It accepts only a discovered md array name and
// checks the live state before writing, so callers cannot turn an arbitrary
// sysfs path into a write target.
func SetRaidRebuildState(ctx context.Context, array string, resume bool) (RaidArrayStatus, error) {
	return setRaidRebuildState(ctx, array, resume, raidSysBlockPath, SampleRaid)
}

func setRaidRebuildState(ctx context.Context, array string, resume bool, root string, sample RaidSamplerFunc) (RaidArrayStatus, error) {
	if !validRaidArrayName(array) {
		return RaidArrayStatus{}, fmt.Errorf("invalid RAID array %q", array)
	}
	if err := ctx.Err(); err != nil {
		return RaidArrayStatus{}, err
	}
	if sample == nil {
		sample = SampleRaid
	}
	status, err := sample()
	if err != nil {
		return RaidArrayStatus{}, fmt.Errorf("sample RAID %s: %w", array, err)
	}
	detail, present := raidArray(status, array)
	if !present {
		return RaidArrayStatus{}, fmt.Errorf("RAID array %q was not found", array)
	}
	if resume {
		if detail.SyncAction != raidSyncActionIdle {
			return RaidArrayStatus{}, fmt.Errorf("RAID array %q is not paused (sync_action=%q)", array, detail.SyncAction)
		}
	} else if !isRaidRebuild(detail.Operation) {
		return RaidArrayStatus{}, fmt.Errorf("RAID array %q is not reconstructing", array)
	}

	action := raidSyncActionIdle
	if resume {
		action = raidSyncActionResync
	}
	path := filepath.Join(root, array, "md", raidSyncActionFile)
	if err := os.WriteFile(path, []byte(action+"\n"), 0); err != nil {
		return RaidArrayStatus{}, fmt.Errorf("set RAID %s sync_action=%s: %w", array, action, err)
	}
	if err := ctx.Err(); err != nil {
		return RaidArrayStatus{}, err
	}
	verified, err := sample()
	if err != nil {
		return RaidArrayStatus{}, fmt.Errorf("verify RAID %s: %w", array, err)
	}
	detail, present = raidArray(verified, array)
	if !present {
		return RaidArrayStatus{}, fmt.Errorf("RAID array %q disappeared after setting sync_action", array)
	}
	if !resume && detail.SyncAction != raidSyncActionIdle {
		return RaidArrayStatus{}, fmt.Errorf("RAID array %q did not pause (sync_action=%q)", array, detail.SyncAction)
	}
	if resume && detail.SyncAction == raidSyncActionIdle {
		return RaidArrayStatus{}, fmt.Errorf("RAID array %q did not resume", array)
	}
	return detail, nil
}

func validRaidArrayName(array string) bool {
	return mdArrayNameRe.MatchString(array)
}

func readRaidSysfsValue(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func raidDetailsByName(details []RaidArrayStatus) map[string]RaidArrayStatus {
	out := make(map[string]RaidArrayStatus, len(details))
	for _, detail := range details {
		out[detail.Name] = detail
	}
	return out
}

func raidTransitions(previous map[string]RaidArrayStatus, current []RaidArrayStatus, array string, sysfsChanges bool) []RaidTransition {
	currentByName := raidDetailsByName(current)
	names := make([]string, 0, len(currentByName))
	for name := range currentByName {
		if array == "" || array == name {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var out []RaidTransition
	for _, name := range names {
		cur := currentByName[name]
		prev, seen := previous[name]
		if !seen {
			continue
		}
		for _, event := range raidStateTransitions(prev, cur) {
			out = append(out, RaidTransition{Event: event, Array: name, Operation: cur.Operation, Progress: cur.ProgressPct, HasProgress: cur.HasProgress})
		}
		if sysfsChanges {
			out = append(out, raidSysfsTransitions(prev, cur)...)
		}
	}
	return out
}

func raidStateTransitions(previous, current RaidArrayStatus) []string {
	var out []string
	if !previous.Degraded && current.Degraded {
		out = append(out, RaidNotifyOnDegraded)
	}
	if !isRaidRebuild(previous.Operation) && isRaidRebuild(current.Operation) {
		out = append(out, RaidNotifyOnRecovering)
	}
	if (previous.Degraded || isRaidRebuild(previous.Operation)) && !current.Degraded && !isRaidRebuild(current.Operation) {
		out = append(out, RaidNotifyOnGood)
	}
	return out
}

func isRaidRebuild(operation string) bool {
	switch operation {
	case "recovery", "resync", "reshape":
		return true
	default:
		return false
	}
}

func raidSysfsTransitions(previous, current RaidArrayStatus) []RaidTransition {
	var out []RaidTransition
	if previous.MismatchCount != current.MismatchCount {
		out = append(out, RaidTransition{Event: RaidNotifyOnArrayChange, Array: current.Name, Field: DataKeyRaidMismatchCount, Old: previous.MismatchCount, New: current.MismatchCount})
	}
	prevMembers := raidMembersByName(previous.Members)
	for _, member := range current.Members {
		prev, seen := prevMembers[member.Name]
		if !seen {
			out = append(out, RaidTransition{Event: RaidNotifyOnArrayChange, Array: current.Name, Member: member.Name, Field: "member", Old: "absent", New: "present"})
			continue
		}
		out = append(out, raidMemberTransitions(current.Name, prev, member)...)
		delete(prevMembers, member.Name)
	}
	for name := range prevMembers {
		out = append(out, RaidTransition{Event: RaidNotifyOnArrayChange, Array: current.Name, Member: name, Field: "member", Old: "present", New: "absent"})
	}
	return out
}

func raidMembersByName(members []RaidMemberStatus) map[string]RaidMemberStatus {
	out := make(map[string]RaidMemberStatus, len(members))
	for _, member := range members {
		out[member.Name] = member
	}
	return out
}

func raidMemberTransitions(array string, previous, current RaidMemberStatus) []RaidTransition {
	fields := []struct{ name, old, new string }{
		{"state", previous.State, current.State}, {"errors", previous.Errors, current.Errors}, {"bad_blocks", previous.BadBlocks, current.BadBlocks},
	}
	var out []RaidTransition
	for _, field := range fields {
		if field.old != field.new {
			out = append(out, RaidTransition{Event: RaidNotifyOnArrayChange, Array: array, Member: current.Name, Field: field.name, Old: field.old, New: field.new})
		}
	}
	return out
}
