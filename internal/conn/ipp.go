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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buildIPPRequest(ippCUPSGetDefault, 1)))
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
	b.Write([]byte{0x02, 0x00}) // version 2.0
	_ = binary.Write(&b, binary.BigEndian, op)
	_ = binary.Write(&b, binary.BigEndian, requestID)
	b.WriteByte(0x01) // operation-attributes-tag
	writeIPPAttr(&b, 0x47, "attributes-charset", "utf-8")
	writeIPPAttr(&b, 0x48, "attributes-natural-language", "en")
	b.WriteByte(0x03) // end-of-attributes-tag
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
	if len(b) < 8 {
		return "", 0, errors.New("short IPP response")
	}
	version = fmt.Sprintf("%d.%d", b[0], b[1])
	status = binary.BigEndian.Uint16(b[2:4])
	return version, status, nil
}

// ippStatusName maps a few common IPP status codes; others render as hex.
func ippStatusName(code uint16) string {
	switch code {
	case 0x0000:
		return "successful-ok"
	case 0x0401:
		return "client-error-not-authorized"
	case 0x0406:
		return "client-error-not-found"
	case 0x0500:
		return "server-error-internal-error"
	default:
		return fmt.Sprintf("0x%04x", code)
	}
}
