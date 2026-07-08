package checks

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RaidStatus summarizes the Linux software-RAID (md) state.
type RaidStatus struct {
	Arrays        int
	Degraded      int
	Recovering    int
	DegradedNames []string
}

// RaidSamplerFunc reads the current md RAID status. Injected for tests; the
// default parses /proc/mdstat.
type RaidSamplerFunc func() (RaidStatus, error)

// raidCheck reports the health of Linux md software-RAID arrays. With no predicate
// it is a condition check that alerts when any array is degraded; predicates on
// `degraded`/`recovering`/`arrays` override that. (A host with no md arrays never
// alerts.)
type raidCheck struct {
	base
	sampler RaidSamplerFunc
	preds   []levelPred
}

func (c raidCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultRaidSampler
	}
	st, err := sampler()
	if err != nil {
		return c.result(false, "raid: "+err.Error(), start)
	}

	values := map[string]float64{
		fieldDegraded:   float64(st.Degraded),
		fieldRecovering: float64(st.Recovering),
		fieldArrays:     float64(st.Arrays),
	}
	ok := st.Degraded > 0 // default alert condition
	if len(c.preds) > 0 {
		ok = levelPredsHold(c.preds, values)
	}

	msg := fmt.Sprintf("raid: %d arrays, %d degraded, %d recovering", st.Arrays, st.Degraded, st.Recovering)
	if len(st.DegradedNames) > 0 {
		msg += " (" + strings.Join(st.DegradedNames, ", ") + ")"
	}
	r := c.result(ok, msg, start)
	r.Data = map[string]any{fieldArrays: st.Arrays, fieldDegraded: st.Degraded, fieldRecovering: st.Recovering}
	if len(st.DegradedNames) > 0 {
		r.Data[DataKeyDegradedArrays] = strings.Join(st.DegradedNames, ",")
	}
	return r
}

// SampleRaid returns one live md RAID observation using the default /proc/mdstat
// sampler.
func SampleRaid() (RaidStatus, error) { return defaultRaidSampler() }

// defaultRaidSampler parses /proc/mdstat (absent/empty -> no arrays).
func defaultRaidSampler() (RaidStatus, error) {
	b, err := os.ReadFile(procMDStatPath)
	if err != nil {
		if os.IsNotExist(err) {
			return RaidStatus{}, nil
		}
		return RaidStatus{}, err
	}
	return parseMdstat(string(b)), nil
}

var (
	mdHeadRe   = regexp.MustCompile(`^(md\w+)\s*:`)
	mdRatioRe  = regexp.MustCompile(`\[(\d+)/(\d+)\]`)
	mdStatusRe = regexp.MustCompile(`\[([U_]+)\]`)
)

const (
	mdArrayNameGroup   = 1
	mdRatioTotalGroup  = 1
	mdRatioActiveGroup = 2
	mdStatusMapGroup   = 1
)

// parseMdstat parses /proc/mdstat: an array (a line "mdN : …" and the indented
// lines that follow it) is degraded when its [n/m] active count is short or its
// [U_…] member map has a down member ('_'); it is recovering when its block
// mentions recovery/resync/reshape/check.
func parseMdstat(s string) RaidStatus {
	var st RaidStatus
	var cur string
	var degraded, recovering bool
	flush := func() {
		if cur == "" {
			return
		}
		st.Arrays++
		if degraded {
			st.Degraded++
			st.DegradedNames = append(st.DegradedNames, cur)
		}
		if recovering {
			st.Recovering++
		}
		cur, degraded, recovering = "", false, false
	}
	for _, l := range strings.Split(s, checkLineSeparator) {
		trimmed := strings.TrimSpace(l)
		if h := mdHeadRe.FindStringSubmatch(trimmed); h != nil {
			flush()
			cur = h[mdArrayNameGroup]
			continue
		}
		if cur == "" {
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "unused devices") {
			flush()
			continue
		}
		if m := mdRatioRe.FindStringSubmatch(l); m != nil {
			total, _ := strconv.Atoi(m[mdRatioTotalGroup])
			active, _ := strconv.Atoi(m[mdRatioActiveGroup])
			if active < total {
				degraded = true
			}
		}
		if m := mdStatusRe.FindStringSubmatch(l); m != nil && strings.Contains(m[mdStatusMapGroup], "_") {
			degraded = true
		}
		if strings.Contains(l, "recovery") || strings.Contains(l, "resync") ||
			strings.Contains(l, "reshape") || strings.Contains(l, "check =") {
			recovering = true
		}
	}
	flush()
	return st
}

// parseRaidPreds reads the optional degraded/recovering/arrays predicates.
