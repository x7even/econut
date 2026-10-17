package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/x7even/econut/internal/status"
)

func checkCmd(t *testing.T, now time.Time, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = runCheck(args, &o, &e, now)
	return code, o.String(), e.String()
}

func TestCheckExitCodes(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	later := now.Add(time.Second)

	bogus := liveSnap(now)
	bogus.Mode = "bogus"

	blindDry := blindSnap(now)
	blindDry.Mode = status.ModeDryRun

	cannotAct := liveSnap(now)
	cannotAct.ShutdownFailed = "exec: \"systemctl\": executable file not found in $PATH"

	cannotSee := liveSnap(now)
	cannotSee.UPSStatus, cannotSee.OnBattery = "DISCHRG", false

	cannotSeeDry := cannotSee
	cannotSeeDry.Mode = status.ModeDryRun

	emptyStatus := liveSnap(now)
	emptyStatus.UPSStatus = ""

	cases := []struct {
		name string
		args []string
		now  time.Time
		want int
		says string
	}{
		{"protected", []string{"--file", writeSnap(t, liveSnap(now))}, later, 0, "protected (on mains, 99%)"},
		{"blind", []string{"--file", writeSnap(t, blindSnap(now))}, later, 3, "NOT PROTECTED - no usable UPS reading"},
		{"unknown mode", []string{"--file", writeSnap(t, bogus)}, later, 3, `NOT PROTECTED - unknown mode "bogus"`},
		{"dry-run", []string{"--file", writeSnap(t, snap(now))}, later, 4, "NOT PROTECTED - dry-run"},
		{"dry-run allowed", []string{"--allow-dry-run", "--file", writeSnap(t, snap(now))}, later, 0, "dry-run (allowed"},
		// A dry-run guard that cannot see the UPS is measuring nothing;
		// --allow-dry-run must not launder a dead driver into exit 0.
		{"blind dry-run is still a fault", []string{"--allow-dry-run", "--file", writeSnap(t, blindDry)}, later, 3, "NOT PROTECTED"},
		// A guard that can see the UPS but could not power the machine off is
		// not protecting it: the exit code a harness gates on must say so.
		{"poweroff failed", []string{"--file", writeSnap(t, cannotAct)}, later, 3, "NOT PROTECTED - the last poweroff attempt failed"},
		// The reading arrived and says neither OB nor OL, so "not on battery"
		// is not a fact about this UPS.
		{"uninterpretable status", []string{"--file", writeSnap(t, cannotSee)}, later, 3, `NOT PROTECTED - the UPS reports "DISCHRG"`},
		{"uninterpretable status is not laundered", []string{"--allow-dry-run", "--file", writeSnap(t, cannotSeeDry)}, later, 3, "NOT PROTECTED - the UPS reports"},
		{"empty status", []string{"--file", writeSnap(t, emptyStatus)}, later, 3, "an empty ups.status"},
		// The clock stepped back after the guard wrote the file: its age says
		// nothing, so a possibly dead guard must not read as a live one.
		{"future-dated snapshot", []string{"--file", writeSnap(t, liveSnap(now))}, now.Add(-time.Hour), 1, "dated 1h0m0s in the future"},
		{"negative max-age", []string{"--max-age", "-5s", "--file", writeSnap(t, liveSnap(now))}, later, 2, "must not be negative"},
		{"stricter max-age", []string{"--max-age", "1s", "--file", writeSnap(t, liveSnap(now))}, now.Add(10 * time.Second), 1, "NOT RUNNING - last update"},
		// 10s is below the snapshot's own 15s window (3 x 5s poll), so the
		// flag is really in play: 8s old passes, 11s old does not. A 30s
		// value would have been inert and tested nothing.
		{"max-age satisfied", []string{"--max-age", "10s", "--file", writeSnap(t, liveSnap(now))}, now.Add(8 * time.Second), 0, "protected"},
		{"max-age just exceeded", []string{"--max-age", "10s", "--file", writeSnap(t, liveSnap(now))}, now.Add(11 * time.Second), 1, "NOT RUNNING - last update 11s ago"},
		// A --max-age longer than the guard's own window can only ever be
		// stricter, never looser: it cannot revive a dead guard.
		{"max-age cannot loosen the window", []string{"--max-age", "1h", "--file", writeSnap(t, liveSnap(now))}, now.Add(time.Hour - time.Second), 1, "NOT RUNNING"},
		{"missing file", []string{"--file", filepath.Join(t.TempDir(), "none.json")}, later, 1, "NOT RUNNING - no status file"},
		{"bad flag", []string{"--nope"}, later, 2, ""},
		{"unexpected argument", []string{"extra"}, later, 2, "unexpected argument"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := checkCmd(t, c.now, c.args...)
			if code != c.want {
				t.Fatalf("code=%d want %d\nstdout: %s\nstderr: %s", code, c.want, out, errOut)
			}
			if c.says != "" && !strings.Contains(out+errOut, c.says) {
				t.Fatalf("missing %q in stdout %q / stderr %q", c.says, out, errOut)
			}
			// The answer goes to stdout only when it is yes, so a cron job's
			// mail carries the problem and nothing else.
			if code == 0 && errOut != "" {
				t.Errorf("exit 0 wrote to stderr: %q", errOut)
			}
		})
	}
}

func TestCheckQuietPrintsNothing(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	for _, s := range []status.Snapshot{liveSnap(now), blindSnap(now), snap(now)} {
		code, out, errOut := checkCmd(t, now.Add(time.Second), "--quiet", "--file", writeSnap(t, s))
		if out != "" || errOut != "" {
			t.Errorf("--quiet (exit %d) printed stdout %q stderr %q", code, out, errOut)
		}
	}
}

// TestCheckAndStatusAgree makes divergence between the two commands a test
// failure: check is a presentation layer over the same verdict.
func TestCheckAndStatusAgree(t *testing.T) {
	now := time.Unix(1_000_000, 0)

	bogus := liveSnap(now)
	bogus.Mode = "bogus"
	neverRead := blindSnap(now)
	neverRead.EverValid, neverRead.BlindSeconds = false, 250
	triggered := liveSnap(now)
	triggered.Triggered, triggered.OnBattery, triggered.Charge = true, true, 40
	cannotAct := liveSnap(now)
	cannotAct.ShutdownFailed = "poweroff: command not found"
	cannotSee := liveSnap(now)
	cannotSee.UPSStatus = "DISCHRG"
	blindAndCannotAct := blindSnap(now)
	blindAndCannotAct.ShutdownFailed = "poweroff: command not found"

	cases := map[string]struct {
		s   status.Snapshot
		now time.Time
	}{
		"protected":            {liveSnap(now), now.Add(time.Second)},
		"triggered":            {triggered, now.Add(time.Second)},
		"blind":                {blindSnap(now), now.Add(time.Second)},
		"never read":           {neverRead, now.Add(time.Second)},
		"unknown":              {bogus, now.Add(time.Second)},
		"dry-run":              {snap(now), now.Add(time.Second)},
		"not running":          {liveSnap(now), now.Add(time.Hour)},
		"future dated":         {liveSnap(now), now.Add(-time.Hour)},
		"cannot act":           {cannotAct, now.Add(time.Second)},
		"cannot see":           {cannotSee, now.Add(time.Second)},
		"blind and cannot act": {blindAndCannotAct, now.Add(time.Second)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeSnap(t, c.s)
			statusCode, _, _ := runCmd(t, c.now, "status", "--file", path)
			checkCode, _, _ := checkCmd(t, c.now, "--file", path)
			if statusCode != checkCode {
				t.Fatalf("econut status exits %d but econut check exits %d", statusCode, checkCode)
			}
		})
	}
}

// TestCheckSaysWhenAShutdownIsAlreadyRequested: exit 0 is the contract - the
// guard is protecting the machine and is in the middle of doing it - but a job
// gated on the code alone would start work that the poweroff is about to kill,
// so the line has to say so.
func TestCheckSaysWhenAShutdownIsAlreadyRequested(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := liveSnap(now)
	s.Triggered, s.OnBattery, s.UPSStatus, s.Charge, s.HeldSecs = true, true, "OB DISCHRG", 40, 125

	code, out, errOut := checkCmd(t, now.Add(time.Second), "--file", writeSnap(t, s))
	if code != 0 {
		t.Fatalf("code = %d, want 0: a guard that has acted is protecting the machine\nstdout %q stderr %q", code, out, errOut)
	}
	if !strings.Contains(out, "a shutdown has already been requested") {
		t.Fatalf("the imminent shutdown must be in the line: %q", out)
	}
	if !strings.Contains(out, "protected (on battery, 40%)") {
		t.Fatalf("the verdict must still be stated: %q", out)
	}
}
