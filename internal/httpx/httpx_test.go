package httpx

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSuccessStatus(t *testing.T) {
	tests := []struct {
		code int
		want bool
	}{
		{code: http.StatusOK, want: true},
		{code: http.StatusCreated, want: true},
		{code: http.StatusNoContent, want: true},
		{code: http.StatusContinue, want: false},
		{code: http.StatusMovedPermanently, want: false},
		{code: http.StatusBadRequest, want: false},
		{code: 0, want: false},
	}
	for _, tc := range tests {
		if got := SuccessStatus(tc.code); got != tc.want {
			t.Errorf("SuccessStatus(%d) = %v, want %v", tc.code, got, tc.want)
		}
	}
}

func TestErrorBody(t *testing.T) {
	if got := ErrorBody(nil, ErrorBodyLimit); got != "" {
		t.Fatalf("ErrorBody(nil) = %q", got)
	}
	resp := &http.Response{Body: io.NopCloser(strings.NewReader("  remote failed  "))}
	if got := ErrorBody(resp, ErrorBodyLimit); got != "remote failed" {
		t.Fatalf("ErrorBody = %q", got)
	}
	long := &http.Response{Body: io.NopCloser(strings.NewReader(strings.Repeat("a", ErrorBodyLimit+8)))}
	if got := ErrorBody(long, ErrorBodyLimit); len(got) != ErrorBodyLimit {
		t.Fatalf("ErrorBody length = %d, want %d", len(got), ErrorBodyLimit)
	}
}

func TestCloneDefaultTransportFallsBackForCustomRoundTripper(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unexpected RoundTrip call")
	})

	if transport := CloneDefaultTransport(); transport == nil {
		t.Fatal("CloneDefaultTransport() = nil")
	}
}

func TestNewClientSharesDefaultTransportUnlessAsked(t *testing.T) {
	plain := NewClient(ClientOptions{Timeout: time.Second})
	if plain.Transport != nil || plain.Timeout != time.Second {
		t.Fatalf("plain client = %+v, want default transport with timeout", plain)
	}
	dialed := false
	bound := NewClient(ClientOptions{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, errors.New("no dial in test")
		},
		TLS:               &tls.Config{ServerName: "probe", MinVersion: tls.VersionTLS12},
		Proxy:             http.ProxyURL(&url.URL{Scheme: "http", Host: "proxy:3128"}),
		DisableKeepAlives: true,
	})
	tr, ok := bound.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("bound client transport = %T, want *http.Transport", bound.Transport)
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.ServerName != "probe" || !tr.DisableKeepAlives || tr.Proxy == nil {
		t.Fatalf("bound transport = %+v, want TLS, proxy and no keep-alives", tr)
	}
	if _, err := tr.DialContext(context.Background(), "tcp", "host:1"); err == nil || !dialed {
		t.Fatal("bound transport must dial through the given DialContext")
	}
	if def, isDefault := http.DefaultTransport.(*http.Transport); isDefault && def.DisableKeepAlives {
		t.Fatal("NewClient must not mutate the shared default transport")
	}
}
