// Package httpx names shared HTTP headers and media types.
package httpx

import "net/http"

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
