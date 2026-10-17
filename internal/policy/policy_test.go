package policy

import (
	"math/rand"
	"testing"
	"time"
)

var t0 = time.Unix(1_000_000, 0)

const step = 5 * time.Second

func cfg() Config {
	return Config{ShutdownBelow: 55, Debounce: 120 * time.Second, StaleAfter: 300 * time.Second}
}

func ob(charge float64) Sample { return Sample{OnBattery: true, Charge: charge, Valid: true} }
func ol(charge float64) Sample { return Sample{OnBattery: false, Charge: charge, Valid: true} }

var bad = Sample{}

// feed runs samples at fixed intervals starting at t0 and returns the results.
func feed(c Config, st State, samples []Sample, start time.Time) (State, []Result, time.Time) {
	var out []Result
	now := start
	for _, s := range samples {
		var r Result
		st, r = Step(c, st, s, now)
		out = append(out, r)
		now = now.Add(step)
	}
	return st, out, now
}

func repeat(s Sample, n int) []Sample {
	out := make([]Sample, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func shutdowns(rs []Result) []int {
	var idx []int
	for i, r := range rs {
		if r.Decision == Shutdown {
			idx = append(idx, i)
		}
	}
	return idx
}

func TestTriggersExactlyOnceAfterDebounce(t *testing.T) {
	_, rs, _ := feed(cfg(), State{}, repeat(ob(50), 40), t0)
	got := shutdowns(rs)
	// Sample 0 is at t=0, so 120s of hold is reached at sample 24.
	if len(got) != 1 || got[0] != 24 {
		t.Fatalf("shutdown indexes = %v, want [24]", got)
	}
}

func TestGlitchShorterThanDebounceResets(t *testing.T) {
	var s []Sample
	s = append(s, repeat(ob(50), 20)...) // 95s held
	s = append(s, ol(50))                // mains blip resets the timer
	s = append(s, repeat(ob(50), 20)...) // 95s again
	_, rs, _ := feed(cfg(), State{}, s, t0)
	if got := shutdowns(rs); len(got) != 0 {
		t.Fatalf("unexpected shutdown at %v", got)
	}
}

func TestChargeAtThresholdDoesNotTrigger(t *testing.T) {
	_, rs, _ := feed(cfg(), State{}, repeat(ob(55), 60), t0)
	if got := shutdowns(rs); len(got) != 0 {
		t.Fatalf("charge == 55 must not trigger, got %v", got)
	}
	_, rs, _ = feed(cfg(), State{}, repeat(ob(54.9), 40), t0)
	if got := shutdowns(rs); len(got) != 1 {
		t.Fatalf("charge 54.9 must trigger once, got %v", got)
	}
}

func TestOnMainsNeverTriggers(t *testing.T) {
	_, rs, _ := feed(cfg(), State{}, repeat(ol(10), 60), t0)
	if got := shutdowns(rs); len(got) != 0 {
		t.Fatalf("on mains must not trigger, got %v", got)
	}
}

func TestRearmsWhenConditionClears(t *testing.T) {
	var s []Sample
	s = append(s, repeat(ob(50), 30)...) // triggers once
	s = append(s, ol(50))                // mains back -> re-armed
	s = append(s, repeat(ob(50), 30)...) // triggers again
	_, rs, _ := feed(cfg(), State{}, s, t0)
	if got := shutdowns(rs); len(got) != 2 {
		t.Fatalf("want 2 shutdown decisions, got %v", got)
	}
}

func TestChargeRisingAboveThresholdRearms(t *testing.T) {
	var s []Sample
	s = append(s, repeat(ob(50), 30)...)
	s = append(s, ob(55)) // still on battery but not below 55
	s = append(s, repeat(ob(50), 30)...)
	_, rs, _ := feed(cfg(), State{}, s, t0)
	if got := shutdowns(rs); len(got) != 2 {
		t.Fatalf("want 2 shutdown decisions, got %v", got)
	}
}

func TestInvalidSamplesDoNotAdvanceOrResetTimer(t *testing.T) {
	var s []Sample
	s = append(s, repeat(ob(50), 10)...) // 45s held
	s = append(s, repeat(bad, 100)...)   // long gap, must neither count nor reset
	s = append(s, repeat(ob(50), 16)...) // first valid after gap adds 0, then +5 each
	_, rs, _ := feed(cfg(), State{}, s, t0)
	got := shutdowns(rs)
	// 45s + 15 steps of 5s = 120s -> the 16th sample after the gap triggers.
	want := 10 + 100 + 15
	if len(got) != 1 || got[0] != want {
		t.Fatalf("shutdown indexes = %v, want [%d]", got, want)
	}
}

func TestIntermittentInvalidSamplesStillTrigger(t *testing.T) {
	// Every other poll fails; the condition holds at every valid sample.
	var s []Sample
	for i := 0; i < 60; i++ {
		s = append(s, ob(50), bad)
	}
	_, rs, _ := feed(cfg(), State{}, s, t0)
	if got := shutdowns(rs); len(got) != 1 {
		t.Fatalf("want exactly one shutdown despite alternating failures, got %v", got)
	}
}

func TestInvalidGapThenMainsResets(t *testing.T) {
	var s []Sample
	s = append(s, repeat(ob(50), 15)...)
	s = append(s, repeat(bad, 3)...)
	s = append(s, ol(50)) // mains back after the gap: must reset
	s = append(s, repeat(ob(50), 15)...)
	_, rs, _ := feed(cfg(), State{}, s, t0)
	if got := shutdowns(rs); len(got) != 0 {
		t.Fatalf("unexpected shutdown at %v", got)
	}
}

func TestOnlyInvalidSamplesNeverTrigger(t *testing.T) {
	_, rs, _ := feed(cfg(), State{}, repeat(bad, 500), t0)
	if got := shutdowns(rs); len(got) != 0 {
		t.Fatalf("invalid samples must never trigger, got %v", got)
	}
}

func TestStaleWhileOnBatteryStillReported(t *testing.T) {
	s := append([]Sample{ob(80)}, repeat(bad, 80)...)
	_, rs, _ := feed(cfg(), State{}, s, t0)
	for i, r := range rs {
		// Blindness is measured from the last usable reading, which is
		// sample 0 here, not from the first unusable sample.
		blind := time.Duration(i) * step
		if want := i > 0; r.Blind != want {
			t.Fatalf("sample %d: Blind=%v want %v", i, r.Blind, want)
		}
		if want := i > 0 && blind > 300*time.Second; r.Stale != want {
			t.Fatalf("sample %d (blind %s): Stale=%v want %v", i, blind, r.Stale, want)
		}
	}
}

// TestBlindAfterMainsBecomesStale is the field incident: telemetry died while
// the UPS was on mains, so nothing ever escalated. Staleness must not depend on
// what the last usable reading happened to say.
func TestBlindAfterMainsBecomesStale(t *testing.T) {
	s := append([]Sample{ol(100)}, repeat(bad, 200)...)
	_, rs, _ := feed(cfg(), State{}, s, t0)
	for i, r := range rs {
		blind := time.Duration(i) * step
		if want := i > 0; r.Blind != want {
			t.Fatalf("sample %d: Blind=%v want %v", i, r.Blind, want)
		}
		if want := i > 0 && blind > 300*time.Second; r.Stale != want {
			t.Fatalf("sample %d (blind %s): Stale=%v want %v (last usable reading was on mains)", i, blind, r.Stale, want)
		}
	}
}

// TestBlindFromColdStartBecomesStale covers a guard started against a dead
// driver, which is also the state after any restart: no sample has ever been
// valid, so there is nothing to measure from but the first poll.
func TestBlindFromColdStartBecomesStale(t *testing.T) {
	_, rs, _ := feed(cfg(), State{}, repeat(bad, 500), t0)
	for i, r := range rs {
		elapsed := time.Duration(i) * step
		if !r.Blind {
			t.Fatalf("sample %d: Blind=false, no reading has ever been usable", i)
		}
		if r.EverValid {
			t.Fatalf("sample %d: EverValid=true without a single valid sample", i)
		}
		if r.BlindFor != elapsed {
			t.Fatalf("sample %d: BlindFor=%s want %s", i, r.BlindFor, elapsed)
		}
		if want := elapsed > 300*time.Second; r.Stale != want {
			t.Fatalf("sample %d (t=%s): Stale=%v want %v", i, elapsed, r.Stale, want)
		}
		if r.Decision != NoAction {
			t.Fatalf("sample %d: invalid samples must never decide to shut down", i)
		}
	}
}

// TestBlindForAnchorsAtLastUsableReading pins the clock the escalation runs on.
// Measuring from the first failed poll instead loses one poll interval, which
// made StaleAfter mean a different interval here than it does for the debounce
// bridge and left a band of repeating gaps in which the guard neither acted
// nor complained.
func TestBlindForAnchorsAtLastUsableReading(t *testing.T) {
	const firstBad = 3
	s := append(repeat(ol(100), firstBad), repeat(bad, 20)...)
	_, rs, _ := feed(cfg(), State{}, s, t0)
	for i, r := range rs {
		want := time.Duration(0)
		if i >= firstBad {
			want = time.Duration(i-(firstBad-1)) * step
		}
		if r.BlindFor != want {
			t.Fatalf("sample %d: BlindFor=%s want %s", i, r.BlindFor, want)
		}
	}
}

// TestGapIsReportedOnTheRecoveringSample covers the window BlindFor cannot
// see: readings return before any single sample has been blind for longer than
// StaleAfter, yet the gap itself was longer than StaleAfter and therefore
// longer than the debounce would bridge.
func TestGapIsReportedOnTheRecoveringSample(t *testing.T) {
	c := cfg()
	// 60 unusable samples: the last is blind for 300s, which is not more than
	// StaleAfter, so nothing is ever Stale - but the gap end to end is 305s.
	s := append(append([]Sample{ol(100)}, repeat(bad, 60)...), ol(100))
	_, rs, _ := feed(c, State{}, s, t0)
	for i, r := range rs[:len(rs)-1] {
		if r.Stale {
			t.Fatalf("sample %d: Stale=true, no single sample is blind for more than %s", i, c.StaleAfter)
		}
		if r.Gap != 0 {
			t.Fatalf("sample %d: Gap=%s, only the recovering sample reports a gap", i, r.Gap)
		}
	}
	last := rs[len(rs)-1]
	if last.Gap != 61*step {
		t.Fatalf("recovering sample: Gap=%s want %s", last.Gap, 61*step)
	}
	if last.Gap <= c.StaleAfter {
		t.Fatalf("Gap=%s must exceed StaleAfter=%s for this fixture to mean anything", last.Gap, c.StaleAfter)
	}
	if last.Blind || last.BlindFor != 0 {
		t.Fatalf("the recovering sample is not blind: %+v", last)
	}
}

// TestEveryRepeatingGapIsEitherActedOnOrReported sweeps the gap length across
// the StaleAfter boundary. A driver that flaps - one usable reading every N
// polls - must either still shut the machine down (the gap is short enough for
// the debounce to bridge) or report an incident. Neither happening is the
// silent band this fixture exists to deny: with blindness measured from the
// first failed poll and no Gap, N=61 did exactly that.
func TestEveryRepeatingGapIsEitherActedOnOrReported(t *testing.T) {
	c := cfg()
	for n := 55; n <= 70; n++ {
		var samples []Sample
		for len(samples) < 2000 {
			samples = append(samples, ob(20))
			samples = append(samples, repeat(bad, n-1)...)
		}
		_, rs, _ := feed(c, State{}, samples, t0)
		var acted, reported int
		for _, r := range rs {
			if r.Decision == Shutdown {
				acted++
			}
			if r.Stale || r.Gap > c.StaleAfter {
				reported++
			}
		}
		if acted == 0 && reported == 0 {
			t.Errorf("one usable on-battery reading every %d polls (gap %s): no shutdown and nothing reported",
				n, time.Duration(n)*step)
		}
	}
}

func TestRecoveryClearsBlindAndSecondEpisodeEscalates(t *testing.T) {
	var s []Sample
	s = append(s, ol(100))
	s = append(s, repeat(bad, 80)...) // first blind run, reaches Stale
	s = append(s, ob(80))             // one usable reading
	s = append(s, repeat(bad, 80)...) // second blind run, must reach Stale again
	_, rs, _ := feed(cfg(), State{}, s, t0)

	recovered := rs[81]
	if recovered.Blind || recovered.BlindFor != 0 || !recovered.EverValid {
		t.Fatalf("a usable reading must clear blindness: %+v", recovered)
	}
	if recovered.Gap != 81*step {
		t.Fatalf("the recovering sample must report the whole gap, got %s want %s", recovered.Gap, 81*step)
	}
	if !rs[80].Stale {
		t.Fatal("first blind run never reached Stale")
	}
	if !rs[len(rs)-1].Stale {
		t.Fatal("second blind run never reached Stale; escalation must not be once per process")
	}
	for i, r := range rs {
		if r.Decision != NoAction {
			t.Fatalf("sample %d: no sample in this sequence may trigger", i)
		}
	}
}

func TestBlindBackwardsClockDoesNotGoNegative(t *testing.T) {
	c := cfg()
	st := State{}
	now := t0
	for i := 0; i < 10; i++ {
		st, _ = Step(c, st, bad, now)
		now = now.Add(step)
	}
	// NTP steps the clock back an hour mid-outage: the anchor moves to the
	// earlier instant rather than saturating or going negative.
	st, r := Step(c, st, bad, now.Add(-time.Hour))
	if !r.Blind {
		t.Fatal("still blind after a backwards clock step")
	}
	if r.BlindFor < 0 {
		t.Fatalf("BlindFor = %s, must never be negative", r.BlindFor)
	}
	if _, r = Step(c, st, bad, now.Add(-time.Hour+step)); r.BlindFor != step {
		t.Fatalf("after re-anchoring, BlindFor = %s, want %s", r.BlindFor, step)
	}
}

func TestEverValidFalseUntilFirstValidSample(t *testing.T) {
	s := append(repeat(bad, 5), ol(100), bad)
	_, rs, _ := feed(cfg(), State{}, s, t0)
	for i, r := range rs {
		if want := i >= 5; r.EverValid != want {
			t.Fatalf("sample %d: EverValid=%v want %v", i, r.EverValid, want)
		}
	}
}

// TestBlindNeverTriggersShutdown is the regression guard for the property the
// whole fix rests on: losing sight of the UPS reports an incident and nothing
// more. Shutting down "to be safe" when blind would power the machine off on a
// USB re-enumeration or a NUT restart while mains is perfectly healthy.
func TestBlindNeverTriggersShutdown(t *testing.T) {
	cases := map[string][]Sample{
		"blind after mains": append([]Sample{ol(100)}, repeat(bad, 200)...),
		"blind from start":  repeat(bad, 500),
		"blind after battery, then blind again": append(
			append(append([]Sample{ob(50)}, repeat(bad, 80)...), ob(50)), repeat(bad, 80)...),
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			_, rs, _ := feed(cfg(), State{}, s, t0)
			if got := shutdowns(rs); len(got) != 0 {
				t.Fatalf("blindness must never shut the machine down, got decisions at %v", got)
			}
		})
	}
}

// oldState and oldStep are a verbatim copy of the decision logic before
// blindness tracking was added, kept only so TestDecisionPathUnchanged can
// prove that the change did not alter when the machine powers off.
type oldState struct {
	Triggered          bool
	Held               time.Duration
	prevHeld           bool
	lastHeld           time.Time
	lastValid          time.Time
	lastValidOnBattery bool
}

func oldStep(cfg Config, st oldState, s Sample, now time.Time) (oldState, Decision) {
	if !s.Valid {
		return st, NoAction
	}
	st.lastValid = now
	st.lastValidOnBattery = s.OnBattery

	if !(s.OnBattery && s.Charge < cfg.ShutdownBelow) {
		st.Held, st.Triggered, st.prevHeld = 0, false, false
		return st, NoAction
	}
	if dt := now.Sub(st.lastHeld); st.prevHeld && dt > 0 && dt <= cfg.StaleAfter {
		st.Held += dt
	}
	st.prevHeld = true
	st.lastHeld = now

	if !st.Triggered && st.Held >= cfg.Debounce {
		st.Triggered = true
		return st, Shutdown
	}
	return st, NoAction
}

// TestDecisionPathUnchanged drives the old and new logic in lockstep and
// requires Decision, Held and Triggered to agree at every single step, so the
// blindness work provably did not change when the machine powers off.
//
// The sample stream runs in regimes rather than being drawn independently per
// step: reaching the trigger takes ~24 consecutive low on-battery samples, so
// an i.i.d. stream never got there and this test compared only the NoAction
// path. The counters at the end fail the test if it ever goes vacuous again.
func TestDecisionPathUnchanged(t *testing.T) {
	c := cfg()
	rng := rand.New(rand.NewSource(1))
	var (
		newSt State
		oldSt oldState
	)
	now := t0
	var acted, triggeredSteps, heldAtDebounce, bridgeBoundary int

	const (
		outage = iota // on battery below the threshold, with the odd failed read
		mains
		nearThreshold // charges either side of 55
		flapping
		blind
		regimes
	)
	regime, left := mains, 1
	for i := 0; i < 20000; i++ {
		if left == 0 {
			regime, left = rng.Intn(regimes), 1+rng.Intn(60)
		}
		left--

		var s Sample
		switch regime {
		case outage:
			s = ob(40 + rng.Float64()*14)
			if rng.Intn(25) == 0 {
				s = bad
			}
		case mains:
			s = ol(60 + rng.Float64()*40)
		case nearThreshold:
			s = ob(54 + rng.Float64()*2)
		case flapping:
			if rng.Intn(2) == 0 {
				s = ob(30)
			} else {
				s = ol(90)
			}
		default:
			s = bad
		}

		switch rng.Intn(14) {
		case 0:
			now = now.Add(-time.Duration(rng.Intn(600)) * time.Second) // NTP step back
		case 1:
			now = now.Add(time.Duration(rng.Intn(4)) * time.Hour) // suspend/resume
		case 2:
			now = now.Add(c.StaleAfter) // exactly the longest gap the debounce bridges
		case 3:
			now = now.Add(c.StaleAfter + time.Second) // one second too long
		case 4:
			now = now.Add(c.Debounce)
		default:
			now = now.Add(step)
		}

		// Measured on the old state, before the step, so the coverage
		// assertions describe what was really exercised.
		condition := s.Valid && s.OnBattery && s.Charge < c.ShutdownBelow
		if dt := now.Sub(oldSt.lastHeld); oldSt.prevHeld && condition && dt == c.StaleAfter {
			bridgeBoundary++
		}

		var res Result
		var oldDec Decision
		newSt, res = Step(c, newSt, s, now)
		oldSt, oldDec = oldStep(c, oldSt, s, now)

		if res.Decision != oldDec {
			t.Fatalf("step %d: Decision %v, old logic said %v", i, res.Decision, oldDec)
		}
		if newSt.Held != oldSt.Held {
			t.Fatalf("step %d: Held %s, old logic said %s", i, newSt.Held, oldSt.Held)
		}
		if newSt.Triggered != oldSt.Triggered {
			t.Fatalf("step %d: Triggered %v, old logic said %v", i, newSt.Triggered, oldSt.Triggered)
		}

		if res.Decision == Shutdown {
			acted++
		}
		if newSt.Triggered {
			triggeredSteps++
		}
		if newSt.Held == c.Debounce {
			heldAtDebounce++
		}
	}

	// A lockstep comparison proves nothing about a branch it never enters.
	if acted < 20 {
		t.Errorf("only %d shutdown decisions: the walk barely reaches the branch it exists to compare", acted)
	}
	if triggeredSteps == 0 {
		t.Error("Triggered was false at every step, so comparing it was a tautology")
	}
	if heldAtDebounce == 0 {
		t.Errorf("Held never equalled the debounce exactly, so the >= boundary was never compared")
	}
	if bridgeBoundary == 0 {
		t.Errorf("a gap of exactly StaleAfter never met the bridge, so its <= boundary was never compared")
	}
}

func TestHugeForwardJumpIsTreatedAsGap(t *testing.T) {
	c := cfg()
	st := State{}
	now := t0
	for i := 0; i < 10; i++ { // 45s held
		st, _ = Step(c, st, ob(50), now)
		now = now.Add(step)
	}
	held := st.Held
	st, r := Step(c, st, ob(50), now.Add(3*time.Hour)) // resume from suspend
	if r.Decision == Shutdown {
		t.Fatal("a 3h forward jump must not count as 3h of held condition")
	}
	if st.Held != held {
		t.Fatalf("Held changed across the jump: %s -> %s", held, st.Held)
	}
}

func TestClockGoingBackwardsDoesNotShrinkOrTrigger(t *testing.T) {
	c := cfg()
	st := State{}
	now := t0
	for i := 0; i < 10; i++ { // 45s held
		st, _ = Step(c, st, ob(50), now)
		now = now.Add(step)
	}
	held := st.Held
	st, r := Step(c, st, ob(50), now.Add(-time.Hour)) // NTP step back
	if r.Decision == Shutdown {
		t.Fatal("clock step backwards must not trigger")
	}
	if st.Held < held {
		t.Fatalf("Held shrank from %s to %s", held, st.Held)
	}
	if st.Held > held {
		t.Fatalf("Held grew across a backwards step: %s -> %s", held, st.Held)
	}
}
