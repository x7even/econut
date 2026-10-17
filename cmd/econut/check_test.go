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
		{"stricter max-age", []string{"--max-age", "1s", "--file", writeSnap(t, liveSnap(now))}, now.Add(10 * time.Second), 1, "NOT RUNNING - last update"},
		{"max-age satisfied", []string{"--max-age", "30s", "--file", writeSnap(t, liveSnap(now))}, later, 0, "protected"},
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

	cases := map[string]struct {
		s   status.Snapshot
		now time.Time
	}{
		"protected":   {liveSnap(now), now.Add(time.Second)},
		"triggered":   {triggered, now.Add(time.Second)},
		"blind":       {blindSnap(now), now.Add(time.Second)},
		"never read":  {neverRead, now.Add(time.Second)},
		"unknown":     {bogus, now.Add(time.Second)},
		"dry-run":     {snap(now), now.Add(time.Second)},
		"not running": {liveSnap(now), now.Add(time.Hour)},
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
