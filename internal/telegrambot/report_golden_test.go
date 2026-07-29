package telegrambot

import "testing"

// The reply text is the bot's entire user interface, and only one assertion
// covered it, so a formatting change could ship unnoticed. These golden strings
// pin what each command renders, including the fallbacks a partly-populated
// report falls back to.
func TestFormattersRenderGoldenReplies(t *testing.T) {
	svcs := []ServiceLine{
		{Name: "db", State: "monitored", Health: "ok", Monitored: true},
		{Name: "bare", Monitored: false},
	}
	watches := []WatchLine{
		{Name: "w1", Scope: "host", State: "ok", Monitored: true},
		{Name: "w2"},
	}
	events := []EventLine{
		{Time: "12:00", Target: "db", Kind: "action", Message: "restarted"},
		{Time: "12:01", Message: "bare"},
	}
	windows := []SLAWindow{{Window: "hour", Ratio: "99.9%"}}
	status := StatusReport{
		Host: "h", Services: 3, OK: 2, Failing: 1, Monitored: 3,
		Errors: 4, LastEvent: "restart", HostUptime: "3d",
	}

	for _, tc := range []struct{ name, got, want string }{
		{"status", formatStatus(status),
			"Sermo status — h\nServices: 3 (ok 2, failing 1)\nMonitoring: 3 monitored, 0 paused\n" +
				"Recent errors: 4\nLast event: restart\nHost uptime: 3d"},
		{"services", formatServices(svcs),
			"Services (2):\n- db: monitored / ok\n- bare: ? / unknown [not monitored]"},
		{"service detail", formatServiceDetail(svcs[1]),
			"bare\nState: ?\nHealth: unknown\nMonitoring: not monitored"},
		{"watches", formatWatches(watches),
			"Watches (2):\n- w1 (host): ok\n- w2 (?): ? [not monitored]"},
		{"events", formatEvents(events),
			"Recent events (2):\n- 12:00 [action] db: restarted\n- 12:01 [?]: bare"},
		{"sla", formatSLA("db", windows), "SLA — db\n- hour: 99.9%"},
		{"services empty", formatServices(nil), "No services configured."},
		{"watches empty", formatWatches(nil), "No watches configured."},
		{"events empty", formatEvents(nil), "No recent events."},
		{"sla empty", formatSLA("db", nil), "No SLA data for db."},
	} {
		if tc.got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, tc.got, tc.want)
		}
	}
}
