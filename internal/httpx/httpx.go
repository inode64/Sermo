// Package httpx names shared HTTP headers and media types.
package httpx

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
