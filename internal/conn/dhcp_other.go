//go:build !linux

package conn

import (
	"context"
	"errors"
)

// dhcpExchange is unsupported off Linux: the broadcast and SO_BINDTODEVICE
// socket handling the DHCP probe relies on is Linux-specific. The protocol
// still registers everywhere, so lookups and config validation work on any OS.
func dhcpExchange(_ context.Context, _, _ string, _ []byte, _ uint32) ([]byte, error) {
	return nil, errors.New("dhcp probe is only supported on Linux")
}
