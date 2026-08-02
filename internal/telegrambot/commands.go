package telegrambot

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// command is one read-only bot command.
type command struct {
	help    string
	handler func(ctx context.Context, b *Bot, args []string) (string, error)
}

// commands is the read-only command registry, mirroring sermoctl's dispatch
// table. Every handler only reads state through the Reporter.
var commands map[string]command

// The table is self-referential — it holds cmdHelp, which reads it back through
// helpText — so neither a package-level literal nor a lazily built var can
// express it: Go rejects both as an initialization cycle. init is exempt, which
// is exactly the case it exists for.
//
//nolint:gochecknoinits // self-referential dispatch table; a var initializer is an initialization cycle.
func init() {
	commands = map[string]command{
		"/status":   {help: "daemon and service health summary", handler: cmdStatus},
		"/services": {help: "list services, or detail one: /services [name]", handler: cmdServices},
		"/watches":  {help: "host and service watch states", handler: cmdWatches},
		"/sla":      {help: "availability windows for a service: /sla <service>", handler: cmdSLA},
		"/events":   {help: "recent events: /events [count]", handler: cmdEvents},
		"/help":     {help: "show this help", handler: cmdHelp},
	}
}

// dispatch parses one message and returns the reply text. An empty or unknown
// command yields help.
func (b *Bot) dispatch(ctx context.Context, text string) (string, error) {
	name, args := parseCommand(text)
	if name == "" {
		return helpText(), nil
	}
	cmd, ok := commands[name]
	if !ok {
		return "Unknown command " + name + ".\n\n" + helpText(), nil
	}
	return cmd.handler(ctx, b, args)
}

// parseCommand splits a message into a normalized command name and its
// arguments. A bot-mention suffix (/status@MyBot) is stripped, the name is
// lowercased, and a leading slash is ensured.
func parseCommand(text string) (name string, args []string) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", nil
	}
	name = fields[0]
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	name = strings.ToLower(name)
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	return name, fields[1:]
}

// reportCommand is the body of the argument-less report commands: read through
// the Reporter, name the command if that fails, otherwise render the reading.
func reportCommand[T any](ctx context.Context, name string, fetch func(context.Context) (T, error), format func(T) string) (string, error) {
	reading, err := fetch(ctx)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return format(reading), nil
}

func cmdStatus(ctx context.Context, b *Bot, _ []string) (string, error) {
	return reportCommand(ctx, "status", b.reporter.Status, formatStatus)
}

func cmdServices(ctx context.Context, b *Bot, args []string) (string, error) {
	lines, err := b.reporter.Services(ctx)
	if err != nil {
		return "", fmt.Errorf("services: %w", err)
	}
	if len(args) > 0 {
		name := args[0]
		for _, s := range lines {
			if strings.EqualFold(s.Name, name) {
				return formatServiceDetail(s), nil
			}
		}
		return noSuchService(name), nil
	}
	return formatServices(lines), nil
}

func cmdWatches(ctx context.Context, b *Bot, _ []string) (string, error) {
	return reportCommand(ctx, "watches", b.reporter.Watches, formatWatches)
}

func cmdSLA(ctx context.Context, b *Bot, args []string) (string, error) {
	if len(args) == 0 {
		return "Usage: /sla <service>", nil
	}
	service := args[0]
	windows, ok, err := b.reporter.SLA(ctx, service)
	if err != nil {
		return "", fmt.Errorf("sla %s: %w", service, err)
	}
	if !ok {
		return noSuchService(service), nil
	}
	return formatSLA(service, windows), nil
}

func cmdEvents(ctx context.Context, b *Bot, args []string) (string, error) {
	limit := EventsDefaultLimit
	if len(args) > 0 {
		if n, err := strconv.Atoi(args[0]); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > EventsMaxLimit {
		limit = EventsMaxLimit
	}
	lines, err := b.reporter.Events(ctx, limit)
	if err != nil {
		return "", fmt.Errorf("events: %w", err)
	}
	return formatEvents(lines), nil
}

// noSuchService is the one wording for a name that matches no configured
// service, shared by /services and /sla.
func noSuchService(name string) string { return fmt.Sprintf("No service named %q.", name) }

func cmdHelp(_ context.Context, _ *Bot, _ []string) (string, error) {
	return helpText(), nil
}

func helpText() string {
	var b strings.Builder
	b.WriteString("Sermo bot — read-only commands:\n")
	for _, name := range slices.Sorted(maps.Keys(commands)) {
		fmt.Fprintf(&b, "%s — %s\n", name, commands[name].help)
	}
	return trimReply(&b)
}
