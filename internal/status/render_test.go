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

// TestRenderFailedPoweroffIsNeverArmed: the telemetry is perfect, the guard is
// live, and the one thing it exists to do did not work. Reporting ARMED here
// is the same bug as reporting it while blind.
func TestRenderFailedPoweroffIsNeverArmed(t *testing.T) {
	s := sample()
	s.Mode, s.ShutdownFailed = ModeLive, "exec: \"systemctl\": executable file not found in $PATH"
	got := Render(s, s.Updated.Add(2*time.Second))
	for _, w := range []string{"NOT PROTECTED", "power off and that failed",
		"executable file not found", "last poweroff attempt FAILED"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
	if strings.Contains(got, "ARMED") {
		t.Errorf("a guard whose poweroff fails must not claim ARMED in:\n%s", got)
	}
}

// TestRenderFailedPoweroffDoesNotPromiseAShutdown: the condition really is
// met, but "shuts down in 0s" is a promise the Action row has just said the
// machine cannot keep.
func TestRenderFailedPoweroffDoesNotPromiseAShutdown(t *testing.T) {
	base := sample()
	base.Mode, base.ShutdownFailed = ModeLive, "poweroff: command not found"
	base.OnBattery, base.Charge, base.HeldSecs = true, 40, 125

	// Triggered is the snapshot a failed poweroff actually produces: the
	// latch is set before the command runs, so the arm has to win over the
	// "shutdown requested" one, which would report the attempt as done.
	triggered := base
	triggered.Triggered = true

	for name, s := range map[string]Snapshot{"not yet triggered": base, "triggered": triggered} {
		got := Render(s, s.Updated.Add(2*time.Second))
		if !strings.Contains(got, "MET - the last poweroff attempt failed") {
			t.Errorf("%s: missing the honest condition row in:\n%s", name, got)
		}
		for _, d := range []string{"shuts down in", "would shut down", "shutdown requested"} {
			if strings.Contains(got, d) {
				t.Errorf("%s: unexpected %q in:\n%s", name, d, got)
			}
		}
	}
}

// TestRenderFailedPoweroffOutlivesTheReading: once the reading is lost the
// headline is about blindness, so the Action row is the only place left that
// can say the poweroff did not work. It must still be there.
func TestRenderFailedPoweroffOutlivesTheReading(t *testing.T) {
	s := sample()
	s.Mode, s.ShutdownFailed = ModeLive, "poweroff: command not found"
	s.Valid, s.Error, s.BlindSeconds = false, "nut: connect refused", 600
	got := Render(s, s.Updated.Add(2*time.Second))
	for _, w := range []string{"NOT PROTECTED", "no usable UPS reading", "last poweroff attempt FAILED",
		"poweroff: command not found"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

// TestRenderUninterpretableStatusIsNotMains: absence of the OB token is only
// evidence of mains power when the status says something econut understands.
// A best-fit subdriver reporting "DISCHRG" alone used to print "on mains".
func TestRenderUninterpretableStatusIsNotMains(t *testing.T) {
	for _, st := range []string{"DISCHRG", "", "TRIM", "ALARM"} {
		s := sample()
		s.Mode, s.UPSStatus, s.OnBattery = ModeLive, st, false
		got := Render(s, s.Updated.Add(2*time.Second))
		for _, w := range []string{"NOT PROTECTED", "says neither OB nor OL", "Power      unknown", "Condition  UNKNOWN"} {
			if !strings.Contains(got, w) {
				t.Errorf("status %q: missing %q in:\n%s", st, w, got)
			}
		}
		for _, d := range []string{"on mains", "ARMED", "will power the machine off"} {
			if strings.Contains(got, d) {
				t.Errorf("status %q: unexpected %q in:\n%s", st, d, got)
			}
		}
	}
	// An empty status is named, not printed as nothing at all.
	s := sample()
	s.Mode, s.UPSStatus = ModeLive, ""
	if got := Render(s, s.Updated.Add(2*time.Second)); !strings.Contains(got, "an empty ups.status") {
		t.Errorf("an empty status must be named:\n%s", got)
	}
}

// TestRenderNeverInventsADuration: a guard whose very first poll failed has no
// elapsed time to report, and "0s ago" asserted a specific fact - that it had
// just started - which during a restart mid-incident is wrong.
func TestRenderNeverInventsADuration(t *testing.T) {
	s := sample()
	s.Mode, s.Valid, s.Error = ModeLive, false, "nut: connect refused"
	s.EverValid, s.BlindSeconds = false, 0
	got := Render(s, s.Updated.Add(2*time.Second))
	if !strings.Contains(got, "no UPS reading since the guard started") {
		t.Errorf("missing the never-read phrase in:\n%s", got)
	}
	for _, d := range []string{"0s ago", "started 0s"} {
		if strings.Contains(got, d) {
			t.Errorf("unexpected %q in:\n%s", d, got)
		}
	}
}

// TestRenderOlderGuardDoesNotRecordBlindTime: reading a snapshot written by a
// build without the blind fields, both are zero. Saying "for 0s" would be a
// confident lie about a UPS this binary has no timing information for.
func TestRenderOlderGuardDoesNotRecordBlindTime(t *testing.T) {
	s := sample()
	s.Mode, s.Valid, s.Error = ModeLive, false, "nut: connect refused"
	s.EverValid, s.BlindSeconds, s.StaleAfterSecs = true, 0, 0
	got := Render(s, s.Updated.Add(2*time.Second))
	if !strings.Contains(got, "does not record") {
		t.Errorf("missing the unknown-duration phrase in:\n%s", got)
	}
	if strings.Contains(got, "for 0s") {
		t.Errorf("unexpected \"for 0s\" in:\n%s", got)
	}
}

// TestRenderFutureDatedSnapshotIsNotRunning: a clock step backwards after the
// guard wrote the file used to read as fresh, so a guard that may be long dead
// was reported as running until wall time caught up.
func TestRenderFutureDatedSnapshotIsNotRunning(t *testing.T) {
	s := sample()
	s.Mode = ModeLive
	got := Render(s, s.Updated.Add(-time.Hour))
	for _, w := range []string{"NOT RUNNING", "dated 1h0m0s in the future"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
	for _, d := range []string{"ARMED", "ago"} {
		if strings.Contains(got, d) {
			t.Errorf("unexpected %q in:\n%s", d, got)
		}
	}
}

// TestRenderUnknownModeConditionDoesNotNameAMode: with a mode this build does
// not know, the Condition row must not fall back to the dry-run verbs. The
// headline has just said that what this guard would do cannot be known, and a
// row answering it anyway is the bug class under repair.
func TestRenderUnknownModeConditionDoesNotNameAMode(t *testing.T) {
	base := sample()
	base.Mode = "bogus"
	now := base.Updated.Add(2 * time.Second)

	triggered := base
	triggered.Triggered, triggered.HeldSecs = true, 125

	onBattery := base // OB DISCHRG at 48%, below the 55% threshold
	for name, s := range map[string]Snapshot{"triggered": triggered, "condition met": onBattery} {
		got := Render(s, now)
		if !strings.Contains(got, "with an unknown mode, what the guard would do is unknown") {
			t.Errorf("%s: missing the unknown-mode condition row in:\n%s", name, got)
		}
		for _, d := range []string{"dry-run", "WOULD have shut down", "would shut down", "shuts down in"} {
			if strings.Contains(got, d) {
				t.Errorf("%s: unexpected %q in:\n%s", name, d, got)
			}
		}
	}
}
