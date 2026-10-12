package policy

import (
	"testing"
	"time"
)

var t0 = time.Unix(1_000_000, 0)

const step = 5 * time.Second

func cfg() Config {
	return Config{TriggerBelow: 55, Debounce: 120 * time.Second, StaleAfter: 300 * time.Second}
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

func TestOnlyInvalidSamplesNeverTrigger(t *testing.T) {
	_, rs, _ := feed(cfg(), State{}, repeat(bad, 500), t0)
	if got := shutdowns(rs); len(got) != 0 {
		t.Fatalf("invalid samples must never trigger, got %v", got)
	}
}

func TestStaleOnBattery(t *testing.T) {
	s := append([]Sample{ob(80)}, repeat(bad, 80)...)
	_, rs, _ := feed(cfg(), State{}, s, t0)
	for i, r := range rs {
		elapsed := time.Duration(i) * step
		want := elapsed > 300*time.Second
		if r.StaleOnBattery != want {
			t.Fatalf("sample %d (t=%s): StaleOnBattery=%v want %v", i, elapsed, r.StaleOnBattery, want)
		}
	}
}

func TestStaleNotReportedWhenLastKnownOnMains(t *testing.T) {
	s := append([]Sample{ol(100)}, repeat(bad, 200)...)
	_, rs, _ := feed(cfg(), State{}, s, t0)
	for i, r := range rs {
		if r.StaleOnBattery {
			t.Fatalf("sample %d: stale reported while last known state was on mains", i)
		}
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
