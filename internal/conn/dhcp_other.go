//go:build !linux

package conn

import (
	"context"
	"errors"
)

// dhcpExchange is unsupported off Linux: the broadcast socket handling and the
// IP_PKTINFO egress/ingress link control the DHCP probe relies on are
// Linux-specific. The protocol still registers everywhere, so lookups and config
// validation work on any OS.
func dhcpExchange(_ context.Context, _, _ string, _ []byte, _ uint32) ([]byte, error) {
	return nil, errors.New("dhcp probe is only supported on Linux")
}
