package notify

import (
	"fmt"
	"net/url"

	"sermo/internal/cfgval"
)

// ConfigSummary returns a non-secret destination hint for operator dashboards.
func ConfigSummary(typ string, entry map[string]any) string {
	switch typ {
	case notifierTypeEmail:
		to := cfgval.StringList(entry[keyTo])
		return listSummary(to, "")
	case notifierTypeSlack, notifierTypeTeams:
		webhook := cfgval.AsString(entry[keyWebhook])
		if webhook == "" {
			return ""
		}
		u, err := url.Parse(webhook)
		if err != nil || u.Host == "" {
			return ""
		}
		return u.Host
	case notifierTypeTTY:
		users := cfgval.StringList(entry[keyUsers])
		return listSummary(users, "all active terminals")
	case notifierTypeWall:
		return "all active terminals"
	default:
		return ""
	}
}

func listSummary(values []string, empty string) string {
	switch len(values) {
	case 0:
		return empty
	case 1:
		return values[0]
	default:
		return fmt.Sprintf("%s (+%d)", values[0], len(values)-1)
	}
}
