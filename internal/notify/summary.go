package notify

import (
	"fmt"
	"net/url"

	"sermo/internal/cfgval"
)

// ConfigSummary returns a non-secret destination hint for operator dashboards.
func ConfigSummary(typ string, entry map[string]any) string {
	switch typ {
	case TypeEmail:
		to := cfgval.StringList(entry[KeyTo])
		return listSummary(to, "")
	case TypeNtfy, TypeSlack, TypeTeams:
		// For ntfy this deliberately shows the server host only: the topic
		// acts as a capability and must stay off the dashboard.
		webhook := cfgval.AsString(entry[KeyWebhook])
		if webhook == "" {
			return ""
		}
		u, err := url.Parse(webhook)
		if err != nil || u.Host == "" {
			return ""
		}
		return u.Host
	case TypeTTY:
		users := cfgval.StringList(entry[KeyUsers])
		return listSummary(users, "all active terminals")
	case TypeWall:
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
