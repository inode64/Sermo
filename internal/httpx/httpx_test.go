package httpx

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
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
