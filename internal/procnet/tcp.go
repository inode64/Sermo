package procnet

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// CountTCPConnections returns the number of established TCP sockets whose
// local port matches port. It reads both IPv4 and IPv6 kernel socket tables.
func CountTCPConnections(port int) (int, error) {
	paths := []string{PathTCP, PathTCP6}
	count := 0
	opened := 0
	var openErrs []error
	for _, path := range paths {
		f, err := os.Open(path) //nolint:gosec // G304: fixed Linux procfs socket-table path
		if err != nil {
			openErrs = append(openErrs, err)
			continue
		}
		opened++
		n, scanErr := countPortState(f, port, StateEstablished)
		closeErr := f.Close()
		if scanErr != nil {
			return 0, fmt.Errorf("read %s: %w", path, scanErr)
		}
		if closeErr != nil {
			return 0, fmt.Errorf("close %s: %w", path, closeErr)
		}
		count += n
	}
	if opened == 0 {
		return 0, fmt.Errorf("open TCP socket tables: %w", errors.Join(openErrs...))
	}
	return count, nil
}

// ScanPortState walks a procfs socket table and calls found for every row whose
// local port and state match. Returning false from found stops the scan.
func ScanPortState(r io.Reader, port int, states map[string]bool, found func(localAddress string) bool) error {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < MinFields || fields[HeaderIndex] == HeaderField {
			continue
		}
		if !states[strings.ToUpper(fields[StateIndex])] {
			continue
		}
		localAddress, portHex, ok := strings.Cut(fields[LocalAddressIndex], AddressSeparator)
		if !ok {
			continue
		}
		got, err := strconv.ParseUint(portHex, HexBase, PortBits)
		if err != nil || int(got) != port {
			continue
		}
		if !found(localAddress) {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read proc socket table: %w", err)
	}
	return nil
}

// countPortState counts entries in r matching port and state. It keeps the
// parser testable without exposing procfs file paths to callers.
func countPortState(r io.Reader, port int, state string) (int, error) {
	count := 0
	err := ScanPortState(r, port, map[string]bool{state: true}, func(string) bool {
		count++
		return true
	})
	return count, err
}
