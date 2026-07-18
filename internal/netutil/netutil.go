// Package netutil names shared network constants.
package netutil

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"
)

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
