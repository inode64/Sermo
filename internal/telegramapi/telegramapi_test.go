package telegramapi

import "testing"

func TestURLMatchesPreRefactorSpelling(t *testing.T) {
	// The notifier used to build: base + token + "/sendMessage".
	const token = "123:abc"
	want := "https://api.telegram.org/bot" + token + "/sendMessage"
	if got := MethodURL(token, MethodSendMessage); got != want {
		t.Fatalf("MethodURL = %q, want %q", got, want)
	}
	// The bot client used: base + token + "/" + method.
	wantGet := "https://api.telegram.org/bot" + token + "/" + "getUpdates"
	if got := MethodURL(token, MethodGetUpdates); got != wantGet {
		t.Fatalf("MethodURL = %q, want %q", got, wantGet)
	}
}
