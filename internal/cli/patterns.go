package cli

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/config"
	"sermo/internal/rules"
	"sermo/internal/strutil"
)

// runPatterns lists the output-analysis pattern sets the configured services
// use in `analyze.use`: each set's name, its rule count, the services using it
// and its description. `patterns catalog` lists every known set (catalog/patterns)
// instead. Unlike apps/libs it probes no binary, so it is a bespoke lister rather
// than appinspect.List.
func (a App) runPatterns(opts options) int {
	catalog := len(opts.args) == 1 && opts.args[0] == commandArgCatalog
	if len(opts.args) > 0 && !catalog {
		return a.commandUsageError(commandPatterns, commandPatterns+" accepts only optional `"+commandArgCatalog+"`")
	}
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}

	type setReport struct {
		Name        string   `json:"name"`
		Rules       int      `json:"rules"`
		UsedBy      []string `json:"used_by,omitempty"`
		Description string   `json:"description,omitempty"`
	}

	usedBy := a.patternSetUsers(opts, cfg)
	names := strutil.SortedUnique(cfg.PatternNames)
	var reports []setReport
	for _, name := range names {
		doc := cfg.Patterns[name]
		if doc == nil || (!catalog && len(usedBy[name]) == 0) {
			continue
		}
		ruleList, _ := doc.Body[rules.SectionRules].([]any)
		reports = append(reports, setReport{
			Name:        name,
			Rules:       len(ruleList),
			UsedBy:      usedBy[name],
			Description: cfgval.AsString(doc.Body[config.EntryKeyDescription]),
		})
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyPatterns: reports})
		return exitSuccess
	}
	if len(reports) == 0 {
		if catalog {
			fmt.Fprintln(a.Stdout, "no pattern sets")
		} else {
			fmt.Fprintln(a.Stdout, "no pattern sets in use")
		}
		return exitSuccess
	}
	tw := newTabWriter(a.Stdout)
	fmt.Fprintln(tw, "PATTERNS\tRULES\tUSED BY\tDESCRIPTION")
	for _, r := range reports {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", r.Name, r.Rules, cmp.Or(strings.Join(r.UsedBy, ","), "-"), r.Description)
	}
	_ = tw.Flush()
	return exitSuccess
}

// patternSetUsers maps each pattern set to the configured services whose
// checks use it, sorted by name. A service that does not resolve is skipped
// with a warning: its pattern sets cannot be known.
func (a App) patternSetUsers(opts options, cfg *config.Config) map[string][]string {
	users := map[string][]string{}
	for _, res := range cfg.ResolveServices(cfg.ServiceNames) {
		if len(res.Errors) > 0 {
			if !opts.quiet {
				fmt.Fprintf(a.Stderr, cliWarningFormat, "service "+res.Resolved.Name+": config resolve failed: "+res.Errors[0])
			}
			continue
		}
		for _, set := range res.Resolved.PatternSets {
			users[set] = append(users[set], res.Resolved.Name)
		}
	}
	for _, services := range users {
		slices.Sort(services)
	}
	return users
}
