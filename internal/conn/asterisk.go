package conn

import (
	"context"
	"fmt"
	"strings"
)

func init() { Register(asteriskProtocol{}, protocolAliasAMI) }

// asteriskProtocol probes an Asterisk PBX via its Manager Interface (AMI). On
// connect, AMI sends an "Asterisk Call Manager/<version>" greeting before any
// login; reading it proves AMI is up and yields the manager version. No auth
// (the greeting precedes the optional Login action). `tls` enables AMI over TLS.
type asteriskProtocol struct{}

func (asteriskProtocol) Name() string       { return ProtocolNameAsterisk }
func (asteriskProtocol) DefaultPort() int   { return defaultPortAsterisk }
func (asteriskProtocol) RequiresUser() bool { return false }

const asteriskGreetingPrefix = "Asterisk Call Manager/"

func (asteriskProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := dialDeadline(ctx, cfg, defaultPortAsterisk)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()

	line, err := readGreetingLine(c)
	if err != nil {
		return Result{}, err
	}
	version, ok := asteriskGreetingVersion(line)
	if !ok {
		return Result{}, fmt.Errorf("not an Asterisk AMI greeting: %q", line)
	}
	return Result{Version: version, Extra: map[string]string{extraBanner: line}}, nil
}

// asteriskGreetingVersion extracts the manager version from an AMI greeting
// ("Asterisk Call Manager/2.10.6" -> "2.10.6").
func asteriskGreetingVersion(line string) (string, bool) {
	if !strings.HasPrefix(line, asteriskGreetingPrefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, asteriskGreetingPrefix)), true
}
