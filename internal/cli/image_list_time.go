package cli

import (
	"fmt"
	"time"
)

type imageListActivity struct {
	At      time.Time
	Unknown bool
}

// formatImageListTime uses minute precision for this year's local timestamps.
// Other years use complete calendar months, with future times marked explicitly.
func formatImageListTime(at, now time.Time) string {
	if at.IsZero() {
		return "-"
	}
	at, now = at.Local(), now.Local()
	if at.Year() == now.Year() {
		return at.Format("01-02 15:04")
	}
	start, end := at, now
	prefix := ""
	if at.After(now) {
		start, end = now, at
		prefix = "in "
	}
	months := (end.Year()-start.Year())*12 + int(end.Month()-start.Month())
	if end.Day() < start.Day() || end.Day() == start.Day() && end.Format("15:04:05.000000000") < start.Format("15:04:05.000000000") {
		months--
	}
	if months < 1 {
		return prefix + "<1mo"
	}
	years, remainder := months/12, months%12
	if years == 0 {
		return fmt.Sprintf("%s%dmo", prefix, months)
	}
	if remainder == 0 {
		return fmt.Sprintf("%s%dy", prefix, years)
	}
	return fmt.Sprintf("%s%dy%dmo", prefix, years, remainder)
}
