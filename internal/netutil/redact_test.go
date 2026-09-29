package netutil

import "testing"

func TestRedactURLScenarios(t *testing.T) {
	cases := map[string]string{
		"https://monitor:secret@api.internal/health":      "https://monitor:xxxxx@api.internal/health",
		"https://api.internal/health?access_token=SECRET": "https://api.internal/health",
		"wss://host/ws?access_token=SECRET":               "wss://host/ws",
		"smtp://ops:p%ssw0rd@host:587":                    "smtp://ops:xxxxx@host:587",
		"smtp://ops:plain@host:587":                       "smtp://ops:xxxxx@host:587",
		"https://api.internal/ok":                         "https://api.internal/ok",
		// Unencoded delimiters in the password must not leak any of it.
		"smtp://u:pa/ss@h":            "smtp://u:xxxxx@h",
		"https://admin:p ss@h":        "https://admin:xxxxx@h",
		"https://admin:s3cr?et@h":     "https://admin:xxxxx@h",
		"https://u:123/abc@host/path": "https://u:xxxxx@host/path",
		"https://u:p#w@host/":         "https://u:xxxxx@host/",
		"https://u:p@ss@host/":        "https://u:xxxxx@host/",
		// '@' outside userinfo is not a credential.
		"https://mastodon.social/@ops":    "https://mastodon.social/@ops",
		"https://ops@host:8080/p@x":       "https://ops@host:8080/p@x",
		"https://api/p?next=http://u:p@x": "https://api/p",
	}
	for in, want := range cases {
		if got := RedactURL(in); got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
	}
}
