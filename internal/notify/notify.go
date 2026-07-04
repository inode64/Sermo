// Package notify delivers notifications to named, typed senders ("notifiers")
// such as email, configured under the global `notifiers` section and referenced
// by name from a watch/check's `then.notify` list.
//
// New transports (slack, teams, …) plug in by registering a builder in the
// `builders` map keyed by `type`; the rest of the system addresses every
// transport uniformly through the Notifier interface. Keep this extensible:
// adding a transport must not require changes outside this package and the docs
// (see AGENTS.md "Central builders" — Notifiers).
package notify

import (
	"context"
	"fmt"
	"maps"
	"sermo/internal/cfgval"
	"slices"
)

// Message is a notification to deliver. Subject/Body are the human-facing text;
// HTML optionally carries a rich email body. Non-email transports ignore HTML and
// use Body. Fields carries the structured context (the SERMO_* key/values a hook
// would get) for future templating.
type Message struct {
	Subject string
	Body    string
	HTML    string
	Fields  map[string]string
}

// Notifier is one configured delivery target. Implementations are safe to call
// concurrently only if their docs say so; the daemon dispatches sequentially per
// watch cycle.
type Notifier interface {
	Name() string
	Type() string
	Send(ctx context.Context, msg Message) error
}

// Option customizes notifier construction.
type Option func(*buildOptions)

type buildOptions struct {
	templateDir       string
	templatesDisabled bool
}

const (
	notifierTypeEmail = "email"
	notifierTypeSlack = "slack"
	notifierTypeTeams = "teams"
	notifierTypeTTY   = "tty"
	notifierTypeWall  = "wall"
)

// WithTemplateDir configures where named notification templates are loaded
// from.
func WithTemplateDir(dir string) Option {
	return func(o *buildOptions) {
		o.templateDir = dir
	}
}

// WithoutTemplates disables notifier-level templates during construction. This
// is useful for ad-hoc CLI reports that render their own complete body.
func WithoutTemplates() Option {
	return func(o *buildOptions) {
		o.templatesDisabled = true
	}
}

// Enabled reports whether a notifier config entry should be active — the
// inverse of the shared cfgval.Disabled opt-out reading (omitted `enabled`
// defaults to true; schema validation reports non-boolean values).
func Enabled(entry map[string]any) bool {
	return !cfgval.Disabled(entry)
}

// builders maps a notifier `type` to its constructor. Register new transports
// here (e.g. a future "discord").
var builders = map[string]func(name string, entry map[string]any) (Notifier, error){
	notifierTypeEmail: buildEmail,
	notifierTypeSlack: buildSlack,
	notifierTypeTeams: buildTeams,
	notifierTypeTTY:   buildTTY,
	notifierTypeWall:  buildWall,
}

// Build constructs global notifiers. Malformed or unknown-type entries become
// warnings, not fatal errors.
func Build(raw map[string]any, opts ...Option) (map[string]Notifier, []string) {
	var options buildOptions
	for _, opt := range opts {
		opt(&options)
	}
	out := map[string]Notifier{}
	if len(raw) == 0 {
		return out, nil
	}
	var warnings []string
	for _, name := range slices.Sorted(maps.Keys(raw)) {
		entry, ok := raw[name].(map[string]any)
		if !ok {
			warnings = append(warnings, "notifier "+name+": not a mapping")
			continue
		}
		if !Enabled(entry) {
			continue
		}
		typ, _ := entry["type"].(string)
		build, ok := builders[typ]
		if !ok {
			warnings = append(warnings, fmt.Sprintf("notifier %s: unsupported type %q", name, typ))
			continue
		}
		n, err := build(name, entry)
		if err != nil {
			warnings = append(warnings, "notifier "+name+": "+err.Error())
			continue
		}
		if templateName := cfgval.AsString(entry["template"]); templateName != "" && !options.templatesDisabled {
			tmpl, err := LoadTemplate(options.templateDir, templateName)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("notifier %s: template %q: %v", name, templateName, err))
				continue
			}
			n = WithTemplate(n, tmpl)
		}
		out[name] = n
	}
	return out, warnings
}

// SupportedTypes lists the registered notifier types, for validation and docs.
func SupportedTypes() []string {
	return slices.Sorted(maps.Keys(builders))
}
