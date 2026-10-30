package mcpserver

import (
	"fmt"
	"time"
)

// age says how long before now t was, in the largest whole unit: "just now",
// "5 minutes ago", "3 days ago", "2 months ago", "1 year ago". A month is 30
// days and a year 365. A t after now (clock skew between processes sharing
// the database) is "just now".
func age(t, now time.Time) string {
	d := now.Sub(t)
	const day = 24 * time.Hour
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return ago(int(d/time.Minute), "minute")
	case d < day:
		return ago(int(d/time.Hour), "hour")
	case d < 30*day:
		return ago(int(d/day), "day")
	case d < 365*day:
		return ago(int(d/(30*day)), "month")
	default:
		return ago(int(d/(365*day)), "year")
	}
}

func ago(n int, unit string) string {
	if n == 1 {
		return "1 " + unit + " ago"
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}
