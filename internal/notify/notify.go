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
	"errors"
	"fmt"
	"maps"
	"sermo/internal/cfgval"
	"slices"
	"strings"
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

// Config key constants name fields inside one notifier configuration entry.
const (
	KeyDSN             = "dsn"
	KeyFrom            = "from"
	KeyTemplate        = "template"
	KeyTo              = "to"
	KeyChatID          = "chat_id"
	KeyToken           = "token"
	KeyType            = "type"
	KeyUsers           = "users"
	KeyWebhook         = "webhook"
	KeyParseMode       = "parse_mode"
	KeySilent          = "silent"
	KeyMessageThreadID = "message_thread_id"
)

// Option customizes notifier construction.
type Option func(*buildOptions)

type buildOptions struct {
	templateDir       string
	templatesDisabled bool
}

// Type constants are the supported notifier transport names.
const (
	TypeEmail    = "email"
	TypeGotify   = "gotify"
	TypeNtfy     = "ntfy"
	TypeSlack    = "slack"
	TypeTeams    = "teams"
	TypeTelegram = "telegram"
	TypeTTY      = "tty"
	TypeWall     = "wall"
)

const (
	notifyCRLF = "\r\n"
	notifyCR   = "\r"
	notifyLF   = "\n"
	notifySP   = " "
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

// transports maps each notifier type to its constructor and field validator.
// Register new transports here (e.g. a future "discord").
type transport struct {
	build    func(name string, entry map[string]any) (Notifier, error)
	validate func(entry map[string]any) []ValidationIssue
}

var transports = map[string]transport{
	TypeEmail:    {build: buildEmail, validate: validateEmailConfig},
	TypeGotify:   {build: buildGotify, validate: validateGotifyConfig},
	TypeNtfy:     {build: buildNtfy, validate: validateNtfyConfig},
	TypeSlack:    {build: buildSlack, validate: validateWebhookConfig(TypeSlack)},
	TypeTeams:    {build: buildTeams, validate: validateWebhookConfig(TypeTeams)},
	TypeTelegram: {build: buildTelegram, validate: validateTelegramConfig},
	TypeTTY:      {build: buildTTY, validate: validateTTYConfig},
	TypeWall:     {build: buildWall, validate: validateWallConfig},
}

// notifierWarning renders one build warning as "notifier <name>: <problem>", so
// every entry Build rejects names the notifier it concerns the same way.
func notifierWarning(name, problem string) string {
	return "notifier " + name + ": " + problem
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
			warnings = append(warnings, notifierWarning(name, "not a mapping"))
			continue
		}
		if !Enabled(entry) {
			continue
		}
		issues := ValidateEntry(entry)
		if len(issues) > 0 {
			warnings = append(warnings, notifierWarning(name, issues[0].Field+issues[0].Suffix))
			continue
		}
		typ := cfgval.String(entry[KeyType])
		registered, ok := transports[typ]
		if !ok {
			warnings = append(warnings, notifierWarning(name, fmt.Sprintf("unsupported type %q", typ)))
			continue
		}
		n, err := registered.build(name, entry)
		if err != nil {
			warnings = append(warnings, notifierWarning(name, err.Error()))
			continue
		}
		if templateName := cfgval.AsString(entry[KeyTemplate]); templateName != "" && !options.templatesDisabled {
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

// PreferWall removes TTY notifiers when the same delivery set contains a wall
// notifier. Wall already targets every active terminal, so sending through both
// transports would duplicate the local-console message.
func PreferWall(notifiers []Notifier) []Notifier {
	hasWall := false
	for _, notifier := range notifiers {
		if notifier.Type() == TypeWall {
			hasWall = true
			break
		}
	}
	if !hasWall {
		return notifiers
	}
	selected := make([]Notifier, 0, len(notifiers))
	for _, notifier := range notifiers {
		if notifier.Type() != TypeTTY {
			selected = append(selected, notifier)
		}
	}
	return selected
}

// NewTargetedTTY constructs an ad-hoc TTY notifier for an explicit set of
// users. An empty effective user set is rejected because an unfiltered TTY
// notifier targets every active terminal session.
func NewTargetedTTY(name string, users []string) (Notifier, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("targeted tty notifier requires a name")
	}
	seen := make(map[string]struct{}, len(users))
	values := make([]any, 0, len(users))
	for _, user := range users {
		user = strings.TrimSpace(user)
		if user == "" {
			continue
		}
		if _, ok := seen[user]; ok {
			continue
		}
		seen[user] = struct{}{}
		values = append(values, user)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("targeted tty notifier %s requires at least one user", name)
	}
	notifier, err := buildTTY(name, map[string]any{KeyUsers: values})
	if err != nil {
		return nil, fmt.Errorf("build targeted tty notifier %s: %w", name, err)
	}
	return notifier, nil
}

// SupportedTypes lists the registered notifier types, for validation and docs.
func SupportedTypes() []string {
	return slices.Sorted(maps.Keys(transports))
}
