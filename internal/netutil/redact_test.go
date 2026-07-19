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
	}
	for in, want := range cases {
		if got := RedactURL(in); got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
	}
}
