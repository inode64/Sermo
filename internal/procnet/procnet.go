// Package procnet names the Linux /proc/net socket-table vocabulary.
package procnet

// Linux /proc/net socket table paths.
const (
	PathTCP  = "/proc/net/tcp"
	PathTCP6 = "/proc/net/tcp6"
	PathUDP  = "/proc/net/udp"
	PathUDP6 = "/proc/net/udp6"
)

// Linux /proc/net socket table fields and states.
const (
	HeaderField       = "sl"
	AddressSeparator  = ":"
	MinFields         = StateIndex + 1
	InodeMinFields    = InodeIndex + 1
	HeaderIndex       = 0
	LocalAddressIndex = 1
	StateIndex        = 3
	InodeIndex        = 9
	StateListen       = "0A"
	StateUDPReady     = "07"
)

// Numeric parsing constants for /proc/net encoded addresses.
const (
	HexBase          = 16
	PortBits         = 16
	IPv4HexChars     = 8
	IPv4Bits         = 32
	IPv6HexChars     = 32
	IPv6Words        = 4
	IPv6WordHexChars = 8
	IPv6WordBits     = 32

	IPv4Byte0 = 0
	IPv4Byte1 = 1
	IPv4Byte2 = 2
	IPv4Byte3 = 3
)
