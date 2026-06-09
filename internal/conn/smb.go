package conn

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/cloudsoda/go-smb2"
)

func init() { Register(smbProtocol{}, "samba", "cifs") }

// smbProtocol probes an SMB/CIFS server (e.g. Samba). It first runs a native
// SMB2 NEGOTIATE to learn the dialect, protocol family and whether signing is
// required (the go-smb2 library does not expose these), which also proves the
// server is up. When a `user` is given it then authenticates with NTLM via
// go-smb2 (auth must succeed), counts the shares, and — if a share is named in
// `query` — verifies it can be mounted. The negotiated dialect is the reported
// version (pair with on_version_change). The domain may be embedded in `user`
// (DOMAIN\user or user@domain).
type smbProtocol struct{}

func (smbProtocol) Name() string       { return "smb" }
func (smbProtocol) DefaultPort() int   { return 445 }
func (smbProtocol) RequiresUser() bool { return false }

func (smbProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 445
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	dialect, signingRequired, err := smbNegotiate(ctx, addr, cfg.Interface)
	if err != nil {
		return Result{}, err
	}
	extra := map[string]string{
		"protocol":         smbProtocolName(dialect),
		"signing_required": strconv.FormatBool(signingRequired),
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
	tcp, err := BindDialer(cfg.Interface).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smb auth: %w", err)
	}
	s, err := d.DialConn(ctx, tcp, addr)
	if err != nil {
		_ = tcp.Close()
		return fmt.Errorf("smb auth: %w", err)
	}
	defer func() { _ = s.Logoff() }()
	extra["authenticated"] = "true"

	if names, err := s.ListSharenames(); err == nil {
		extra["shares"] = strconv.Itoa(len(names))
	}
	if share := cfg.Query; share != "" {
		fs, err := s.Mount(share)
		if err != nil {
			return fmt.Errorf("smb mount %q: %w", share, err)
		}
		_ = fs.Umount()
		extra["share_access"] = share
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
	c, err := BindDialer(iface).DialContext(ctx, "tcp", addr)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}

	req, err := buildSMBNegotiate()
	if err != nil {
		return 0, false, err
	}
	if _, err := c.Write(req); err != nil {
		return 0, false, err
	}

	var h [4]byte
	if _, err := io.ReadFull(c, h[:]); err != nil {
		return 0, false, err
	}
	n := int(h[1])<<16 | int(h[2])<<8 | int(h[3]) // direct-TCP message length
	if n < 70 || n > 1<<16 {
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
	if len(resp) < 70 || !bytes.Equal(resp[0:4], []byte{0xFE, 'S', 'M', 'B'}) {
		return 0, false, errors.New("not an SMB2 response")
	}
	securityMode := binary.LittleEndian.Uint16(resp[66:68])
	dialect := binary.LittleEndian.Uint16(resp[68:70])
	const signingRequired = 0x0002 // SMB2_NEGOTIATE_SIGNING_REQUIRED
	return dialect, securityMode&signingRequired != 0, nil
}

// buildSMBNegotiate builds a direct-TCP-framed SMB2 NEGOTIATE request offering
// dialects 2.0.2..3.1.1 (with the mandatory pre-auth integrity context for
// 3.1.1).
func buildSMBNegotiate() ([]byte, error) {
	var guid [16]byte
	if _, err := rand.Read(guid[:]); err != nil {
		return nil, err
	}
	var salt [32]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return nil, err
	}
	dialects := []uint16{0x0202, 0x0210, 0x0300, 0x0302, 0x0311}

	var b bytes.Buffer
	// SMB2 header (64 bytes): ProtocolId, StructureSize, Command=NEGOTIATE.
	hdr := make([]byte, 64)
	copy(hdr[0:4], []byte{0xFE, 'S', 'M', 'B'})
	binary.LittleEndian.PutUint16(hdr[4:], 64)
	binary.LittleEndian.PutUint16(hdr[14:], 1) // CreditRequest
	b.Write(hdr)

	// NEGOTIATE request body (36 fixed bytes).
	body := make([]byte, 36)
	binary.LittleEndian.PutUint16(body[0:], 36)                    // StructureSize
	binary.LittleEndian.PutUint16(body[2:], uint16(len(dialects))) // DialectCount
	binary.LittleEndian.PutUint16(body[4:], 0x0001)                // SecurityMode = SIGNING_ENABLED
	copy(body[12:28], guid[:])                                     // ClientGuid
	binary.LittleEndian.PutUint32(body[28:], 112)                  // NegotiateContextOffset
	binary.LittleEndian.PutUint16(body[32:], 1)                    // NegotiateContextCount
	b.Write(body)

	for _, d := range dialects {
		_ = binary.Write(&b, binary.LittleEndian, d)
	}
	b.Write([]byte{0, 0}) // pad dialects (110) to 8-byte alignment (112)

	// SMB2_PREAUTH_INTEGRITY_CAPABILITIES context.
	ctx := make([]byte, 8)
	binary.LittleEndian.PutUint16(ctx[0:], 0x0001) // ContextType
	binary.LittleEndian.PutUint16(ctx[2:], 38)     // DataLength
	b.Write(ctx)
	data := make([]byte, 6)
	binary.LittleEndian.PutUint16(data[0:], 1)      // HashAlgorithmCount
	binary.LittleEndian.PutUint16(data[2:], 32)     // SaltLength
	binary.LittleEndian.PutUint16(data[4:], 0x0001) // SHA-512
	b.Write(data)
	b.Write(salt[:])

	msg := b.Bytes()
	frame := []byte{0x00, byte(len(msg) >> 16), byte(len(msg) >> 8), byte(len(msg))}
	return append(frame, msg...), nil
}

func smbDialectName(d uint16) string {
	switch d {
	case 0x0202:
		return "2.0.2"
	case 0x0210:
		return "2.1"
	case 0x0300:
		return "3.0"
	case 0x0302:
		return "3.0.2"
	case 0x0311:
		return "3.1.1"
	case 0x02FF:
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
