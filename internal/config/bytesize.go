package config

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// byteUnits are 1024-based suffixes, longest first so "kb" wins over
// "k". A bare integer is a byte count.
var byteUnits = []struct {
	suffix string
	mult   int64
}{
	{"tb", 1 << 40},
	{"gb", 1 << 30},
	{"mb", 1 << 20},
	{"kb", 1 << 10},
	{"t", 1 << 40},
	{"g", 1 << 30},
	{"m", 1 << 20},
	{"k", 1 << 10},
}

// ParseByteSize parses a non-negative size. A bare integer is bytes.
// The suffixes k/kb, m/mb, g/gb, and t/tb are case-insensitive and
// scale by 1024. Decimals and IEC suffixes such as ki are rejected.
func ParseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "+") {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	if strings.HasPrefix(s, "-") {
		return 0, errors.New("must not be negative")
	}
	number, mult := splitByteSize(strings.ToLower(s))
	if number == "" {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	n, err := strconv.ParseInt(number, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	if mult != 1 && n > math.MaxInt64/mult {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n * mult, nil
}

func splitByteSize(s string) (string, int64) {
	for _, unit := range byteUnits {
		if number, ok := strings.CutSuffix(s, unit.suffix); ok {
			return number, unit.mult
		}
	}
	return s, 1
}
