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

// BlindPhrase names how long the guard has been unable to see the UPS. Both
// the status output and econut check word it the same way.
func (s Snapshot) BlindPhrase() string { long, _ := s.blindWords(); return long }

// blindWords is the headline phrase and the shorter Power-row phrase for a
// snapshot with no usable reading. A duration is only ever printed when one is
// actually known: a guard older than these fields records none at all, and one
// whose very first poll failed has none to report yet. Printing "0s ago" for
// either asserted a specific fact - a guard that just started - that is
// unknown and, during a restart mid-incident, wrong.
func (s Snapshot) blindWords() (long, short string) {
	d := s.blindFor()
	switch {
	case s.StaleAfterSecs == 0 && s.BlindSeconds == 0:
		return "no usable UPS reading (this guard does not record for how long)",
			"no reading (for how long is not recorded)"
	case !s.EverValid && d == 0:
		return "no UPS reading since the guard started", "no reading since the guard started"
	case !s.EverValid:
		return fmt.Sprintf("no UPS reading since the guard started %s ago", d),
			fmt.Sprintf("no reading since the guard started %s ago", d)
	default:
		return fmt.Sprintf("no usable UPS reading for %s", d), fmt.Sprintf("no reading for %s", d)
	}
}

// dryRunNote names a dry-run guard that is also faulty, so one fault does not
// hide the other.
func (s Snapshot) dryRunNote() string {
	if s.Mode == ModeDryRun {
		return ", and it is in dry-run"
	}
	return ""
}

// blindFor is BlindSeconds as a whole-second duration.
func (s Snapshot) blindFor() time.Duration {
	return time.Duration(s.BlindSeconds * float64(time.Second)).Round(time.Second)
}

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

	// One judgement, computed once: the headline, the rows and the exit code
	// must not be able to disagree about whether the machine is protected.
	p := s.Protection(now)
	if p == NotRunning {
		// A snapshot dated ahead of the clock is reported as what it is,
		// rather than as a negative age ("last update -1h0m0s ago").
		when := fmt.Sprintf("last update %s ago", now.Sub(s.Updated).Round(time.Second))
		if s.Updated.After(now) {
			when = fmt.Sprintf("the status file is dated %s in the future", s.Updated.Sub(now).Round(time.Second))
		}
		line("%s econut-guard: %s (%s)", paint(red, "○"), paint(red+bold, "NOT RUNNING"), when)
		return b.String()
	}

	live := s.Mode == ModeLive // still needed by the Condition verbs
	bad := func(format string, a ...any) {
		line("%s econut-guard: running, %s (%s)", paint(red, "●"), paint(red+bold, "NOT PROTECTED"),
			fmt.Sprintf(format, a...))
	}

	// Every fault is named as a fault, with neither "ARMED" nor any promise to
	// act, so that no fragment of the line can reassure anyone. There is an
	// arm per state and no catch-all that guesses, because guessing a state's
	// wording is the bug class this output exists to avoid.
	switch p {
	case Protected:
		line("%s econut-guard: running, %s (will power the machine off)", paint(green, "●"), paint(yellow+bold, "ARMED"))
	case DryRun:
		line("%s econut-guard: running, %s (will NOT shut down)", paint(cyan, "●"), paint(cyan+bold, "dry-run"))
	case Blind:
		bad("%s; a low battery would not be noticed%s", s.BlindPhrase(), s.dryRunNote())
	case ActionFailed:
		bad("it asked the machine to power off and that failed: %s", s.ShutdownFailed)
	case UnknownStatus:
		bad("the UPS reports %s, which says neither OB nor OL, so whether it is on battery is unknown%s",
			quoteStatus(s.UPSStatus), s.dryRunNote())
	case UnknownMode:
		bad("unknown mode %q; cannot tell whether it will shut the machine off", s.Mode)
	default:
		bad("unrecognised protection state %q", p)
	}
	line("")

	row("UPS", s.UPS)
	if !s.Valid {
		_, what := s.blindWords()
		row("Power", paint(red, fmt.Sprintf("%s (%s)", what, s.Error)))
		// Always printed: when the reading is lost, saying nothing here is how
		// the most alarming fact used to be expressed.
		row("Battery", paint(dim, "unknown"))
	} else {
		if !StatusKnown(s.UPSStatus) {
			// Never "on mains": absence of the OB token is only evidence of
			// mains power when the status says something econut understands.
			row("Power", paint(red, fmt.Sprintf("unknown (%s)", quoteStatus(s.UPSStatus))))
		} else if s.OnBattery {
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
	if s.ShutdownFailed != "" {
		// Printed whichever state won above, so a telemetry fault that
		// arrives later cannot hide a poweroff that does not work.
		row("Action", paint(red+bold, "last poweroff attempt FAILED: "+s.ShutdownFailed))
	}

	// Validity is tested first: policy state frozen before the reading was
	// lost must never be presented as the current condition.
	switch {
	case !s.Valid:
		msg := "UNKNOWN - nothing is watching the battery"
		if s.Triggered {
			msg += " (a shutdown was requested before the reading was lost)"
		}
		row("Condition", paint(red, msg))
	case p == ActionFailed:
		// The condition may well be met, but "shuts down in 0s" is a promise
		// the Action row above has just said the machine cannot keep.
		met := "not met"
		if s.Triggered || (s.OnBattery && s.Charge < s.ShutdownBelow) {
			met = "MET"
		}
		row("Condition", paint(red, met+" - the last poweroff attempt failed; the guard retries every poll"))
	case p == UnknownStatus:
		row("Condition", paint(red, fmt.Sprintf("UNKNOWN - %s does not say whether the UPS is on battery",
			quoteStatus(s.UPSStatus))))
	case p == UnknownMode:
		// No dry-run or live verb here: the headline has just said that what
		// this guard would do cannot be known.
		met := "not met"
		if s.Triggered || (s.OnBattery && s.Charge < s.ShutdownBelow) {
			met = "MET"
		}
		row("Condition", paint(red, met+" - with an unknown mode, what the guard would do is unknown"))
	case s.Triggered && live:
		row("Condition", paint(red+bold, "MET, shutdown requested"))
	case s.Triggered:
		row("Condition", paint(red, "MET, WOULD have shut down (dry-run)"))
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

// quoteStatus quotes a ups.status value for a sentence, naming an empty one
// rather than printing nothing at all.
func quoteStatus(upsStatus string) string {
	if upsStatus == "" {
		return "an empty ups.status"
	}
	return fmt.Sprintf("%q", upsStatus)
}
