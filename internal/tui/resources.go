package tui

import (
	"fmt"

	"github.com/luiul/canopy/internal/registry"
)

// cpuCellText is the CPU column's plain-text cell value: registry's raw
// `ps` %cpu sample (registry.RegistryEntry.CPUPercent), rounded to a whole
// percent the way `top`/`ps` themselves display it. This is deliberately
// the same decaying-average number canopy's own idle/working heuristic
// has to correct for (see internal/state and registry.refineExternalStates)
// — showing that correction here instead would make the column disagree
// with what running `ps` by hand for the same pid reports, for no benefit
// to someone just glancing at "is this using CPU right now".
func cpuCellText(e registry.RegistryEntry) string {
	return fmt.Sprintf("%.0f%%", e.CPUPercent)
}

// ramCellText is the RAM column's plain-text cell value: registry's RSSKb
// (resident memory, in KB, straight from `ps`), rendered as whichever of
// M/G reads more naturally — under 1024 MB as whole megabytes, at or above
// that as gigabytes with one decimal place, matching how most system
// monitors show it. "" (not "0M") for a zero/unknown sample, the one case
// that's not a real reading (see RegistryEntry.RSSKb's own doc comment) —
// unlike CPU%, where 0% is itself a perfectly normal, common reading and
// hiding it would be actively misleading.
func ramCellText(e registry.RegistryEntry) string {
	if e.RSSKb <= 0 {
		return ""
	}
	mb := float64(e.RSSKb) / 1024
	if mb < 1024 {
		return fmt.Sprintf("%.0fM", mb)
	}
	return fmt.Sprintf("%.1fG", mb/1024)
}

// uptimeCellText is the Uptime column's plain-text cell value: how long
// this process itself has been running in total (registry.RegistryEntry.
// Uptime, straight from `ps`'s "etime"), reusing humanizeSince's compact
// duration formatting. Deliberately distinct from the Since column, which
// is time in the current State, not the process's total lifetime — a
// session idle for 5m after 3 days open reads "idle" / Since "5m" / Uptime
// "3d", each answering a different question. "" for a zero/unknown sample,
// same reasoning as ramCellText.
func uptimeCellText(e registry.RegistryEntry) string {
	if e.Uptime <= 0 {
		return ""
	}
	return humanizeSince(e.Uptime)
}
