package cli

import (
	"fmt"
	"io"
	"text/tabwriter"
)

type commandUsage struct {
	Name     string
	Summary  string
	Usage    []string
	Flags    []string
	Notes    []string
	Examples []string
}

type commandGroup struct {
	Title    string
	Commands []string
}

var commandGroups = []commandGroup{
	{
		Title:    "Basics",
		Commands: []string{commandVersion, commandBackend, commandStatus, commandIsActive, commandWatch},
	},
	{
		Title:    "Safe Service Operations",
		Commands: []string{commandStart, commandStop, commandRestart, commandReload, commandResume, commandMonitor, commandUnmonitor, commandPreflight, commandProcesses, commandLocks, commandLock},
	},
	{
		Title:    "Mounts",
		Commands: []string{commandMount, commandUmount},
	},
	{
		Title:    "Configuration And Catalog",
		Commands: []string{commandConfig, commandDaemon, commandServices, commandApps, commandLibs, commandPatterns, commandWizard},
	},
	{
		Title:    "History And State",
		Commands: []string{commandEvents, commandActivity, commandSLA, commandState},
	},
	{
		Title:    "Emergency",
		Commands: []string{commandPanic},
	},
}

var commandUsages = []commandUsage{
	{
		Name:    commandHelp,
		Summary: "Show global help or detailed help for one command.",
		Usage: []string{
			"sermoctl help",
			"sermoctl help COMMAND",
			"sermoctl COMMAND --help",
		},
		Examples: []string{
			"sermoctl help restart",
			"sermoctl services --help",
		},
	},
	{
		Name:    commandVersion,
		Summary: "Print the sermoctl version.",
		Usage: []string{
			"sermoctl version",
			"sermoctl --version",
			"sermoctl -V",
		},
	},
	{
		Name:    commandBackend,
		Summary: "Detect and print the active service-manager backend.",
		Usage: []string{
			"sermoctl backend",
		},
		Flags: []string{
			"--backend auto|systemd|openrc  backend to probe; default is auto",
			"--json                         print {\"backend\": \"...\"}",
		},
		Examples: []string{
			"sermoctl backend",
			"SERMO_BACKEND=openrc sermoctl backend",
		},
	},
	{
		Name:    commandStatus,
		Summary: "Show one service's resolved runtime state.",
		Usage: []string{
			"sermoctl status SERVICE",
		},
		Flags: []string{
			"--json  print the service state as JSON",
		},
		Notes: []string{
			"When sermod is running with web enabled, status prefers the daemon's",
			"computed state (including starting during startup settling). Otherwise",
			"it reflects the init backend and local monitor metadata only.",
		},
		Examples: []string{
			"sermoctl status nginx-main",
			"sermoctl --json status mysql-main",
		},
	},
	{
		Name:    commandWatch,
		Summary: "Query or pause/resume a watch (host watch or service watch).",
		Usage: []string{
			"sermoctl watch status WATCH",
			"sermoctl watch monitor WATCH",
			"sermoctl watch unmonitor WATCH",
		},
		Flags: []string{
			"--json  print the result as JSON",
		},
		Notes: []string{
			"When sermod is running with web enabled, watch status prefers the",
			"daemon's computed state (including starting during startup settling).",
			"Otherwise it reports ok.",
			"monitor/unmonitor pause or resume a single watch, persisted under",
			"paths.state and read live by the daemon. WATCH is a host watch name or",
			"a service watch \"<service>:<watch>\"; a watch's monitor state is",
			"independent of its service's.",
		},
		Examples: []string{
			"sermoctl watch status storage-root",
			"sermoctl --json watch status load",
			"sermoctl watch unmonitor mail-queue:deferred-backlog",
		},
	},
	{
		Name:    commandIsActive,
		Summary: "Exit 0 when a service is active, 1 when it is not active.",
		Usage: []string{
			"sermoctl is-active SERVICE",
		},
		Notes: []string{
			"Probes the init backend only (active/inactive/paused), not the daemon's",
			"computed state. Use status when you need starting/settling visibility.",
			"Paused monitoring counts as not active for scripting purposes.",
		},
		Examples: []string{
			"sermoctl is-active redis-cache",
		},
	},
	{
		Name:    commandStart,
		Summary: "Start a service through the safe operation engine.",
		Usage: []string{
			"sermoctl start SERVICE [--no-cascade]",
		},
		Flags: []string{
			"--no-cascade  operate only the named service",
		},
		Notes: []string{
			"Start honors guards, preflight checks, locks and operation timeouts.",
		},
		Examples: []string{
			"sermoctl start apache-main",
			"sermoctl start docker --no-cascade",
		},
	},
	{
		Name:    commandStop,
		Summary: "Stop a service through the safe operation engine.",
		Usage: []string{
			"sermoctl stop SERVICE [--no-cascade]",
		},
		Flags: []string{
			"--no-cascade  operate only the named service",
		},
		Notes: []string{
			"Stop uses the resolved stop policy and does not send SIGKILL unless the service explicitly allows it.",
		},
		Examples: []string{
			"sermoctl stop postgres-main",
		},
	},
	{
		Name:    commandRestart,
		Summary: "Restart a service through the safe operation engine.",
		Usage: []string{
			"sermoctl restart SERVICE [--no-cascade]",
		},
		Flags: []string{
			"--no-cascade  operate only the named service",
		},
		Notes: []string{
			"Manual restarts are not remediation-rate-limited, but still honor guards, preflight and locks.",
		},
		Examples: []string{
			"sermoctl restart nginx-main",
		},
	},
	{
		Name:    commandReload,
		Summary: "Reload one service in place.",
		Usage: []string{
			"sermoctl reload SERVICE",
		},
		Notes: []string{
			"Use `sermoctl daemon reload` to make sermod reload Sermo configuration.",
		},
		Examples: []string{
			"sermoctl reload haproxy-main",
			"sermoctl daemon reload",
		},
	},
	{
		Name:    commandResume,
		Summary: "Resume a paused service target through the safe operation engine.",
		Usage: []string{
			"sermoctl resume SERVICE",
		},
		Examples: []string{
			"sermoctl resume vm-web01",
		},
	},
	{
		Name:    commandMonitor,
		Summary: "Resume daemon monitoring for a service.",
		Usage: []string{
			"sermoctl monitor SERVICE",
		},
		Examples: []string{
			"sermoctl monitor mysql-main",
		},
	},
	{
		Name:    commandUnmonitor,
		Summary: "Pause daemon monitoring for a service without removing config.",
		Usage: []string{
			"sermoctl unmonitor SERVICE",
		},
		Notes: []string{
			"Manual operations such as start, stop, restart, reload and resume remain available.",
		},
		Examples: []string{
			"sermoctl unmonitor mysql-main",
		},
	},
	{
		Name:    commandPanic,
		Summary: "Enable, disable or show the daemon-wide panic mode.",
		Usage: []string{
			"sermoctl panic on|off|status",
		},
		Notes: []string{
			"Panic mode keeps monitoring running but suspends hooks, alerts and automatic remediation across the daemon.",
			"Manual operations (start, stop, restart, reload, resume) remain available.",
			"The flag is persisted, so it survives daemon restarts until turned off.",
		},
		Examples: []string{
			"sermoctl panic on",
			"sermoctl panic status",
			"sermoctl panic off",
		},
	},
	{
		Name:    commandPreflight,
		Summary: "Run a service's preflight checks without changing service state.",
		Usage: []string{
			"sermoctl preflight SERVICE",
		},
		Examples: []string{
			"sermoctl preflight mysql-main",
		},
	},
	{
		Name:    commandProcesses,
		Summary: "Show processes matched by a service's resolved process selectors.",
		Usage: []string{
			"sermoctl processes SERVICE",
		},
		Flags: []string{
			"--json  print discovered processes as JSON",
		},
		Examples: []string{
			"sermoctl processes nginx-main",
		},
	},
	{
		Name:    commandLocks,
		Summary: "List named runtime locks for a service.",
		Usage: []string{
			"sermoctl locks SERVICE",
		},
		Flags: []string{
			"--json   print locks as JSON",
			"--quiet  suppress the empty-locks message",
		},
		Examples: []string{
			"sermoctl locks mysql-main",
		},
	},
	{
		Name:    commandLock,
		Summary: "Acquire, release or hold a named runtime lock around a command.",
		Usage: []string{
			"sermoctl lock SERVICE [--name NAME] --reason REASON --ttl DURATION -- COMMAND...",
			"sermoctl lock acquire SERVICE [--name NAME] --reason REASON --ttl DURATION",
			"sermoctl lock release SERVICE [--name NAME]",
		},
		Flags: []string{
			"--name NAME        optional lock name",
			"--reason REASON    required reason stored with the lock",
			"--ttl DURATION     required lock lifetime",
		},
		Examples: []string{
			"sermoctl lock mysql-main --reason backup --ttl 2h -- /usr/local/bin/backup-mysql",
			"sermoctl lock acquire postgres-main --name maintenance --reason patch --ttl 30m",
			"sermoctl lock release postgres-main --name maintenance",
		},
	},
	{
		Name:    commandMount,
		Summary: "Acquire or inspect a configured fstab-backed mount.",
		Usage: []string{
			"sermoctl mount TARGET",
			"sermoctl mount status TARGET",
			"sermoctl mount list",
		},
		Notes: []string{
			"TARGET is a configured mount name or an absolute path with an /etc/fstab entry.",
		},
		Examples: []string{
			"sermoctl mount mount-backup",
			"sermoctl mount /mnt/backup",
			"sermoctl mount status mount-backup",
		},
	},
	{
		Name:    commandUmount,
		Summary: "Release a configured fstab-backed mount.",
		Usage: []string{
			"sermoctl umount TARGET",
		},
		Examples: []string{
			"sermoctl umount mount-backup",
		},
	},
	{
		Name:    commandConfig,
		Summary: "Validate Sermo configuration.",
		Usage: []string{
			"sermoctl config validate",
		},
		Examples: []string{
			"sermoctl config validate",
		},
	},
	{
		Name:    commandDaemon,
		Summary: "Reload the running sermod process.",
		Usage: []string{
			"sermoctl daemon reload",
		},
		Notes: []string{
			"`daemon reload` reloads sermod configuration; it does not reload an application service.",
		},
		Examples: []string{
			"sermoctl daemon reload",
		},
	},
	{
		Name:    commandServices,
		Summary: "List packaged service catalog entries and installation status.",
		Usage: []string{
			"sermoctl services [all] [--long] [--notify NAME[,NAME]|all]",
		},
		Flags: []string{
			"all       include entries whose binary is not installed",
			"--long    show full version command output",
			"--notify  send an HTML report through selected configured notifiers",
		},
		Notes: []string{
			"Lists catalog service profiles under catalog/services, not the configured",
			"runtime services that sermod monitors. For live configured services use",
			"the web UI Services panel (GET /api/services) or the YAML under",
			"paths.services; for one service's state use status or is-active.",
		},
		Examples: []string{
			"sermoctl services",
			"sermoctl services all --long",
			"sermoctl services --notify ops-email",
			"sermoctl services all --notify all",
		},
	},
	{
		Name:    commandApps,
		Summary: "List packaged application/runtime catalog entries.",
		Usage: []string{
			"sermoctl apps [all] [--long]",
		},
		Flags: []string{
			"all     include entries whose binary is not installed",
			"--long  show full version command output",
		},
		Notes: []string{
			"When sermod is running with web enabled, the STATUS column prefers the",
			"daemon's computed state (including starting during startup settling).",
		},
	},
	{
		Name:    commandLibs,
		Summary: "List packaged library catalog entries used by restart-on-change.",
		Usage: []string{
			"sermoctl libs [all] [--long]",
		},
		Flags: []string{
			"all     include entries whose file is not present",
			"--long  show full version command output",
		},
	},
	{
		Name:    commandPatterns,
		Summary: "List output-analysis pattern sets and rule counts.",
		Usage: []string{
			"sermoctl patterns",
		},
	},
	{
		Name:    commandWizard,
		Summary: "Generate service, watch and mount configuration interactively.",
		Usage: []string{
			"sermoctl wizard",
			"sermoctl wizard service|docker|vm|mount|volume|net|uplink",
		},
		Examples: []string{
			"sermoctl wizard service",
			"sermoctl wizard volume",
		},
	},
	{
		Name:    commandEvents,
		Summary: "List or clear recent daemon events through the web API.",
		Usage: []string{
			"sermoctl events [SERVICE] [--limit N]",
			"sermoctl events clear [--before TIME]",
		},
		Flags: []string{
			"--limit N       maximum events to fetch",
			"--before TIME   RFC3339 timestamp or duration such as 2h",
		},
		Examples: []string{
			"sermoctl events mysql-main --limit 20",
			"sermoctl events clear --before 24h",
		},
	},
	{
		Name:    commandActivity,
		Summary: "Clear the same event log shown as Recent activity in the web UI.",
		Usage: []string{
			"sermoctl activity clear [--before TIME]",
		},
		Flags: []string{
			"--before TIME  RFC3339 timestamp or duration such as 2h",
		},
	},
	{
		Name:    commandSLA,
		Summary: "Report service availability windows or per-minute series.",
		Usage: []string{
			"sermoctl sla [SERVICE]",
			"sermoctl sla --series SERVICE [--since DURATION]",
		},
		Flags: []string{
			"--series           print per-minute series for one service",
			"--since DURATION   series lookback; default is 24h",
		},
		Examples: []string{
			"sermoctl sla",
			"sermoctl sla apache-main",
			"sermoctl sla --series apache-main --since 168h",
		},
	},
	{
		Name:    commandState,
		Summary: "Prune old history and vacuum the persistent state database.",
		Usage: []string{
			"sermoctl state compact [--before TIME]",
		},
		Flags: []string{
			"--before TIME  RFC3339 timestamp or duration; omitted means normal retention",
		},
		Examples: []string{
			"sermoctl state compact",
			"sermoctl state compact --before 720h",
		},
	},
}

func runHelp(a App, opts options) int {
	switch len(opts.args) {
	case 0:
		writeUsage(a.Stdout)
		return exitSuccess
	case 1:
		if writeCommandUsage(a.Stdout, opts.args[0]) {
			return exitSuccess
		}
		return a.commandUsageError(commandHelp, fmt.Sprintf("unknown help topic %q", opts.args[0]))
	default:
		return a.commandUsageError(commandHelp, "help accepts at most one command")
	}
}

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, "Sermo operator CLI")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  sermoctl [GLOBAL FLAGS] COMMAND [ARGS]")
	fmt.Fprintln(w, "  sermoctl help [COMMAND]")
	fmt.Fprintln(w, "  sermoctl COMMAND --help")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global Flags:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  --config PATH\tconfig file (default /etc/sermo/sermo.yml)")
	fmt.Fprintln(tw, "  --backend auto|systemd|openrc\tservice-manager backend; default is auto")
	fmt.Fprintln(tw, "  --json\tmachine-readable output where supported")
	fmt.Fprintln(tw, "  --quiet, -q\tsuppress non-essential text where supported")
	fmt.Fprintln(tw, "  --timeout DURATION\touter command timeout")
	fmt.Fprintln(tw, "  --version, -V\tprint version and exit")
	_ = tw.Flush()
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, group := range commandGroups {
		fmt.Fprintf(w, "  %s:\n", group.Title)
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, name := range group.Commands {
			help, _ := lookupCommandUsage(name)
			fmt.Fprintf(tw, "    %s\t%s\n", help.Name, help.Summary)
		}
		_ = tw.Flush()
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Use `sermoctl help COMMAND` for command-specific usage and examples.")
}

func writeCommandUsage(w io.Writer, topic string) bool {
	help, ok := lookupCommandUsage(topic)
	if !ok {
		return false
	}
	fmt.Fprintf(w, "Command: sermoctl %s\n", help.Name)
	fmt.Fprintln(w)
	fmt.Fprintln(w, help.Summary)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	for _, line := range help.Usage {
		fmt.Fprintf(w, "  %s\n", line)
	}
	if len(help.Flags) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Command Flags And Arguments:")
		for _, line := range help.Flags {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
	if len(help.Notes) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Notes:")
		for _, line := range help.Notes {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
	if len(help.Examples) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Examples:")
		for _, line := range help.Examples {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global flags may be placed before or after the command. Use `sermoctl help` to list them.")
	return true
}

func lookupCommandUsage(topic string) (commandUsage, bool) {
	for _, help := range commandUsages {
		if help.Name == topic {
			return help, true
		}
	}
	return commandUsage{}, false
}

func (a App) commandUsageError(command, msg string) int {
	fmt.Fprintln(a.Stderr, "usage error: "+msg)
	if !writeCommandUsage(a.Stderr, command) {
		writeUsage(a.Stderr)
	}
	return exitUsage
}
