package cli

import (
	"fmt"

	"sermo/internal/config"
)

// runConfig dispatches the `config` subcommands.
func (a App) runConfig(opts options) int {
	if len(opts.args) == 0 {
		return a.commandUsageError(commandConfig, "config requires a subcommand (validate)")
	}

	sub := opts.args[0]
	rest := opts.args[1:]
	globalPath := opts.globalPath()

	switch sub {
	case commandValidate:
		return a.runConfigValidate(globalPath, rest, opts)
	default:
		return a.commandUsageError(commandConfig, fmt.Sprintf("unknown config subcommand %q", sub))
	}
}

func (a App) runConfigValidate(globalPath string, rest []string, opts options) int {
	if len(rest) > 0 {
		return a.commandUsageError(commandConfig, "config validate takes no service name; it validates the whole Sermo configuration")
	}

	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("load config failed: %v", err))
	}

	issues := config.Validate(cfg)

	if len(issues) == 0 {
		switch {
		case opts.json:
			writeJSON(a.Stdout, map[string]any{cliJSONKeyValid: true})
		case !opts.quiet:
			fmt.Fprintln(a.Stdout, cliTextOK)
		}
		return exitSuccess
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyValid: false, cliJSONKeyErrors: issuesJSON(issues)})
	} else {
		a.printIssues(opts, issues)
	}
	return exitConfigInvalid
}

// printIssues writes validation findings in the section-30 ERROR format.
func (a App) printIssues(opts options, issues []config.Issue) {
	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyValid: false, cliJSONKeyErrors: issuesJSON(issues)})
		return
	}
	for _, is := range issues {
		fmt.Fprintf(a.Stderr, "ERROR %s:\n  %s\n", is.Scope, is.Msg)
	}
}

func scopedIssues(scope string, msgs []string) []config.Issue {
	issues := make([]config.Issue, 0, len(msgs))
	for _, m := range msgs {
		issues = append(issues, config.Issue{Scope: scope, Msg: m})
	}
	return issues
}

func issuesJSON(issues []config.Issue) []map[string]string {
	out := make([]map[string]string, 0, len(issues))
	for _, is := range issues {
		out = append(out, map[string]string{cliJSONKeyScope: is.Scope, cliJSONKeyError: is.Msg})
	}
	return out
}
