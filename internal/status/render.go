package status

import (
	"fmt"
	"strings"
	"time"
)

// secs renders a number of seconds as whole seconds ("120s"), matching the
// flags and docs rather than Go's "2m0s".
func secs(f float64) string { return fmt.Sprintf("%.0fs", f) }

// Render formats a snapshot as the text `econut status` prints.
func Render(s Snapshot, now time.Time) string {
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	if !s.Fresh(now) {
		line("econut-guard: NOT RUNNING (last update %s ago)", now.Sub(s.Updated).Round(time.Second))
		return b.String()
	}

	if s.Mode == ModeLive {
		line("econut-guard: running, ARMED (will power the machine off)")
	} else {
		line("econut-guard: running, dry-run (will NOT shut down)")
	}

	if !s.Valid {
		line("UPS %s: no reading (%s)", s.UPS, s.Error)
	} else {
		power := "on mains"
		if s.OnBattery {
			power = "on battery"
		}
		line("UPS %s: %s (%s), battery %.0f%%", s.UPS, power, s.UPSStatus, s.Charge)
	}

	line("Shuts down when: on battery and below %.0f%% for %s", s.TriggerBelow, secs(s.DebounceSecs))

	switch {
	case s.Triggered && s.Mode == ModeLive:
		line("Condition: MET, shutdown requested")
	case s.Triggered:
		line("Condition: MET, WOULD have shut down (dry-run)")
	case !s.Valid:
		// No condition to report without a reading.
	case s.OnBattery && s.Charge < s.TriggerBelow:
		verb := "would shut down"
		if s.Mode == ModeLive {
			verb = "shuts down"
		}
		line("Condition: MET for %s of %s, %s in %s",
			secs(s.HeldSecs), secs(s.DebounceSecs), verb, secs(max(s.DebounceSecs-s.HeldSecs, 0)))
	case s.OnBattery:
		line("Condition: not met (on battery, but %.0f%% is not below %.0f%%)", s.Charge, s.TriggerBelow)
	default:
		line("Condition: not met (on mains)")
	}
	return b.String()
}
