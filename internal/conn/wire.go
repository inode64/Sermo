package conn

import (
	"encoding/binary"
	"fmt"
)

// The wire helpers narrow a Go int into a protocol length or count field.
// Each reports an error instead of truncating, so a frame never carries a
// length that does not describe its payload.
const (
	wireByteMax     = 0xff
	wireUint16Max   = 0xffff
	wireUint24Max   = 0xffffff
	wireUint32Max   = 0xffffffff
	wireUint24Bytes = 3
	wireUint32Bytes = 4
)

func wireFieldError(protocol, field string, n int, maxV uint64) error {
	return fmt.Errorf("%s: %s %d does not fit the wire field (max %d)", protocol, field, n, maxV)
}

// wireByte narrows n into a one-byte field.
func wireByte(protocol, field string, n int) (byte, error) {
	if n < 0 || n > wireByteMax {
		return 0, wireFieldError(protocol, field, n, wireByteMax)
	}
	return byte(n), nil
}

// wireUint16 narrows n into a 16-bit field.
func wireUint16(protocol, field string, n int) (uint16, error) {
	if n < 0 || n > wireUint16Max {
		return 0, wireFieldError(protocol, field, n, wireUint16Max)
	}
	return uint16(n), nil
}

// wireUint24 returns the big-endian octets of n as a 24-bit field.
func wireUint24(protocol, field string, n int) ([wireUint24Bytes]byte, error) {
	var octets [wireUint24Bytes]byte
	if n < 0 || n > wireUint24Max {
		return octets, wireFieldError(protocol, field, n, wireUint24Max)
	}
	var buf [wireUint32Bytes]byte
	binary.BigEndian.PutUint32(buf[:], uint32(n))
	copy(octets[:], buf[wireUint32Bytes-wireUint24Bytes:])
	return octets, nil
}

// wireUint32 narrows n into a 32-bit field.
func wireUint32(protocol, field string, n int) (uint32, error) {
	if n < 0 || int64(n) > wireUint32Max {
		return 0, wireFieldError(protocol, field, n, wireUint32Max)
	}
	return uint32(n), nil
}
