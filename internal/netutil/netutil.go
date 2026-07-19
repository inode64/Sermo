// Package netutil names shared network constants.
package netutil

import (
	"context"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// redactedMark replaces any credential material in a redacted URL.
const redactedMark = "xxxxx"

// userinfoPasswordPattern matches the password in a URL userinfo section
// (scheme://user:PASSWORD@host), used to scrub a URL that url.Parse rejects.
var userinfoPasswordPattern = regexp.MustCompile(`(//[^/@\s:]+:)[^/@\s]*(@)`)

// RedactURL strips credential material from a URL so it is safe to put in an
// error, event or log. It drops the entire query string — `?token=` /
// `?access_token=` are common credential carriers Go never redacts — and masks
// the userinfo password. A URL that url.Parse rejects (e.g. a control char in
// the password) is scrubbed textually so a parse failure can never surface the
// raw credential either.
func RedactURL(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		raw = raw[:i]
	}
	if u, err := url.Parse(raw); err == nil {
		if u.User != nil {
			if _, hasPassword := u.User.Password(); hasPassword {
				u.User = url.UserPassword(u.User.Username(), redactedMark)
			}
		}
		return u.String()
	}
	return userinfoPasswordPattern.ReplaceAllString(raw, "${1}"+redactedMark+"${2}")
}

// URLErrorCause unwraps a *url.Error to its underlying cause, dropping the URL
// text it embeds (which may carry a credential). Callers that still want to
// show the URL should pass it separately through RedactURL.
func URLErrorCause(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}

const (
	// LoopbackIPv4 is the IPv4 loopback address used for local-only defaults.
	LoopbackIPv4 = "127.0.0.1"
	// NetworkUnix is the net package network name for Unix-domain sockets.
	NetworkUnix = "unix"
)

// URL scheme constants shared by packages that must not depend on each other.
const (
	URLSchemeHTTP      = "http"
	URLSchemeHTTPS     = "https"
	URLSchemeSeparator = "://"
)

// JoinHostPort formats host with an integer port using net.JoinHostPort rules.
func JoinHostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// TimeoutFromContext derives a dial timeout from ctx's deadline: the remaining
// time when one is set, fallback when there is none, and 1ns when the deadline
// has already passed so the dial fails fast instead of hanging.
func TimeoutFromContext(ctx context.Context, fallback time.Duration) time.Duration {
	dl, ok := ctx.Deadline()
	if !ok {
		return fallback
	}
	if d := time.Until(dl); d > 0 {
		return d
	}
	return time.Nanosecond
}

// TLS mode tokens shared by the transports that accept a friendly tls value.
const (
	TLSModeTrue       = "true"
	TLSModeSkipVerify = "skip-verify"
)

// NormalizeTLS maps a friendly tls value to the canonical mode: "" plaintext,
// "true" verified TLS, "skip-verify" unverified TLS, or a custom name passed
// through (e.g. a registered go-sql-driver tls config). Shared by the conn
// probes and the Docker HTTP transport, which accept the same spellings.
func NormalizeTLS(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "false", "no", "off":
		return ""
	case TLSModeTrue, "yes", "on", "required":
		return TLSModeTrue
	case TLSModeSkipVerify:
		return TLSModeSkipVerify
	default:
		return s
	}
}

// ValidTLSValue reports whether s is one of the shared friendly tls spellings
// every transport accepts (the boolean forms plus required and skip-verify).
// Transports with extra modes (e.g. the SQL drivers' sslmode names) extend it
// on their side, so a common spelling can never be accepted by one transport
// and rejected by another.
func ValidTLSValue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case TLSModeTrue, "false", "yes", "no", "on", "off", "required", TLSModeSkipVerify:
		return true
	default:
		return false
	}
}
