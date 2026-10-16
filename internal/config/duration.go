package config

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// ParseDuration accepts a Go duration string. An integer d suffix is
// also accepted and means 24 hours, so 30d and 720h are equal.
// Negative values are rejected.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if days, ok, err := parseDays(s); ok || err != nil {
		if err != nil {
			return 0, err
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	if d < 0 {
		return 0, errors.New("must not be negative")
	}
	return d, nil
}

func parseDays(s string) (int64, bool, error) {
	number, ok := strings.CutSuffix(s, "d")
	if !ok {
		return 0, false, nil
	}
	days, err := strconv.ParseInt(number, 10, 64)
	if err != nil {
		return 0, true, fmt.Errorf("invalid duration %q", s)
	}
	if days < 0 {
		return 0, true, errors.New("must not be negative")
	}
	const day = int64(24 * time.Hour)
	if days > math.MaxInt64/day {
		return 0, true, fmt.Errorf("invalid duration %q", s)
	}
	return days, true, nil
}
