package httpx

import (
	"net/http"
	"testing"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCloneDefaultTransportFallsBackForCustomRoundTripper(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, nil })

	if transport := CloneDefaultTransport(); transport == nil {
		t.Fatal("CloneDefaultTransport() = nil")
	}
}
