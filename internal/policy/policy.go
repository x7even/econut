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
	ShutdownBelow float64
	Debounce      time.Duration
	StaleAfter    time.Duration
}

type Decision int

const (
	NoAction Decision = iota
	Shutdown
)

type Result struct {
	Decision Decision
	// Blind is true when this sample carried no usable reading. It is
	// instantaneous: one failed read is enough. It never causes a shutdown.
	Blind bool
	// BlindFor is how long readings have been unusable, measured from the last
	// usable reading - or, if none has ever been usable, from the first sample
	// this policy saw. Zero when this sample is usable.
	BlindFor time.Duration
	// Stale is true when BlindFor exceeds StaleAfter: no usable reading for
	// long enough to be an incident in its own right, whatever the last
	// reading happened to say. A warning only; it never causes a shutdown.
	Stale bool
	// Gap is set on the usable sample that ends a run of unusable ones: how
	// long readings were unusable, end to end. It is how a gap that is longer
	// than StaleAfter but bridged by the next reading before any single sample
	// is Stale still gets reported. Zero otherwise.
	Gap time.Duration
	// EverValid is false until this policy has consumed one valid sample.
	EverValid bool
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

	prevHeld   bool      // the last valid sample met the condition
	lastHeld   time.Time // when that sample was taken
	lastValid  time.Time // when the last usable sample was taken
	blind      bool      // the last sample carried no usable reading
	blindSince time.Time // start of the current unusable run: the last usable reading, or its own first sample
	everValid  bool      // a valid sample has been consumed
}

// Step consumes one sample taken at now and returns the new state and result.
func Step(cfg Config, st State, s Sample, now time.Time) (State, Result) {
	var res Result

	// Blindness is tracked for every sample, before any decision branch, and
	// is measured from the last usable reading, not from the first failed
	// poll: how long the UPS has been out of sight must not depend on when the
	// guard happened to poll next, and StaleAfter must mean the same interval
	// here as it does for the debounce bridge below. With no usable reading
	// ever - a guard started against a dead driver, which is also the state
	// after any restart - there is nothing to measure from but the first
	// sample. A backwards clock step re-anchors rather than producing a
	// negative duration. (time.Now carries a monotonic reading, so that
	// matters only to a wall-clock-only test clock.)
	switch {
	case s.Valid:
		if st.blind {
			if d := now.Sub(st.blindSince); d > 0 {
				res.Gap = d
			}
		}
		st.blind, st.everValid = false, true
		st.lastValid, st.blindSince = now, time.Time{}
	case !st.blind:
		st.blind, st.blindSince = true, now
		if st.everValid && st.lastValid.Before(now) {
			st.blindSince = st.lastValid
		}
	case now.Before(st.blindSince):
		st.blindSince = now
	}
	res.EverValid = st.everValid
	if st.blind {
		res.Blind = true
		if d := now.Sub(st.blindSince); d > 0 {
			res.BlindFor = d
			res.Stale = d > cfg.StaleAfter
		}
	}

	if !s.Valid {
		// Invalid samples neither advance nor reset the timer; prevHeld and
		// lastHeld are kept so a brief NUT glitch does not stall the debounce.
		return st, res
	}

	if !(s.OnBattery && s.Charge < cfg.ShutdownBelow) {
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
