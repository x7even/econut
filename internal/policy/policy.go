// Package policy decides when the rig should shut down. It is pure: no I/O,
// no clock reads; the caller supplies samples and timestamps.
package policy

import "time"

// Sample is one reading of the UPS. Valid is false when NUT could not supply
// usable data.
type Sample struct {
	OnBattery bool
	Charge    float64
	Valid     bool
}

type Config struct {
	TriggerBelow float64
	Debounce     time.Duration
	StaleAfter   time.Duration
}

type Decision int

const (
	NoAction Decision = iota
	Shutdown
)

type Result struct {
	Decision Decision
	// StaleOnBattery is true when data has been invalid for longer than
	// StaleAfter and the last valid sample was on battery. It is a warning
	// only; it never causes a shutdown.
	StaleOnBattery bool
}

// State is the policy's memory. The zero value is ready to use.
type State struct {
	// Triggered is true after a Shutdown decision, until the trigger
	// condition stops holding. A caller whose shutdown action failed may
	// clear it to request a retry on the next sample.
	Triggered bool
	// Held is how long the trigger condition has held on consecutive valid
	// samples. Invalid samples neither advance nor reset it.
	Held time.Duration

	prevHeld           bool      // the last valid sample met the condition
	lastHeld           time.Time // when that sample was taken
	lastValid          time.Time
	lastValidOnBattery bool
}

// Step consumes one sample taken at now and returns the new state and result.
func Step(cfg Config, st State, s Sample, now time.Time) (State, Result) {
	var res Result

	if !s.Valid {
		// Invalid samples neither advance nor reset the timer; prevHeld and
		// lastHeld are kept so a brief NUT glitch does not stall the debounce.
		if st.lastValidOnBattery && now.Sub(st.lastValid) > cfg.StaleAfter {
			res.StaleOnBattery = true
		}
		return st, res
	}

	st.lastValid = now
	st.lastValidOnBattery = s.OnBattery

	if !(s.OnBattery && s.Charge < cfg.TriggerBelow) {
		st.Held, st.Triggered, st.prevHeld = 0, false, false
		return st, res
	}

	// Credit the interval since the last valid sample that met the condition
	// (bridging any invalid samples between) when both ends held, and it is
	// positive and no longer than StaleAfter. A backwards clock step or a
	// huge forward jump (suspend/resume, NTP) is treated as a gap, not as
	// time held.
	if dt := now.Sub(st.lastHeld); st.prevHeld && dt > 0 && dt <= cfg.StaleAfter {
		st.Held += dt
	}
	st.prevHeld = true
	st.lastHeld = now

	if !st.Triggered && st.Held >= cfg.Debounce {
		st.Triggered = true
		res.Decision = Shutdown
	}
	return st, res
}
