package notify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"sermo/internal/severity"
)

func TestPanelLinkOpensTheMessagesTarget(t *testing.T) {
	const base = "http://fr5.intranet:9797"
	tests := []struct {
		fields map[string]string
		want   string
	}{
		{map[string]string{FieldService: "nginx", FieldWatch: "port"}, base + "/#svc:nginx"},
		{map[string]string{FieldApp: "php8.4"}, base + "/#app:php8.4"},
		{map[string]string{FieldWatch: "exim:tidy-retry-db"}, base + "/#wat:exim:tidy-retry-db"},
		{map[string]string{FieldWatch: "disk root"}, base + "/#wat:disk%20root"},
		{nil, base + "/"},
	}
	for _, tt := range tests {
		if got := PanelLink(base, tt.fields); got != tt.want {
			t.Errorf("PanelLink(%v) = %q, want %q", tt.fields, got, tt.want)
		}
	}
	if got := PanelLink("", map[string]string{FieldService: "nginx"}); got != "" {
		t.Errorf("PanelLink without a base = %q, want none", got)
	}
}

// The link wrapper sits inside the severity floor, keeps it working, and hands
// templates the link as SERMO_URL.
func TestPanelLinkNotifierSetsLinkAndField(t *testing.T) {
	inner := &recordingNotifier{}
	var n Notifier = severityFloorNotifier{Notifier: panelLinkNotifier{Notifier: inner, base: "http://h:9797"}, floor: severity.Error}
	if !Accepts(n, severity.Critical) || Accepts(n, severity.Warning) {
		t.Fatal("the floor must stay visible through the link wrapper")
	}
	if err := n.Send(context.Background(), Message{Subject: "x", Fields: map[string]string{FieldWatch: "disk-root"}}); err != nil {
		t.Fatal(err)
	}
	got := inner.msg
	if got.Link != "http://h:9797/#wat:disk-root" || got.Fields[FieldURL] != got.Link {
		t.Fatalf("sent = %+v, want the link and SERMO_URL", got)
	}
}

func TestBuildWithPanelURLLinksEveryNotifier(t *testing.T) {
	built, warnings := Build(map[string]any{
		"chat": map[string]any{"type": "slack", "webhook": "https://hooks.slack.com/services/T/B/x", "min_severity": "error"},
	}, WithPanelURL("http://kvm1.vpn:9797/"))
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	floor, ok := built["chat"].(severityFloorNotifier)
	if !ok {
		t.Fatalf("notifier = %T, want the severity floor outermost", built["chat"])
	}
	if link, ok := floor.Notifier.(panelLinkNotifier); !ok || link.base != "http://kvm1.vpn:9797" {
		t.Fatalf("inner = %#v, want the panel link wrapper with a trimmed base", floor.Notifier)
	}
}

func slackAttachment(t *testing.T, msg Message) map[string]any {
	t.Helper()
	var body struct {
		Attachments []map[string]any `json:"attachments"`
	}
	if err := json.Unmarshal(slackPayload(msg), &body); err != nil || len(body.Attachments) != 1 {
		t.Fatalf("slack payload = %v (%v), want one attachment", body, err)
	}
	return body.Attachments[0]
}

// A Slack channel reads the grade from the attachment bar; a recovery reads
// green whatever grade the incident reached.
func TestSlackColoursBySeverityAndLinksTheTitle(t *testing.T) {
	critical := slackAttachment(t, Message{Subject: "[sermo][critical] web: down", Severity: severity.Critical, Link: "http://h/#svc:web"})
	if critical["color"] != toneColors[toneCritical] || critical["title_link"] != "http://h/#svc:web" {
		t.Fatalf("critical attachment = %v", critical)
	}
	warning := slackAttachment(t, Message{Subject: "[sermo][warning] disk", Severity: severity.Warning})
	if warning["color"] != toneColors[toneWarning] {
		t.Fatalf("warning colour = %v", warning["color"])
	}
	if _, linked := warning["title_link"]; linked {
		t.Fatal("a message without a link must not carry title_link")
	}
	recovered := slackAttachment(t, Message{Subject: "recovered", Severity: severity.Critical, Fields: map[string]string{FieldEvent: "recovered"}})
	if recovered["color"] != toneColors[toneRecovered] {
		t.Fatalf("recovery colour = %v", recovered["color"])
	}
	if unset := slackAttachment(t, Message{Subject: "x"}); unset["color"] != toneColors[toneError] {
		t.Fatalf("an ungraded message counts as an error, colour = %v", unset["color"])
	}
}

func TestTeamsColoursTheLeadLineAndLinksThePanel(t *testing.T) {
	card := teamsCard(Message{Subject: "s", Severity: severity.Warning, Link: "http://h/#wat:x"})
	body := card[teamsCardBodyKey].([]map[string]any)
	if body[0][teamsTextColorKey] != "warning" {
		t.Fatalf("lead line = %v, want the warning colour", body[0])
	}
	actions, ok := card[teamsCardActionsKey].([]map[string]any)
	if !ok || actions[0][teamsActionURLKey] != "http://h/#wat:x" {
		t.Fatalf("actions = %v, want an OpenUrl to the panel", card[teamsCardActionsKey])
	}
}

func TestTelegramMarksSeverityAndEscapesTheLink(t *testing.T) {
	msg := Message{Subject: "[sermo] disk", Severity: severity.Warning, Link: "http://kvm1.vpn:9797/#wat:disk-root"}
	if got := telegramText(msg, ""); got != "🟡 [sermo] disk\nhttp://kvm1.vpn:9797/#wat:disk-root" {
		t.Fatalf("plain text = %q", got)
	}
	if got := telegramText(msg, "MarkdownV2"); !strings.HasSuffix(got, `http://kvm1\.vpn:9797/\#wat:disk\-root`) {
		t.Fatalf("MarkdownV2 link must be escaped: %q", got)
	}
}

func TestPushTransportsCarrySeverityPriorityAndClick(t *testing.T) {
	msg := Message{Subject: "s", Body: "b", Severity: severity.Critical, Link: "http://h/#svc:web"}
	var ntfy map[string]any
	if err := json.Unmarshal(ntfyPayload("alerts", msg), &ntfy); err != nil {
		t.Fatal(err)
	}
	if ntfy["priority"] != float64(5) || ntfy["click"] != "http://h/#svc:web" {
		t.Fatalf("ntfy = %v, want max priority and the click link", ntfy)
	}
	var gotify map[string]any
	if err := json.Unmarshal(gotifyPayload(Message{Subject: "s", Severity: severity.Info, Link: "http://h/"}), &gotify); err != nil {
		t.Fatal(err)
	}
	click := gotify["extras"].(map[string]any)["client::notification"].(map[string]any)["click"].(map[string]any)
	if gotify["priority"] != float64(4) || click["url"] != "http://h/" {
		t.Fatalf("gotify = %v, want info priority and the click link", gotify)
	}
}

func TestEmailBodyEndsWithThePanelLink(t *testing.T) {
	if got := emailTextBody(Message{Body: "detail", Link: "http://h/#svc:web"}); got != "detail\n\nOpen in Sermo: http://h/#svc:web" {
		t.Fatalf("body = %q", got)
	}
	if got := emailTextBody(Message{Body: "detail"}); got != "detail" {
		t.Fatalf("body without a link = %q", got)
	}
}
