package status

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// secs renders a number of seconds as whole seconds ("120s"), matching the
// flags and docs rather than Go's "2m0s".
func secs(f float64) string { return fmt.Sprintf("%.0fs", f) }

const barWidth = 20

// bar draws a charge percentage as a fixed-width block bar.
func bar(charge float64) string {
	n := int(math.Round(charge / 100 * barWidth))
	n = max(0, min(barWidth, n))
	return strings.Repeat("█", n) + strings.Repeat("░", barWidth-n)
}

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	cyan   = "\x1b[36m"
)

// Render formats a snapshot as the plain text `econut status` prints.
func Render(s Snapshot, now time.Time) string { return RenderColor(s, now, false) }

// RenderColor is Render with ANSI colours when color is true (for terminals).
func RenderColor(s Snapshot, now time.Time, color bool) string {
	paint := func(code, text string) string {
		if !color {
			return text
		}
		return code + text + reset
	}
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	row := func(label, value string) { line("  %s %s", paint(dim, fmt.Sprintf("%-10s", label)), value) }

	if !s.Fresh(now) {
		line("%s econut-guard: %s (last update %s ago)", paint(red, "○"), paint(red+bold, "NOT RUNNING"),
			now.Sub(s.Updated).Round(time.Second))
		return b.String()
	}

	live := s.Mode == ModeLive
	if live {
		line("%s econut-guard: running, %s (will power the machine off)", paint(green, "●"), paint(yellow+bold, "ARMED"))
	} else {
		line("%s econut-guard: running, %s (will NOT shut down)", paint(cyan, "●"), paint(cyan+bold, "dry-run"))
	}
	line("")

	row("UPS", s.UPS)
	if !s.Valid {
		row("Power", paint(red, fmt.Sprintf("no reading (%s)", s.Error)))
	} else {
		if s.OnBattery {
			row("Power", paint(yellow+bold, "on battery")+" ("+s.UPSStatus+")")
		} else {
			row("Power", paint(green, "on mains")+" ("+s.UPSStatus+")")
		}
		shade := green
		switch {
		case s.Charge < s.ShutdownBelow:
			shade = red
		case s.Charge < s.ShutdownBelow+15:
			shade = yellow
		}
		row("Battery", fmt.Sprintf("%s %s", paint(shade, bar(s.Charge)), paint(shade, fmt.Sprintf("%.0f%%", s.Charge))))
	}
	row("Shutdown", fmt.Sprintf("when on battery and below %.0f%% for %s", s.ShutdownBelow, secs(s.DebounceSecs)))

	switch {
	case s.Triggered && live:
		row("Condition", paint(red+bold, "MET, shutdown requested"))
	case s.Triggered:
		row("Condition", paint(red, "MET, WOULD have shut down (dry-run)"))
	case !s.Valid:
		// No condition to report without a reading.
	case s.OnBattery && s.Charge < s.ShutdownBelow:
		verb := "would shut down"
		if live {
			verb = "shuts down"
		}
		row("Condition", paint(red, fmt.Sprintf("MET for %s of %s, %s in %s",
			secs(s.HeldSecs), secs(s.DebounceSecs), verb, secs(max(s.DebounceSecs-s.HeldSecs, 0)))))
	case s.OnBattery:
		row("Condition", fmt.Sprintf("not met (on battery, but %.0f%% is not below %.0f%%)", s.Charge, s.ShutdownBelow))
	default:
		row("Condition", "not met (on mains)")
	}
	return b.String()
}
