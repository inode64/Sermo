package conn

import (
	"bufio"
	"context"
	"fmt"
	"strings"
)

func init() { Register(asteriskProtocol{}, "ami") }

// asteriskProtocol probes an Asterisk PBX via its Manager Interface (AMI). On
// connect, AMI sends an "Asterisk Call Manager/<version>" greeting before any
// login; reading it proves AMI is up and yields the manager version. No auth
// (the greeting precedes the optional Login action). `tls` enables AMI over TLS.
type asteriskProtocol struct{}

func (asteriskProtocol) Name() string       { return "asterisk" }
func (asteriskProtocol) DefaultPort() int   { return 5038 }
func (asteriskProtocol) RequiresUser() bool { return false }

func (asteriskProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := dialDeadline(ctx, cfg, 5038)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()

	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil && line == "" {
		return Result{}, err
	}
	line = strings.TrimRight(line, "\r\n")
	version, ok := asteriskGreetingVersion(line)
	if !ok {
		return Result{}, fmt.Errorf("not an Asterisk AMI greeting: %q", line)
	}
	return Result{Version: version, Extra: map[string]string{"banner": line}}, nil
}

// asteriskGreetingVersion extracts the manager version from an AMI greeting
// ("Asterisk Call Manager/2.10.6" -> "2.10.6").
func asteriskGreetingVersion(line string) (string, bool) {
	const prefix = "Asterisk Call Manager/"
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, prefix)), true
}
