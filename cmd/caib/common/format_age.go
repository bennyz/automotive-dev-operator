package caibcommon

import (
	"fmt"
	"math"
	"time"
)

// FormatAge converts an RFC3339 timestamp to a human-readable relative duration.
func FormatAge(timestamp string) string {
	return formatAgeFrom(timestamp, time.Now())
}

func formatAgeFrom(timestamp string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return timestamp
	}

	d := now.Sub(t)
	if d < 0 {
		return "future"
	}

	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(math.Floor(d.Hours()/24)))
	default:
		months := int(math.Floor(d.Hours() / (24 * 30)))
		if months < 12 {
			return fmt.Sprintf("%dmo", months)
		}
		return fmt.Sprintf("%dy", int(math.Floor(d.Hours()/(24*365))))
	}
}
