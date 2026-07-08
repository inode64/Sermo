package conn

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/cloudsoda/go-smb2"
)

func init() { Register(smbProtocol{}, protocolAliasSamba, protocolAliasCIFS) }

// smbProtocol probes an SMB/CIFS server (e.g. Samba). It first runs a native
// SMB2 NEGOTIATE to learn the dialect, protocol family and whether signing is
// required (the go-smb2 library does not expose these), which also proves the
// server is up. When a `user` is given it then authenticates with NTLM via
// go-smb2 (auth must succeed), counts the shares, and — if a share is named in
// `query` — verifies it can be mounted. The negotiated dialect is the reported
// version (pair with on_version_change). The domain may be embedded in `user`
// (DOMAIN\user or user@domain).
type smbProtocol struct{}

func (smbProtocol) Name() string       { return ProtocolNameSMB }
func (smbProtocol) DefaultPort() int   { return defaultPortSMB }
func (smbProtocol) RequiresUser() bool { return false }

const (
	smbExtraSigningRequired = "signing_required"
	smbProtocolID           = "\xfeSMB"
	smbProtocolIDBytes      = 4
)

const (
	smbDirectTCPHeaderBytes        = 4
	smbDirectTCPLengthHighOffset   = 1
	smbDirectTCPLengthMiddleOffset = 2
	smbDirectTCPLengthLowOffset    = 3
	smbDirectTCPMessageType        = 0x00
	smbLengthByteShift             = 8
	smbLengthHighShift             = 16
)

const (
	smb2CreditRequestOffset           = 14
	smb2Dialect202                    = 0x0202
	smb2Dialect210                    = 0x0210
	smb2Dialect300                    = 0x0300
	smb2Dialect302                    = 0x0302
	smb2Dialect311                    = 0x0311
	smb2DialectWildcard               = 0x02FF
	smb2HeaderBytes                   = 64
	smb2HashAlgorithmSHA512           = 0x0001
	smb2MaxNegotiateResponseBytes     = 1 << 16
	smb2MinNegotiateResponseBytes     = 70
	smb2NegotiateCommand              = 1
	smb2NegotiateContextCount         = 1
	smb2NegotiateContextCountOffset   = 32
	smb2NegotiateContextOffset        = 112
	smb2NegotiateContextOffsetOffset  = 28
	smb2NegotiateDialectCountOffset   = 2
	smb2NegotiateFixedBytes           = 36
	smb2NegotiateSecurityModeOffset   = 4
	smb2NegotiateSigningEnabled       = 0x0001
	smb2NegotiateSigningRequired      = 0x0002
	smb2PreauthContextBytes           = 8
	smb2PreauthDataBytes              = 6
	smb2PreauthDataLength             = 38
	smb2PreauthDataLengthOffset       = 2
	smb2PreauthHashAlgorithmCount     = 1
	smb2PreauthHashAlgorithmOffset    = 4
	smb2PreauthHashCountOffset        = 0
	smb2PreauthIntegrityContext       = 0x0001
	smb2PreauthSaltBytes              = 32
	smb2PreauthSaltLengthOffset       = 2
	smb2ResponseDialectOffset         = 68
	smb2ResponseSecurityModeOffset    = 66
	smb2ResponseSecurityModeEndOffset = 68
	smb2ResponseDialectEndOffset      = 70
)

func (smbProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPortSMB
	}
	addr := hostPort(host, port)

	dialect, signingRequired, err := smbNegotiate(ctx, addr, cfg.Interface)
	if err != nil {
		return Result{}, err
	}
	extra := map[string]string{
		extraProtocol:           smbProtocolName(dialect),
		smbExtraSigningRequired: strconv.FormatBool(signingRequired),
	}

	if cfg.User != "" {
		if err := smbSession(ctx, addr, cfg, extra); err != nil {
			return Result{}, err
		}
	}
	return Result{Version: smbDialectName(dialect), Extra: extra}, nil
}

// smbSession authenticates with NTLM and gathers share information.
func smbSession(ctx context.Context, addr string, cfg Config, extra map[string]string) error {
	user, domain := splitSMBUser(cfg.User)
	d := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: user, Password: cfg.Password, Domain: domain}}
	tcp, err := BindDialer(cfg.Interface).DialContext(ctx, networkTCP, addr)
	if err != nil {
		return fmt.Errorf("smb auth: %w", err)
	}
	s, err := d.DialConn(ctx, tcp, addr)
	if err != nil {
		_ = tcp.Close()
		return fmt.Errorf("smb auth: %w", err)
	}
	defer func() { _ = s.Logoff() }()
	extra[extraAuthenticated] = strconv.FormatBool(true)

	if names, err := s.ListSharenames(); err == nil {
		extra[extraShares] = strconv.Itoa(len(names))
	}
	if share := cfg.Query; share != "" {
		fs, err := s.Mount(share)
		if err != nil {
			return fmt.Errorf("smb mount %q: %w", share, err)
		}
		_ = fs.Umount()
		extra[extraShareAccess] = share
	}
	return nil
}

// splitSMBUser separates a domain from a user given as "DOMAIN\user" or
// "user@domain".
func splitSMBUser(s string) (user, domain string) {
	if i := strings.IndexByte(s, '\\'); i >= 0 {
		return s[i+1:], s[:i]
	}
	if i := strings.IndexByte(s, '@'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// smbNegotiate sends a native SMB2 NEGOTIATE and returns the negotiated dialect
// and whether the server requires signing.
func smbNegotiate(ctx context.Context, addr, iface string) (dialect uint16, signingRequired bool, err error) {
	c, err := BindDialer(iface).DialContext(ctx, networkTCP, addr)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = c.Close() }()
	applyDeadline(ctx, c)

	req, err := buildSMBNegotiate()
	if err != nil {
		return 0, false, err
	}
	if _, err := c.Write(req); err != nil {
		return 0, false, err
	}

	var h [smbDirectTCPHeaderBytes]byte
	if _, err := io.ReadFull(c, h[:]); err != nil {
		return 0, false, err
	}
	n := int(h[smbDirectTCPLengthHighOffset])<<smbLengthHighShift |
		int(h[smbDirectTCPLengthMiddleOffset])<<smbLengthByteShift |
		int(h[smbDirectTCPLengthLowOffset])
	if n < smb2MinNegotiateResponseBytes || n > smb2MaxNegotiateResponseBytes {
		return 0, false, errors.New("invalid SMB2 negotiate response length")
	}
	resp := make([]byte, n)
	if _, err := io.ReadFull(c, resp); err != nil {
		return 0, false, err
	}
	return parseSMBNegotiate(resp)
}

// parseSMBNegotiate reads the dialect and signing requirement from an SMB2
// NEGOTIATE response (after the direct-TCP framing has been stripped).
func parseSMBNegotiate(resp []byte) (uint16, bool, error) {
	if len(resp) < smb2MinNegotiateResponseBytes ||
		!bytes.Equal(resp[:smbProtocolIDBytes], []byte(smbProtocolID)) {
		return 0, false, errors.New("not an SMB2 response")
	}
	securityMode := binary.LittleEndian.Uint16(resp[smb2ResponseSecurityModeOffset:smb2ResponseSecurityModeEndOffset])
	dialect := binary.LittleEndian.Uint16(resp[smb2ResponseDialectOffset:smb2ResponseDialectEndOffset])
	return dialect, securityMode&smb2NegotiateSigningRequired != 0, nil
}

// buildSMBNegotiate builds a direct-TCP-framed SMB2 NEGOTIATE request offering
// dialects 2.0.2..3.1.1 (with the mandatory pre-auth integrity context for
// 3.1.1).
func buildSMBNegotiate() ([]byte, error) {
	var guid [smbProtocolIDBytes * 4]byte
	if _, err := rand.Read(guid[:]); err != nil {
		return nil, err
	}
	var salt [smb2PreauthSaltBytes]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return nil, err
	}
	dialects := []uint16{smb2Dialect202, smb2Dialect210, smb2Dialect300, smb2Dialect302, smb2Dialect311}

	var b bytes.Buffer
	// SMB2 header (64 bytes): ProtocolId, StructureSize, Command=NEGOTIATE.
	hdr := make([]byte, smb2HeaderBytes)
	copy(hdr[:smbProtocolIDBytes], smbProtocolID)
	binary.LittleEndian.PutUint16(hdr[smbProtocolIDBytes:], smb2HeaderBytes)
	binary.LittleEndian.PutUint16(hdr[smb2CreditRequestOffset:], smb2NegotiateCommand)
	b.Write(hdr)

	// NEGOTIATE request body (36 fixed bytes).
	body := make([]byte, smb2NegotiateFixedBytes)
	binary.LittleEndian.PutUint16(body[0:], smb2NegotiateFixedBytes)
	binary.LittleEndian.PutUint16(body[smb2NegotiateDialectCountOffset:], uint16(len(dialects)))
	binary.LittleEndian.PutUint16(body[smb2NegotiateSecurityModeOffset:], smb2NegotiateSigningEnabled)
	copy(body[12:28], guid[:])
	binary.LittleEndian.PutUint32(body[smb2NegotiateContextOffsetOffset:], smb2NegotiateContextOffset)
	binary.LittleEndian.PutUint16(body[smb2NegotiateContextCountOffset:], smb2NegotiateContextCount)
	b.Write(body)

	for _, d := range dialects {
		_ = binary.Write(&b, binary.LittleEndian, d)
	}
	b.Write([]byte{0, 0}) // pad dialects (110) to 8-byte alignment (112)

	// SMB2_PREAUTH_INTEGRITY_CAPABILITIES context.
	ctx := make([]byte, smb2PreauthContextBytes)
	binary.LittleEndian.PutUint16(ctx[0:], smb2PreauthIntegrityContext)
	binary.LittleEndian.PutUint16(ctx[smb2PreauthDataLengthOffset:], smb2PreauthDataLength)
	b.Write(ctx)
	data := make([]byte, smb2PreauthDataBytes)
	binary.LittleEndian.PutUint16(data[smb2PreauthHashCountOffset:], smb2PreauthHashAlgorithmCount)
	binary.LittleEndian.PutUint16(data[smb2PreauthSaltLengthOffset:], smb2PreauthSaltBytes)
	binary.LittleEndian.PutUint16(data[smb2PreauthHashAlgorithmOffset:], smb2HashAlgorithmSHA512)
	b.Write(data)
	b.Write(salt[:])

	msg := b.Bytes()
	frame := []byte{
		smbDirectTCPMessageType,
		byte(len(msg) >> smbLengthHighShift),
		byte(len(msg) >> smbLengthByteShift),
		byte(len(msg)),
	}
	return append(frame, msg...), nil
}

func smbDialectName(d uint16) string {
	switch d {
	case smb2Dialect202:
		return "2.0.2"
	case smb2Dialect210:
		return "2.1"
	case smb2Dialect300:
		return "3.0"
	case smb2Dialect302:
		return "3.0.2"
	case smb2Dialect311:
		return "3.1.1"
	case smb2DialectWildcard:
		return "2.x"
	default:
		return fmt.Sprintf("0x%04x", d)
	}
}

func smbProtocolName(d uint16) string {
	if d >= 0x0300 {
		return "SMB3"
	}
	return "SMB2"
}
