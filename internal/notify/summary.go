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
		to := cfgval.StringList(entry["to"])
		if len(to) == 0 {
			return ""
		}
		if len(to) == 1 {
			return to[0]
		}
		return fmt.Sprintf("%s (+%d)", to[0], len(to)-1)
	case notifierTypeSlack, notifierTypeTeams:
		webhook := cfgval.AsString(entry["webhook"])
		if webhook == "" {
			return ""
		}
		u, err := url.Parse(webhook)
		if err != nil || u.Host == "" {
			return ""
		}
		return u.Host
	case notifierTypeTTY:
		users := cfgval.StringList(entry["users"])
		if len(users) == 0 {
			return "all active terminals"
		}
		if len(users) == 1 {
			return users[0]
		}
		return fmt.Sprintf("%s (+%d)", users[0], len(users)-1)
	case notifierTypeWall:
		return "all active terminals"
	default:
		return ""
	}
}
