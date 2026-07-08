package conn

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
)

func init() { Register(ippProtocol{}, protocolAliasCUPS) }

// ippCUPSGetDefault is the CUPS-Get-Default operation id — a server-level IPP
// operation needing only the charset/language attributes, so it works without a
// printer URI.
const (
	ippContentType    = "application/ipp"
	ippCUPSGetDefault = 0x4001
	ippEndpointRoot   = "/"
	ippExtraStatus    = "ipp_status"
	ippExtraVersion   = "ipp_version"
	ippVersionPrefix  = "IPP/"
)

const (
	ippRequestIDDefault    = 1
	ippResponseMinBytes    = 8
	ippStatusOffset        = 2
	ippStatusEndOffset     = 4
	ippVersionMajor        = 2
	ippVersionMinor        = 0
	ippVersionMajorOffset  = 0
	ippVersionMinorOffset  = 1
	ippTagOperationAttrs   = 0x01
	ippTagEndOfAttributes  = 0x03
	ippTagCharset          = 0x47
	ippTagNaturalLanguage  = 0x48
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
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPortIPP
	}
	scheme := schemeHTTP
	client := httpProbeClient(cfg.Interface, nil)
	if mode := normalizeTLS(cfg.TLS); mode != "" {
		scheme = schemeHTTPS
		tlsConfig := tlsClientConfig(host)
		if mode == tlsSkipVerify {
			tlsConfig.InsecureSkipVerify = true //nolint:gosec // operator chose tls: skip-verify
		}
		client = httpProbeClient(cfg.Interface, tlsConfig)
	}

	url := scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)) + ippEndpointRoot
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buildIPPRequest(ippCUPSGetDefault, ippRequestIDDefault)))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set(httpHeaderContentType, ippContentType)

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("ipp: HTTP status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPProbeBody))
	version, status, err := parseIPPResponse(body)
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

// buildIPPRequest builds an IPP/2.0 request for op with the mandatory
// attributes-charset and attributes-natural-language operation attributes.
func buildIPPRequest(op uint16, requestID uint32) []byte {
	var b bytes.Buffer
	b.Write([]byte{ippVersionMajor, ippVersionMinor})
	_ = binary.Write(&b, binary.BigEndian, op)
	_ = binary.Write(&b, binary.BigEndian, requestID)
	b.WriteByte(ippTagOperationAttrs)
	writeIPPAttr(&b, ippTagCharset, ippAttrCharset, ippCharsetUTF8)
	writeIPPAttr(&b, ippTagNaturalLanguage, ippAttrNaturalLanguage, ippLanguageEN)
	b.WriteByte(ippTagEndOfAttributes)
	return b.Bytes()
}

func writeIPPAttr(b *bytes.Buffer, valueTag byte, name, value string) {
	b.WriteByte(valueTag)
	_ = binary.Write(b, binary.BigEndian, uint16(len(name)))
	b.WriteString(name)
	_ = binary.Write(b, binary.BigEndian, uint16(len(value)))
	b.WriteString(value)
}

// parseIPPResponse reads the IPP response header: version and status-code.
func parseIPPResponse(b []byte) (version string, status uint16, err error) {
	if len(b) < ippResponseMinBytes {
		return "", 0, errors.New("short IPP response")
	}
	version = fmt.Sprintf("%d.%d", b[ippVersionMajorOffset], b[ippVersionMinorOffset])
	status = binary.BigEndian.Uint16(b[ippStatusOffset:ippStatusEndOffset])
	return version, status, nil
}

// ippStatusName maps a few common IPP status codes; others render as hex.
func ippStatusName(code uint16) string {
	switch code {
	case ippStatusOK:
		return ippStatusNameOK
	case ippStatusClientUnauthorized:
		return ippStatusNameUnauthorized
	case ippStatusClientNotFound:
		return ippStatusNameNotFound
	case ippStatusServerInternalError:
		return ippStatusNameInternalError
	default:
		return fmt.Sprintf("0x%04x", code)
	}
}
