package procnet

import (
	"bufio"
	"fmt"
	"io"
	"sermo/internal/hostfs"
	"strconv"
	"strings"
)

// CountTCPConnections returns the number of established TCP sockets whose
// local port matches port. It reads both IPv4 and IPv6 kernel socket tables.
func CountTCPConnections(port int) (int, error) {
	return countTCPConnections(port, []string{PathTCP, PathTCP6})
}

// countTCPConnections counts established sockets from every path. A missing or
// unreadable table makes the whole observation unavailable: returning a partial
// count could authorize a guard while connections in the other address family
// remain unseen.
func countTCPConnections(port int, paths []string) (int, error) {
	count := 0
	for _, path := range paths {
		f, err := hostfs.Open(path)
		if err != nil {
			return 0, fmt.Errorf("open TCP socket table %s: %w", path, err)
		}
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
	return count, nil
}

// ScanPortState walks a procfs socket table and calls found for every row whose
// local port and state match. Returning false from found stops the scan.
func ScanPortState(r io.Reader, port int, states map[string]bool, found func(localAddress string) bool) error {
	return ScanSocketRows(r, MinFields, func(fields []string) (bool, error) {
		if !states[strings.ToUpper(fields[StateIndex])] {
			return true, nil
		}
		localAddress, portHex, ok := strings.Cut(fields[LocalAddressIndex], AddressSeparator)
		if !ok {
			return true, nil
		}
		if got, err := strconv.ParseUint(portHex, HexBase, PortBits); err == nil && int(got) == port {
			return found(localAddress), nil
		}
		return true, nil
	})
}

// ScanSocketRows visits non-header procfs socket rows with at least minFields
// columns. Consumers own protocol-specific address validation and filtering;
// returning false stops the scan, and an error rejects the observation.
func ScanSocketRows(r io.Reader, minFields int, found func([]string) (bool, error)) error {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < minFields || len(fields) == 0 || fields[HeaderIndex] == HeaderField {
			continue
		}
		more, err := found(fields)
		if err != nil {
			return fmt.Errorf("read proc socket row: %w", err)
		}
		if !more {
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
