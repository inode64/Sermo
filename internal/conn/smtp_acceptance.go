package conn

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	netsmtp "net/smtp"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
)

// smtpAcceptanceProtocol verifies that a destination MX accepts this host's
// SMTP envelope through RCPT TO. It deliberately never calls Client.Data: a
// successful probe resets the transaction and quits without transferring or
// queueing a message.
type smtpAcceptanceProtocol struct {
	lookupMX  smtpMXLookup
	tlsConfig func(string) *tls.Config
}

type smtpMXLookup func(context.Context, string, string) ([]*net.MX, error)

const (
	smtpAcceptanceMaxMXAttempts = 3
	smtpAcceptanceReplyMaxBytes = 512
	smtpReplyClassTemporary     = 4
	smtpReplyClassPermanent     = 5

	// SMTPStartTLSRequired rejects an MX that does not advertise STARTTLS.
	SMTPStartTLSRequired = "required"
	// SMTPStartTLSOpportunistic permits plaintext when STARTTLS is unavailable.
	SMTPStartTLSOpportunistic = "opportunistic"
)

// SMTP acceptance result keys are public because checks expose them in
// Result.Data and operators may assert them with expect.
const (
	ExtraKeySMTPAccepted        = "accepted"
	ExtraKeySMTPEnhancedStatus  = "enhanced_status"
	ExtraKeySMTPMXHost          = "mx_host"
	ExtraKeySMTPRecipientDomain = "recipient_domain"
	ExtraKeySMTPReply           = "smtp_reply"
	ExtraKeySMTPStage           = "smtp_stage"
	ExtraKeySMTPStatus          = "smtp_status"
	ExtraKeySMTPStatusCode      = "smtp_code"
	ExtraKeySMTPStartTLS        = ParamKeySMTPStartTLS
)

const (
	smtpAcceptanceStageGreeting = "greeting"
	smtpAcceptanceStageEHLO     = "ehlo"
	smtpAcceptanceStageStartTLS = "starttls"
	smtpAcceptanceStageMailFrom = "mail_from"
	smtpAcceptanceStageRCPTTo   = "rcpt_to"
	smtpAcceptanceStageMX       = "mx"

	smtpAcceptanceStatusAccepted  = "accepted"
	smtpAcceptanceStatusTemporary = "temporary"
	smtpAcceptanceStatusPermanent = "permanent"
	smtpAcceptanceStatusPolicy    = "policy"
	smtpAcceptanceStatusProtocol  = "protocol"
)

// SMTPAcceptanceEnvelope is the validated, normalized SMTP envelope used by
// acceptance checks. RecipientDomain is derived from Recipient and selects the
// MX lookup target.
type SMTPAcceptanceEnvelope struct {
	Helo            string
	MailFrom        string
	Recipient       string
	RecipientDomain string
	StartTLS        string
}

func (smtpAcceptanceProtocol) Name() string       { return ProtocolNameSMTPAcceptance }
func (smtpAcceptanceProtocol) DefaultPort() int   { return defaultPortSMTP }
func (smtpAcceptanceProtocol) RequiresUser() bool { return false }

func (p smtpAcceptanceProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	envelope, err := ParseSMTPAcceptanceEnvelope(
		cfg.Params[ParamKeySMTPHelo],
		cfg.Params[ParamKeySMTPMailFrom],
		cfg.Params[ParamKeySMTPRecipient],
		cfg.Params[ParamKeySMTPStartTLS],
	)
	if err != nil {
		return Result{}, probeErr(ProtocolNameSMTPAcceptance, stepConfig, err)
	}

	lookupMX := p.lookupMX
	if lookupMX == nil {
		lookupMX = lookupSMTPMX
	}
	tlsConfig := p.tlsConfig
	if tlsConfig == nil {
		tlsConfig = tlsClientConfig
	}
	mxs, err := lookupMX(ctx, envelope.RecipientDomain, cfg.Interface)
	if err != nil {
		return Result{}, probeErr(ProtocolNameSMTPAcceptance, stepResolveServer, fmt.Errorf("MX for %s: %w", envelope.RecipientDomain, err))
	}
	if len(mxs) == 0 {
		return Result{}, probeErr(ProtocolNameSMTPAcceptance, stepResolveServer, fmt.Errorf("MX for %s: no records", envelope.RecipientDomain))
	}
	if isSMTPNullMX(mxs) {
		res := newSMTPAcceptanceResult(".", envelope.RecipientDomain)
		return smtpAcceptancePolicyFailure(res, smtpAcceptanceStageMX, "recipient domain publishes null MX"), nil
	}
	slices.SortFunc(mxs, func(a, b *net.MX) int { return cmp.Compare(a.Pref, b.Pref) })

	limit := min(len(mxs), smtpAcceptanceMaxMXAttempts)
	dialErrors := make([]error, 0, limit)
	for _, mx := range mxs[:limit] {
		host := strings.TrimSuffix(mx.Host, ".")
		if host == "" {
			dialErrors = append(dialErrors, errors.New("MX target is empty"))
			continue
		}
		res, attemptErr := probeSMTPAcceptanceMX(ctx, cfg, host, envelope, tlsConfig)
		if attemptErr == nil {
			// An SMTP reply is an authoritative verdict from the preferred MX.
			// Only transport failures fall through to a lower-priority MX, so a
			// later server cannot mask a rejection from the provider's primary.
			return res, nil
		}
		dialErrors = append(dialErrors, fmt.Errorf("%s: %w", host, attemptErr))
	}
	return Result{}, probeErr(ProtocolNameSMTPAcceptance, stepDial, errors.Join(dialErrors...))
}

func probeSMTPAcceptanceMX(
	ctx context.Context,
	cfg Config,
	mxHost string,
	envelope SMTPAcceptanceEnvelope,
	tlsConfig func(string) *tls.Config,
) (Result, error) {
	res := newSMTPAcceptanceResult(mxHost, envelope.RecipientDomain)
	extra := res.Extra

	// The recipient domain genuinely selects a different transport target via
	// MX. Rebuild the prepared target for that resolved host while preserving the
	// configured port, timeout context and mandatory interface binding.
	targetCfg := cfg
	targetCfg.Host, targetCfg.Socket, targetCfg.TLS = mxHost, "", ""
	conn, err := probeTargetFor(ctx, targetCfg, defaultPortSMTP).openTCP(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = conn.Close() }()

	client, err := netsmtp.NewClient(conn, mxHost)
	if err != nil {
		return smtpAcceptanceStepResult(res, smtpAcceptanceStageGreeting, err)
	}
	defer func() { _ = client.Close() }()

	if err := client.Hello(envelope.Helo); err != nil {
		return smtpAcceptanceStepResult(res, smtpAcceptanceStageEHLO, err)
	}
	offersStartTLS, _ := client.Extension("STARTTLS")
	if !offersStartTLS && envelope.StartTLS == SMTPStartTLSRequired {
		return smtpAcceptancePolicyFailure(res, smtpAcceptanceStageStartTLS, "STARTTLS not advertised"), nil
	}
	if offersStartTLS {
		if err := client.StartTLS(tlsConfig(mxHost)); err != nil {
			return smtpAcceptanceStepResult(res, smtpAcceptanceStageStartTLS, err)
		}
		extra[ExtraKeySMTPStartTLS] = "true"
	}
	if err := client.Mail(envelope.MailFrom); err != nil {
		return smtpAcceptanceStepResult(res, smtpAcceptanceStageMailFrom, err)
	}
	if err := client.Rcpt(envelope.Recipient); err != nil {
		return smtpAcceptanceStepResult(res, smtpAcceptanceStageRCPTTo, err)
	}

	extra[ExtraKeySMTPAccepted] = "true"
	extra[ExtraKeySMTPStage] = smtpAcceptanceStageRCPTTo
	extra[ExtraKeySMTPStatus] = smtpAcceptanceStatusAccepted
	_ = client.Reset()
	_ = client.Quit()
	return res, nil
}

func newSMTPAcceptanceResult(mxHost, recipientDomain string) Result {
	return Result{Extra: map[string]string{
		ExtraKeySMTPAccepted:        "false",
		ExtraKeySMTPMXHost:          mxHost,
		ExtraKeySMTPRecipientDomain: recipientDomain,
		ExtraKeySMTPStartTLS:        "false",
	}}
}

func isSMTPNullMX(mxs []*net.MX) bool {
	return len(mxs) == 1 && mxs[0] != nil && mxs[0].Pref == 0 && mxs[0].Host == "."
}

func smtpAcceptanceStepResult(res Result, stage string, err error) (Result, error) {
	var replyErr *textproto.Error
	if !errors.As(err, &replyErr) {
		return res, probeErr(ProtocolNameSMTPAcceptance, stage, err)
	}
	reply := truncateSMTPReply(replyErr.Msg)
	res.Extra[ExtraKeySMTPStage] = stage
	res.Extra[ExtraKeySMTPStatusCode] = strconv.Itoa(replyErr.Code)
	res.Extra[ExtraKeySMTPReply] = reply
	switch replyErr.Code / 100 {
	case smtpReplyClassTemporary:
		res.Extra[ExtraKeySMTPStatus] = smtpAcceptanceStatusTemporary
	case smtpReplyClassPermanent:
		res.Extra[ExtraKeySMTPStatus] = smtpAcceptanceStatusPermanent
	default:
		res.Extra[ExtraKeySMTPStatus] = smtpAcceptanceStatusProtocol
	}
	if enhanced := smtpEnhancedStatus(reply); enhanced != "" {
		res.Extra[ExtraKeySMTPEnhancedStatus] = enhanced
	}
	res.Failure = fmt.Sprintf("%s rejected: %d %s", stage, replyErr.Code, reply)
	return res, nil
}

func smtpAcceptancePolicyFailure(res Result, stage, reply string) Result {
	res.Extra[ExtraKeySMTPStage] = stage
	res.Extra[ExtraKeySMTPStatus] = smtpAcceptanceStatusPolicy
	res.Extra[ExtraKeySMTPReply] = reply
	res.Failure = stage + " policy: " + reply
	return res
}

func smtpEnhancedStatus(reply string) string {
	for field := range strings.FieldsSeq(reply) {
		parts := strings.Split(field, ".")
		if len(parts) != 3 || len(parts[0]) != 1 {
			continue
		}
		valid := true
		for _, part := range parts {
			if part == "" {
				valid = false
				break
			}
			for _, char := range part {
				if char < '0' || char > '9' {
					valid = false
					break
				}
			}
		}
		if valid && (parts[0] == "2" || parts[0] == "4" || parts[0] == "5") {
			return field
		}
	}
	return ""
}

func truncateSMTPReply(reply string) string {
	reply = strings.TrimSpace(strings.ToValidUTF8(reply, "?"))
	if len(reply) <= smtpAcceptanceReplyMaxBytes {
		return reply
	}
	return strings.ToValidUTF8(reply[:smtpAcceptanceReplyMaxBytes], "?")
}

// ParseSMTPAcceptanceEnvelope validates and normalizes the identities and TLS
// policy required by an SMTP acceptance probe.
func ParseSMTPAcceptanceEnvelope(helo, mailFrom, recipient, startTLS string) (SMTPAcceptanceEnvelope, error) {
	if !ValidSMTPHelo(helo) {
		return SMTPAcceptanceEnvelope{}, fmt.Errorf("helo %q must be a fully-qualified DNS name", helo)
	}
	mailFrom, _, err := ParseSMTPMailbox(mailFrom)
	if err != nil {
		return SMTPAcceptanceEnvelope{}, fmt.Errorf("mail_from: %w", err)
	}
	recipient, recipientDomain, err := ParseSMTPMailbox(recipient)
	if err != nil {
		return SMTPAcceptanceEnvelope{}, fmt.Errorf("recipient: %w", err)
	}
	startTLS = NormalizeSMTPStartTLS(startTLS)
	if !ValidSMTPStartTLS(startTLS) {
		return SMTPAcceptanceEnvelope{}, fmt.Errorf("starttls %q must be %s", startTLS, SMTPStartTLSValueSummary)
	}
	return SMTPAcceptanceEnvelope{
		Helo:            helo,
		MailFrom:        mailFrom,
		Recipient:       recipient,
		RecipientDomain: recipientDomain,
		StartTLS:        startTLS,
	}, nil
}

// ParseSMTPMailbox validates a bare SMTP mailbox and returns its normalized
// addr-spec plus the lower-case recipient domain used for MX resolution.
func ParseSMTPMailbox(value string) (address, domain string, err error) {
	if value == "" || strings.TrimSpace(value) != value {
		return "", "", errors.New("must be a non-empty bare email address")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Name != "" || parsed.Address != value {
		return "", "", errors.New("must be a bare email address")
	}
	at := strings.LastIndexByte(parsed.Address, '@')
	if at <= 0 || at == len(parsed.Address)-1 {
		return "", "", errors.New("must contain a local part and DNS domain")
	}
	domain = strings.ToLower(strings.TrimSuffix(parsed.Address[at+1:], "."))
	if !validSMTPDomain(domain, true) {
		return "", "", errors.New("domain must be a fully-qualified DNS name")
	}
	return parsed.Address, domain, nil
}

// ValidSMTPHelo reports whether value is a fully-qualified DNS hostname safe to
// send verbatim in EHLO/HELO.
func ValidSMTPHelo(value string) bool {
	return value == strings.TrimSpace(value) && validSMTPDomain(strings.TrimSuffix(value, "."), true)
}

func validSMTPDomain(domain string, requireDot bool) bool {
	if domain == "" || len(domain) > 253 || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	if requireDot && !strings.Contains(domain, ".") {
		return false
	}
	for label := range strings.SplitSeq(domain, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
				(char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

// SMTPStartTLSValueSummary is the user-facing list of supported policies.
const SMTPStartTLSValueSummary = SMTPStartTLSRequired + " or " + SMTPStartTLSOpportunistic

// NormalizeSMTPStartTLS applies the secure default for acceptance probes.
func NormalizeSMTPStartTLS(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return SMTPStartTLSRequired
	}
	return value
}

// ValidSMTPStartTLS reports whether value selects a supported STARTTLS policy.
func ValidSMTPStartTLS(value string) bool {
	switch NormalizeSMTPStartTLS(value) {
	case SMTPStartTLSRequired, SMTPStartTLSOpportunistic:
		return true
	default:
		return false
	}
}

func lookupSMTPMX(ctx context.Context, domain, iface string) ([]*net.MX, error) {
	resolver := net.DefaultResolver
	if iface != "" {
		resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return nil, fmt.Errorf("parse DNS resolver address %q: %w", address, err)
				}
				return BindDialer(dnsProbeInterface(host, iface)).DialContext(ctx, network, address)
			},
		}
	}
	mxs, err := resolver.LookupMX(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("look up MX for %s: %w", domain, err)
	}
	return mxs, nil
}
