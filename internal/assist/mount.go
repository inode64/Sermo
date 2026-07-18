package assist

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"sermo/internal/checks"
	"sermo/internal/config"
)

type mountAssistant struct{}

const (
	mountCandidateStateMounted    = "mounted"
	mountCandidateStateNotMounted = "not mounted"
	mountSourceDetailSeparator    = " on "
)

func (mountAssistant) Name() string { return AssistantNameMount }
func (mountAssistant) Title() string {
	return "Manage fstab-backed mount units"
}

func (mountAssistant) Run(p *Prompt, env Env) (res Result, err error) {
	defer Recover(&err)
	if env.Mounts == nil {
		return Result{}, errors.New("mount detection is unavailable")
	}
	cands, err := env.Mounts()
	if err != nil {
		return Result{}, fmt.Errorf("detect fstab mounts: %w", err)
	}
	cands = sortedMountCandidates(cands)
	if len(cands) == 0 {
		return Result{}, errors.New("no fstab mount points were detected on this host")
	}

	selected, shared := chooseSharedSettings(p,
		"Which fstab mount points do you want Sermo to manage?", cands, mountCandidateLabel,
		"Apply the same mount settings to all selected mount points?", "the selected mount points",
		func(label string) mountSettings { return askMountSettings(p, label) })

	mounts := map[string]any{}
	forEachWithSettings(selected, shared,
		func(c MountCandidate) mountSettings { return askMountSettings(p, c.Path) },
		func(c MountCandidate, s mountSettings) { mounts[mountUnitName(c.Path)] = buildMountUnit(c, s) })
	return mountResult(mounts), nil
}

type mountSettings struct {
	refcount bool
}

func askMountSettings(p *Prompt, label string) mountSettings {
	return mountSettings{
		refcount: p.Confirm("Use refcounted mount/umount for "+label+"?", true),
	}
}

func buildMountUnit(c MountCandidate, s mountSettings) map[string]any {
	return map[string]any{
		config.EntryKeyCategory: config.WatchCategoryStorage,
		config.WatchKeyCheck: map[string]any{
			checks.CheckKeyType:    checks.CheckTypeStorage,
			checks.CheckKeyPath:    filepath.Clean(c.Path),
			checks.CheckKeyMounted: true,
		},
		config.StorageKeyMount: map[string]any{
			config.MountKeyRefcount: s.refcount,
		},
	}
}

func mountResult(mounts map[string]any) Result {
	if len(mounts) == 0 {
		return Result{}
	}
	return Result{
		Mounts:  mounts,
		Summary: resultSummary("mount unit", mounts),
	}
}

func mountUnitName(path string) string {
	return watchName(AssistantNameMount, filepath.Clean(path))
}

func sortedMountCandidates(cands []MountCandidate) []MountCandidate {
	out := append([]MountCandidate(nil), cands...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Path < out[j].Path
	})
	return out
}

func mountCandidateLabel(c MountCandidate) string {
	state := mountCandidateStateNotMounted
	if c.Mounted {
		state = mountCandidateStateMounted
	}
	parts := []string{c.Path}
	if detail := mountSourceDetail(c); detail != "" {
		parts = append(parts, "("+detail+")")
	}
	parts = append(parts, "["+state+"]")
	return strings.Join(parts, " ")
}

func mountSourceDetail(c MountCandidate) string {
	return strings.TrimSpace(strings.Join(nonEmpty(c.FSType, c.Source), mountSourceDetailSeparator))
}
