package assist

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type mountAssistant struct{}

func (mountAssistant) Name() string { return "mount" }
func (mountAssistant) Title() string {
	return "Manage fstab-backed mount units"
}

func (mountAssistant) Run(p *Prompt, env Env) (res Result, err error) {
	defer Recover(&err)
	if env.Mounts == nil {
		return Result{}, fmt.Errorf("mount detection is unavailable")
	}
	cands, err := env.Mounts()
	if err != nil {
		return Result{}, fmt.Errorf("detect fstab mounts: %w", err)
	}
	cands = sortedMountCandidates(cands)
	if len(cands) == 0 {
		return Result{}, fmt.Errorf("no fstab mount points were detected on this host")
	}

	labels := make([]string, len(cands))
	for i, c := range cands {
		labels[i] = mountCandidateLabel(c)
	}
	sel := p.MultiChoose("Which fstab mount points do you want Sermo to manage?", labels)

	var shared *mountSettings
	if len(sel) > 1 && p.Confirm("Apply the same mount settings to all selected mount points?", true) {
		s := askMountSettings(p, "the selected mount points")
		shared = &s
	}

	mounts := map[string]any{}
	for _, idx := range sel {
		c := cands[idx]
		settings := shared
		if settings == nil {
			s := askMountSettings(p, c.Path)
			settings = &s
		}
		mounts[mountUnitName(c.Path)] = buildMountUnit(c, *settings)
	}
	return mountResult(mounts)
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
		"category": "storage",
		"path":     filepath.Clean(c.Path),
		"mount": map[string]any{
			"refcount": s.refcount,
			"umount": map[string]any{
				"allow_sigkill": false,
				"allow_lazy":    false,
			},
		},
	}
}

func mountResult(mounts map[string]any) (Result, error) {
	if len(mounts) == 0 {
		return Result{}, nil
	}
	return Result{
		Mounts:  mounts,
		Summary: resultSummary("mount unit", mounts),
	}, nil
}

func mountUnitName(path string) string {
	return watchName("mount", filepath.Clean(path))
}

func sortedMountCandidates(cands []MountCandidate) []MountCandidate {
	out := append([]MountCandidate(nil), cands...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Path < out[j].Path
	})
	return out
}

func mountCandidateLabel(c MountCandidate) string {
	state := "not mounted"
	if c.Mounted {
		state = "mounted"
	}
	parts := []string{c.Path}
	if c.FSType != "" || c.Source != "" {
		detail := strings.TrimSpace(strings.Join(nonEmpty(c.FSType, c.Source), " on "))
		if detail != "" {
			parts = append(parts, "("+detail+")")
		}
	}
	parts = append(parts, "["+state+"]")
	return strings.Join(parts, " ")
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
