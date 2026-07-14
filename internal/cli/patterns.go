package cli

import (
	"fmt"
	"slices"
	"text/tabwriter"

	"sermo/internal/cfgval"
	"sermo/internal/config"
	"sermo/internal/rules"
)

// runPatterns lists the output-analysis pattern sets (catalog/patterns): each
// set's name, its rule count, and its description. Unlike apps/libs it probes no
// binary, so it is a bespoke lister rather than appinspect.List.
func (a App) runPatterns(opts options) int {
	if len(opts.args) > 0 {
		return a.commandUsageError(commandPatterns, "patterns takes no arguments")
	}
	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}

	type setReport struct {
		Name        string `json:"name"`
		Rules       int    `json:"rules"`
		Description string `json:"description,omitempty"`
	}

	names := slices.Clone(cfg.PatternNames)
	slices.Sort(names)
	var reports []setReport
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		doc := cfg.Patterns[name]
		if doc == nil {
			continue
		}
		ruleList, _ := doc.Body[rules.SectionRules].([]any)
		reports = append(reports, setReport{
			Name:        name,
			Rules:       len(ruleList),
			Description: cfgval.AsString(doc.Body[config.EntryKeyDescription]),
		})
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyPatterns: reports})
		return exitSuccess
	}
	if len(reports) == 0 {
		fmt.Fprintln(a.Stdout, "no pattern sets")
		return exitSuccess
	}
	tw := tabwriter.NewWriter(a.Stdout, 0, 0, tabwriterPadding, ' ', 0)
	fmt.Fprintln(tw, "PATTERNS\tRULES\tDESCRIPTION")
	for _, r := range reports {
		fmt.Fprintf(tw, "%s\t%d\t%s\n", r.Name, r.Rules, r.Description)
	}
	_ = tw.Flush()
	return exitSuccess
}
