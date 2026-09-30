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
	switch sub {
	case commandValidate:
		return a.runConfigValidate(rest, opts)
	default:
		return a.commandUsageError(commandConfig, fmt.Sprintf("unknown config subcommand %q", sub))
	}
}

func (a App) runConfigValidate(rest []string, opts options) int {
	if len(rest) > 0 {
		return a.commandUsageError(commandConfig, "config validate takes no service name; it validates the whole Sermo configuration")
	}

	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}

	issues := config.Validate(cfg)
	warnings := config.Warnings(cfg)

	if len(issues) == 0 {
		switch {
		case opts.json:
			out := map[string]any{cliJSONKeyValid: true}
			if len(warnings) > 0 {
				out[cliJSONKeyWarnings] = warningsJSON(warnings)
			}
			writeJSON(a.Stdout, out)
		case !opts.quiet:
			// A warning never fails validation: the configuration loads and
			// runs, just not the way its author likely meant.
			a.printWarnings(warnings)
			fmt.Fprintln(a.Stdout, cliTextOK)
		}
		return exitSuccess
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyValid: false, cliJSONKeyErrors: issuesJSON(issues), cliJSONKeyWarnings: warningsJSON(warnings)})
		return exitConfigInvalid
	}
	a.printWarnings(warnings)
	a.printIssues(opts, issues)
	return exitConfigInvalid
}

// printWarnings writes advisory findings in the ERROR format, labelled WARN.
func (a App) printWarnings(warnings []config.Issue) {
	for _, is := range warnings {
		fmt.Fprintf(a.Stderr, "%s %s:\n  %s\n", cliTextWarn, is.Scope, is.Msg)
	}
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

// warningsJSON renders advisory findings; a warning is not an error, so it
// carries a message rather than an error key.
func warningsJSON(warnings []config.Issue) []map[string]string {
	return issueRows(warnings, cliJSONKeyMessage)
}

func issuesJSON(issues []config.Issue) []map[string]string {
	return issueRows(issues, cliJSONKeyError)
}

// issueRows renders findings as {scope, <textKey>} rows.
func issueRows(issues []config.Issue, textKey string) []map[string]string {
	out := make([]map[string]string, 0, len(issues))
	for _, is := range issues {
		out = append(out, map[string]string{cliJSONKeyScope: is.Scope, textKey: is.Msg})
	}
	return out
}
