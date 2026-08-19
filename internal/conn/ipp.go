package conn

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"github.com/OpenPrinting/goipp"
)

const (
	ippEndpointRoot  = "/"
	ippExtraStatus   = "ipp_status"
	ippExtraVersion  = "ipp_version"
	ippVersionPrefix = "IPP/"
)

const (
	ippRequestIDDefault    = 1
	ippAttrCharset         = "attributes-charset"
	ippAttrNaturalLanguage = "attributes-natural-language"
	ippCharsetUTF8         = "utf-8"
	ippLanguageEN          = "en"
)

const (
	ippStatusOK                  = 0x0000
	ippStatusClientUnauthorized  = 0x0401
	ippStatusClientNotFound      = 0x0406
	ippStatusServerInternalError = 0x0500
	ippStatusNameOK              = "successful-ok"
	ippStatusNameUnauthorized    = "client-error-not-authorized"
	ippStatusNameNotFound        = "client-error-not-found"
	ippStatusNameInternalError   = "server-error-internal-error"
)

// ippProtocol probes an IPP server (CUPS/cupsd) natively: it POSTs an IPP
// request (CUPS-Get-Default) to the server over HTTP and verifies a valid IPP
// response. Any parseable IPP reply proves the daemon is up and speaking IPP.
type ippProtocol struct{}

func (ippProtocol) Name() string       { return ProtocolNameIPP }
func (ippProtocol) DefaultPort() int   { return defaultPortIPP }
func (ippProtocol) RequiresUser() bool { return false }

func (ippProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	client, base := httpProbeBase(ctx, cfg, defaultPortIPP)
	url := base + ippEndpointRoot
	payload, err := buildIPPRequest(goipp.OpCupsGetDefault, ippRequestIDDefault)
	if err != nil {
		return Result{}, probeErr(ProtocolNameIPP, stepRequest, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Result{}, probeErr(ProtocolNameIPP, stepRequest, err)
	}
	req.Header.Set(httpHeaderContentType, goipp.ContentType)

	resp, err := doHTTPProbe(client, req, maxHTTPProbeBody)
	if err != nil {
		return Result{}, err
	}
	if resp.status != http.StatusOK {
		return Result{}, fmt.Errorf("ipp: HTTP status %d", resp.status)
	}
	version, status, err := parseIPPResponse(resp.body)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Version: ippVersionPrefix + version,
		Extra: map[string]string{
			ippExtraVersion: version,
			ippExtraStatus:  ippStatusName(status),
		},
	}, nil
}

// buildIPPRequest uses goipp only as the RFC 8010 codec. HTTP, interface
// binding, TLS and response bounds remain owned by Sermo's probe transport.
func buildIPPRequest(op goipp.Op, requestID uint32) ([]byte, error) {
	message := goipp.NewRequest(goipp.DefaultVersion, op, requestID)
	message.Operation.Add(goipp.MakeAttr(ippAttrCharset, goipp.TagCharset, goipp.String(ippCharsetUTF8)))
	message.Operation.Add(goipp.MakeAttr(ippAttrNaturalLanguage, goipp.TagLanguage, goipp.String(ippLanguageEN)))
	payload, err := message.EncodeBytes()
	if err != nil {
		return nil, fmt.Errorf("encode IPP request: %w", err)
	}
	return payload, nil
}

// parseIPPResponse validates the complete IPP message and returns the response
// header fields exposed by the probe.
func parseIPPResponse(b []byte) (version string, status uint16, err error) {
	var message goipp.Message
	if err := message.DecodeBytes(b); err != nil {
		return "", 0, fmt.Errorf("decode IPP response: %w", err)
	}
	return message.Version.String(), uint16(message.Code), nil
}

// ippStatusNames maps a few common IPP status codes; others render as hex.
var ippStatusNames = map[uint16]string{
	ippStatusOK:                  ippStatusNameOK,
	ippStatusClientUnauthorized:  ippStatusNameUnauthorized,
	ippStatusClientNotFound:      ippStatusNameNotFound,
	ippStatusServerInternalError: ippStatusNameInternalError,
}

func ippStatusName(code uint16) string {
	return codeName(code, ippStatusNames, "0x%04x")
}
