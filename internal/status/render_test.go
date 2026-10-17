package status

import (
	"strings"
	"testing"
	"time"
)

func TestRender(t *testing.T) {
	base := sample() // dry-run, OB DISCHRG, 48%, held 35s of 120s, trigger 55
	now := base.Updated.Add(2 * time.Second)

	mains := base
	mains.UPSStatus, mains.OnBattery, mains.Charge, mains.HeldSecs = "OL CHRG", false, 99, 0

	highBatt := base
	highBatt.Charge, highBatt.HeldSecs = 80, 0

	enabled := base
	enabled.Mode = ModeLive

	triggered := base
	triggered.Triggered, triggered.HeldSecs = true, 125

	triggeredEnabled := triggered
	triggeredEnabled.Mode = ModeLive

	noRead := base
	noRead.Valid, noRead.Error = false, "nut: connect refused"

	cases := []struct {
		name string
		s    Snapshot
		now  time.Time
		want []string
		deny []string
	}{
		{"mains", mains, now,
			[]string{"running", "dry-run", "will NOT shut down", "on mains", "99%", "below 55%", "120s", "Condition  not met"},
			[]string{"NOT RUNNING"}},
		{"on battery above level", highBatt, now,
			[]string{"on battery", "80%", "Condition  not met", "not below 55%"}, nil},
		{"counting down", base, now,
			[]string{"on battery", "48%", "Condition  MET", "35s of 120s", "85s"}, nil},
		{"armed for real", enabled, now,
			[]string{"running, ARMED", "will power the machine off", "shuts down in 85s"}, []string{"will NOT", "dry-run"}},
		{"dry-run triggered", triggered, now,
			[]string{"WOULD have shut down"}, nil},
		{"real triggered", triggeredEnabled, now,
			[]string{"shutdown requested"}, nil},
		{"no reading", noRead, now,
			[]string{"no reading", "connect refused"}, []string{"ARMED"}},
		{"guard gone quiet", base, base.Updated.Add(90 * time.Second),
			[]string{"NOT RUNNING", "1m30s ago"}, []string{"Condition"}},
	}
	t.Run("layout", func(t *testing.T) {
		got := Render(mains, now)
		for _, w := range []string{"● econut-guard: running", "UPS        <ups>", "Power      on mains (OL CHRG)",
			"Battery    ████████████████████ 99%", "Shutdown   when on battery and below 55% for 120s", "Condition  not met"} {
			if !strings.Contains(got, w) {
				t.Errorf("missing %q in:\n%s", w, got)
			}
		}
		if strings.Contains(got, "\x1b[") {
			t.Errorf("Render must not emit colour codes:\n%q", got)
		}
	})
	t.Run("bar edges", func(t *testing.T) {
		for charge, want := range map[float64]string{0: "░░░░░░░░░░░░░░░░░░░░", 100: "████████████████████", 48: "██████████░░░░░░░░░░"} {
			if got := bar(charge); got != want {
				t.Errorf("bar(%v) = %q, want %q", charge, got, want)
			}
		}
	})
	t.Run("colour", func(t *testing.T) {
		got := RenderColor(mains, now, true)
		if !strings.Contains(got, "\x1b[") || !strings.Contains(got, "running") {
			t.Errorf("expected colour codes:\n%q", got)
		}
		if RenderColor(mains, now, false) != Render(mains, now) {
			t.Error("RenderColor(false) must equal Render")
		}
	})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Render(c.s, c.now)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, d := range c.deny {
				if strings.Contains(got, d) {
					t.Errorf("unexpected %q in:\n%s", d, got)
				}
			}
		})
	}
}

// TestRenderLiveBlindNeverSaysArmed is the field incident verbatim: the guard
// was live and running, had not had a usable reading for days, and printed a
// green dot and a bold ARMED the whole time.
func TestRenderLiveBlindNeverSaysArmed(t *testing.T) {
	s := sample()
	s.Mode, s.Valid, s.Error = ModeLive, false, "nut: <ups> ups.status: DRIVER-NOT-CONNECTED"
	s.BlindSeconds = 302400 // 84h
	got := Render(s, s.Updated.Add(2*time.Second))
	for _, w := range []string{"NOT PROTECTED", "no usable UPS reading", "84h0m0s",
		"Battery    unknown", "Condition  UNKNOWN"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
	for _, d := range []string{"ARMED", "will power the machine off", "will NOT"} {
		if strings.Contains(got, d) {
			t.Errorf("a guard that cannot see the UPS must not claim %q in:\n%s", d, got)
		}
	}
}

func TestRenderBlindNeverSeen(t *testing.T) {
	s := sample()
	s.Mode, s.Valid, s.Error = ModeLive, false, "nut: connect refused"
	s.EverValid, s.BlindSeconds = false, 250
	got := Render(s, s.Updated.Add(2*time.Second))
	for _, w := range []string{"NOT PROTECTED", "since the guard started", "4m10s"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
	if strings.Contains(got, "no usable UPS reading for") {
		t.Errorf("a guard that never read the UPS has no reading to date from:\n%s", got)
	}
}

func TestRenderBlindDryRunNamesBoth(t *testing.T) {
	s := sample() // dry-run
	s.Valid, s.Error, s.BlindSeconds = false, "nut: connect refused", 600
	got := Render(s, s.Updated.Add(2*time.Second))
	for _, w := range []string{"NOT PROTECTED", "dry-run"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
	if strings.Contains(got, "ARMED") {
		t.Errorf("unexpected ARMED in:\n%s", got)
	}
}

// TestRenderTriggeredWhileBlind pins the switch reorder: policy state frozen
// before the reading was lost is disclosed as history, never as the condition
// now holding.
func TestRenderTriggeredWhileBlind(t *testing.T) {
	s := sample()
	s.Mode, s.Valid, s.Error = ModeLive, false, "nut: connect refused"
	s.Triggered, s.HeldSecs, s.BlindSeconds = true, 125, 600
	got := Render(s, s.Updated.Add(2*time.Second))
	for _, w := range []string{"Condition  UNKNOWN", "a shutdown was requested before"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
	for _, d := range []string{"MET, shutdown requested", "125s"} {
		if strings.Contains(got, d) {
			t.Errorf("stale policy state presented as current: %q in:\n%s", d, got)
		}
	}
}

func TestRenderUnknownModeIsNotDryRun(t *testing.T) {
	s := sample()
	s.Mode = "bogus"
	got := Render(s, s.Updated.Add(2*time.Second))
	for _, w := range []string{"NOT PROTECTED", "unknown mode"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
	for _, d := range []string{"will NOT shut down", "ARMED"} {
		if strings.Contains(got, d) {
			t.Errorf("unexpected %q in:\n%s", d, got)
		}
	}
}

// TestRenderAlwaysPrintsCondition: silence is how the most alarming fact used
// to be expressed, so every running guard now reports a condition.
func TestRenderAlwaysPrintsCondition(t *testing.T) {
	base := sample()
	now := base.Updated.Add(2 * time.Second)
	for _, mode := range []string{ModeLive, ModeDryRun, "bogus"} {
		for _, valid := range []bool{true, false} {
			s := base
			s.Mode, s.Valid = mode, valid
			if !valid {
				s.Error = "nut: connect refused"
			}
			if got := Render(s, now); !strings.Contains(got, "Condition") {
				t.Errorf("mode=%q valid=%v: no Condition row in:\n%s", mode, valid, got)
			}
		}
	}
}
