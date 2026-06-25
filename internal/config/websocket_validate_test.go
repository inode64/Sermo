package config

import "testing"

func TestValidateWebsocketCheck(t *testing.T) {
	for _, u := range []string{"ws://h/s", "wss://h:8443/s", "http://h/s", "https://h/s"} {
		issues := validateService(t, "name: w\nservice: x\nchecks:\n  ws: { type: websocket, url: \""+u+"\" }\n")
		for _, is := range issues {
			if hasIssue([]Issue{is}, "checks.ws") {
				t.Fatalf("url %q must be valid: %v", u, issues)
			}
		}
	}

	mustHave(t, validateService(t, `
name: w
service: x
checks:
  ws: { type: websocket }
`), "url is required")

	mustHave(t, validateService(t, `
name: w
service: x
checks:
  ws: { type: websocket, url: "ftp://h/x" }
`), "scheme must be ws")
}
