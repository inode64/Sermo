package conn

import (
	"context"
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
	return probeLineCommand(ctx, cfg, defaultPortAsterisk, "", func(line string) (Result, bool) {
		version, ok := asteriskGreetingVersion(line)
		return Result{Version: version, Extra: map[string]string{extraBanner: line}}, ok
	}, "not an Asterisk AMI greeting: %q")
}

// asteriskGreetingVersion extracts the manager version from an AMI greeting
// ("Asterisk Call Manager/2.10.6" -> "2.10.6").
func asteriskGreetingVersion(line string) (string, bool) {
	if !strings.HasPrefix(line, asteriskGreetingPrefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, asteriskGreetingPrefix)), true
}
