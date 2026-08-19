package tui

import (
	"fmt"
	"time"
)

// humanizeSince renders a duration the way canopy's Since column does:
// compact, at most two units, no sub-second precision. Negative durations
// (a state that "just" changed, or a clock hiccup between two time.Now()
// reads) render as "0s" rather than going negative.
func humanizeSince(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		h := int(d / time.Hour)
		m := int((d % time.Hour) / time.Minute)
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}
