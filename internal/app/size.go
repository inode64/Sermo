package app

import (
	"fmt"
	"strconv"
	"strings"
)

// sizeUnits maps a size suffix to its multiplier in bytes (binary units, so
// 1K = 1024). The empty suffix means raw bytes.
var sizeUnits = map[string]int64{
	"":  1,
	"B": 1,
	"K": 1 << 10,
	"M": 1 << 20,
	"G": 1 << 30,
	"T": 1 << 40,
}

// parseSize parses a human size like "5G", "500M", "1.5G" or "1024" into bytes,
// using binary units (1K = 1024). It rejects negative, empty or unitless-garbage
// input. Used for the watch `then.expand.by` field.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	unit := int64(1)
	last := s[len(s)-1]
	if last < '0' || last > '9' {
		mult, ok := sizeUnits[strings.ToUpper(string(last))]
		if !ok {
			return 0, fmt.Errorf("invalid size unit in %q", s)
		}
		unit = mult
		s = s[:len(s)-1]
	}
	s = strings.TrimSpace(s)
	val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	if val < 0 {
		return 0, fmt.Errorf("negative size %q", s)
	}
	return int64(val * float64(unit)), nil
}
