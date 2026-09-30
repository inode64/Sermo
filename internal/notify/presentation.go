package notify

import (
	"context"
	"maps"
	"net/url"
	"strings"

	"sermo/internal/severity"
)

// Context keys a message's Fields carry that shape how it is presented: the
// target a panel link opens, and whether the message closes an incident.
const (
	FieldService = "SERMO_SERVICE"
	FieldApp     = "SERMO_APP"
	FieldWatch   = "SERMO_WATCH"
	FieldEvent   = "SERMO_EVENT"
	// FieldURL is the panel link, for templates.
	FieldURL = "SERMO_URL"
	// eventRecovered is the FieldEvent value of a recovery.
	eventRecovered = "recovered"
)

// Fragment prefixes the dashboard opens a target's row from (#svc:NAME).
const (
	panelFragmentService = "svc:"
	panelFragmentApp     = "app:"
	panelFragmentWatch   = "wat:"
)

// panelLinkLabel is the text a transport shows for the panel link.
const panelLinkLabel = "Open in Sermo"

// WithPanelURL makes every built notifier link its messages to the dashboard
// at base (web.public_url): a message about a service, app or watch opens that
// row. An empty base adds no link.
func WithPanelURL(base string) Option {
	return func(o *buildOptions) {
		o.panelURL = strings.TrimRight(base, "/")
	}
}

// PanelLink returns the dashboard link for the target a message's fields name
// — its service, else its app, else its watch — or "" when base is empty or
// the message names none.
func PanelLink(base string, fields map[string]string) string {
	if base == "" {
		return ""
	}
	for _, target := range [...]struct{ field, prefix string }{
		{FieldService, panelFragmentService},
		{FieldApp, panelFragmentApp},
		{FieldWatch, panelFragmentWatch},
	} {
		if name := fields[target.field]; name != "" {
			return base + "/#" + url.PathEscape(target.prefix+name)
		}
	}
	return base + "/"
}

// panelLinkNotifier sets the panel link on each message it sends. It sits
// outside the template wrapper, so a template can render ${SERMO_URL}.
type panelLinkNotifier struct {
	Notifier
	base string
}

func (n panelLinkNotifier) Send(ctx context.Context, msg Message) error {
	if msg.Link == "" {
		msg.Link = PanelLink(n.base, msg.Fields)
	}
	if msg.Link != "" {
		fields := make(map[string]string, len(msg.Fields))
		maps.Copy(fields, msg.Fields)
		fields[FieldURL] = msg.Link
		msg.Fields = fields
	}
	//nolint:wrapcheck // a transparent decorator: the inner notifier's error already names it.
	return n.Notifier.Send(ctx, msg)
}

// recovery reports a message that closes an incident; transports paint it as
// good news whatever grade the incident reached.
func (m Message) recovery() bool {
	return m.Fields[FieldEvent] == eventRecovered
}

// tone is how a transport colours a message: its severity, or recovered.
type tone int

const (
	toneDebug tone = iota
	toneInfo
	toneWarning
	toneError
	toneCritical
	toneRecovered
)

func (m Message) tone() tone {
	if m.recovery() {
		return toneRecovered
	}
	switch m.Severity.Resolved() {
	case severity.Debug:
		return toneDebug
	case severity.Info:
		return toneInfo
	case severity.Warning:
		return toneWarning
	case severity.Critical:
		return toneCritical
	default:
		return toneError
	}
}

// toneColors are the bar colours of a Slack attachment, matching the
// dashboard's severity palette.
var toneColors = map[tone]string{
	toneDebug:     "#9e9e9e",
	toneInfo:      "#1e88e5",
	toneWarning:   "#f9a825",
	toneError:     "#e53935",
	toneCritical:  "#8e24aa",
	toneRecovered: "#43a047",
}

// toneMarks lead a plain-text message (Telegram) with a coloured symbol, the
// one colour a chat line can carry.
var toneMarks = map[tone]string{
	toneDebug:     "⚪",
	toneInfo:      "🔵",
	toneWarning:   "🟡",
	toneError:     "🔴",
	toneCritical:  "🟣",
	toneRecovered: "🟢",
}

// toneTeamsColors are the Adaptive Card TextBlock colours.
var toneTeamsColors = map[tone]string{
	toneDebug:     "default",
	toneInfo:      "accent",
	toneWarning:   "warning",
	toneError:     "attention",
	toneCritical:  "attention",
	toneRecovered: "good",
}

// Push priorities on ntfy's scale; Gotify's 0..10 scale doubles them.
const (
	priorityMin = iota + 1
	priorityLow
	priorityDefault
	priorityHigh
	priorityMax
)

// tonePriorities set how loudly a push client announces a message.
var tonePriorities = map[tone]int{
	toneDebug:     priorityMin,
	toneInfo:      priorityLow,
	toneWarning:   priorityDefault,
	toneError:     priorityHigh,
	toneCritical:  priorityMax,
	toneRecovered: priorityDefault,
}
