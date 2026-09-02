// Package httpx names shared HTTP headers and media types.
package httpx

import (
	"errors"
	"io"
	"net/http"
	"strings"
)

const (
	// HeaderAccept is the standard Accept request header.
	HeaderAccept = "Accept"
	// HeaderAuthorization is the standard Authorization request header.
	HeaderAuthorization = "Authorization"
	// HeaderContentType is the standard Content-Type header.
	HeaderContentType = "Content-Type"
	// HeaderServer is the standard Server response header.
	HeaderServer = "Server"
)

const (
	// ContentTypeJSON is the JSON media type used by Sermo HTTP clients and APIs.
	ContentTypeJSON = "application/json"
)

const (
	statusClassDivisor = 100
	statusClassSuccess = 2
	// ErrorBodyLimit bounds a non-2xx response body captured into an error.
	ErrorBodyLimit = 256
)

// SuccessStatus reports whether code is a 2xx HTTP status.
func SuccessStatus(code int) bool {
	return code/statusClassDivisor == statusClassSuccess
}

// ErrorBody reads at most limit bytes of resp's body, trimmed. Callers use it
// after a non-success status so the operator sees the remote error text.
func ErrorBody(resp *http.Response, limit int64) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	return strings.TrimSpace(string(snippet))
}

// ErrNilResponse reports a transport that broke net/http's contract by
// returning neither a response nor an error.
var ErrNilResponse = errors.New("http: nil response without error")

// Doer performs an HTTP request; *http.Client satisfies it. Callers take the
// interface so tests can inject a stub.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Do performs req through client and guarantees a non-nil response whenever it
// returns a nil error. net/http already documents that contract ("On error, any
// Response can be ignored"), but it is not expressible in the type system, so
// every caller would otherwise repeat the same defensive check — or, as static
// analysis keeps pointing out, skip it. The transport error is returned
// unchanged so each caller keeps its own wrapping and credential scrubbing.
func Do(client Doer, req *http.Request) (*http.Response, error) {
	resp, err := client.Do(req)
	switch {
	case err != nil:
		//nolint:wrapcheck // by design, see above: the caller wraps, and the notify/telegram callers must scrub the bot token out of the *url.Error first.
		return nil, err
	case resp == nil:
		return nil, ErrNilResponse
	}
	return resp, nil
}

// CloneDefaultTransport returns a mutable copy of the process default HTTP
// transport. A caller may replace http.DefaultTransport with a custom
// RoundTripper; use a working zero-value transport rather than panic when the
// caller needs Transport-specific settings such as a dialer or TLS config.
func CloneDefaultTransport() *http.Transport {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		return transport.Clone()
	}
	return &http.Transport{}
}
