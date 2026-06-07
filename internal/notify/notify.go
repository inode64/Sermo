// Package notify delivers notifications to named, typed senders ("notifiers")
// such as email, configured under the global `notifiers` section and referenced
// by name from a watch/check's `then.notify` list.
//
// New transports (slack, teams, …) plug in by registering a builder in the
// `builders` map keyed by `type`; the rest of the system addresses every
// transport uniformly through the Notifier interface. Keep this extensible:
// adding a transport must not require changes outside this package and the docs
// (see AGENTS.md "Notifications are pluggable").
package notify

import (
	"context"
	"fmt"
	"sort"
)

// Message is a notification to deliver. Subject/Body are the human-facing text;
// Fields carries the structured context (the SERMO_* key/values a hook would get)
// for future templating.
type Message struct {
	Subject string
	Body    string
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

// builders maps a notifier `type` to its constructor. Register new transports
// here (e.g. "slack", "teams").
var builders = map[string]func(name string, entry map[string]any) (Notifier, error){
	"email": buildEmail,
	"slack": buildSlack,
}

// Build constructs the named notifiers from the global `notifiers` section
// (raw == cfg.Global.Raw["notifiers"]). Malformed or unknown-type entries are
// skipped with a warning, mirroring BuildWorkers/BuildWatches.
func Build(raw map[string]any) (map[string]Notifier, []string) {
	out := map[string]Notifier{}
	if len(raw) == 0 {
		return out, nil
	}
	var warnings []string
	for _, name := range sortedKeys(raw) {
		entry, ok := raw[name].(map[string]any)
		if !ok {
			warnings = append(warnings, "notifier "+name+": not a mapping")
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
		out[name] = n
	}
	return out, warnings
}

// SupportedTypes lists the registered notifier types, for validation and docs.
func SupportedTypes() []string {
	types := make([]string, 0, len(builders))
	for t := range builders {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func stringList(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if t != "" {
			return []string{t}
		}
	}
	return nil
}
