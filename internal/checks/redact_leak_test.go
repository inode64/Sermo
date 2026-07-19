package checks

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A failed HTTP check must never surface URL-embedded credentials: the message
// goes to the persisted event log and the guest-readable web API.
func TestHTTPCheckRedactsCredentialsOnFailure(t *testing.T) {
	// A connection-refused target on a URL with basic-auth and a query token.
	c := httpCheck{
		base:   base{name: "h", timeout: 200 * time.Millisecond},
		client: &http.Client{Timeout: 200 * time.Millisecond},
		url:    "https://monitor:s3cret@127.0.0.1:1/health?access_token=TOKENVALUE",
		method: "GET",
		expect: statusMatcher{codes: []int{200}},
	}
	res := c.Run(context.Background())
	if res.OK {
		t.Fatal("expected the check to fail (connection refused)")
	}
	for _, secret := range []string{"s3cret", "TOKENVALUE", "access_token"} {
		if strings.Contains(res.Message, secret) {
			t.Fatalf("message leaked %q: %s", secret, res.Message)
		}
	}
}
