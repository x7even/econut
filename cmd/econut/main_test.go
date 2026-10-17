package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/x7even/econut/internal/status"
)

func snap(updated time.Time) status.Snapshot {
	return status.Snapshot{
		Updated: updated, Mode: status.ModeDryRun, UPS: "<ups>",
		PollSeconds: 5, ShutdownBelow: 55, DebounceSecs: 120,
		Valid: true, UPSStatus: "OL", Charge: 99,
	}
}

// liveSnap is a guard that is actually protecting the machine: live mode and a
// usable reading. snap() stays dry-run, since other tests in this package rely
// on that.
func liveSnap(updated time.Time) status.Snapshot {
	s := snap(updated)
	s.Mode, s.EverValid, s.StaleAfterSecs = status.ModeLive, true, 300
	return s
}

func runCmd(t *testing.T, now time.Time, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = run(args, &o, &e, now)
	return code, o.String(), e.String()
}

func TestStatusRunning(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	live := filepath.Join(t.TempDir(), "s.json")
	if err := status.Write(live, liveSnap(now)); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--file", live}, {"status", "--file", live}} {
		code, out, _ := runCmd(t, now.Add(time.Second), args...)
		if code != 0 || !strings.Contains(out, "running") || !strings.Contains(out, "99%") {
			t.Fatalf("args %v: code=%d out=%q", args, code, out)
		}
	}
	// A dry-run guard is running and reading the UPS, but it is not protecting
	// the machine, so it reports itself as before and exits non-zero.
	dry := filepath.Join(t.TempDir(), "s.json")
	if err := status.Write(dry, snap(now)); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCmd(t, now.Add(time.Second), "status", "--file", dry)
	if code != 4 || !strings.Contains(out, "dry-run") {
		t.Fatalf("dry-run: code=%d out=%q", code, out)
	}
}

func TestStatusNotRunningExitsNonZero(t *testing.T) {
	// File absent.
	code, out, _ := runCmd(t, time.Now(), "status", "--file", filepath.Join(t.TempDir(), "none.json"))
	if code != 1 || !strings.Contains(out, "NOT RUNNING") {
		t.Fatalf("absent: code=%d out=%q", code, out)
	}
	// File present but stale.
	path := filepath.Join(t.TempDir(), "s.json")
	now := time.Unix(1_000_000, 0)
	status.Write(path, snap(now))
	code, out, _ = runCmd(t, now.Add(time.Hour), "status", "--file", path)
	if code != 1 || !strings.Contains(out, "NOT RUNNING") {
		t.Fatalf("stale: code=%d out=%q", code, out)
	}
}

func TestStatusJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	now := time.Unix(1_000_000, 0)
	status.Write(path, liveSnap(now))
	code, out, _ := runCmd(t, now, "status", "--json", "--file", path)
	var got status.Snapshot
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got.Charge != 99 {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"bogus"}, {"status", "--nope"}} {
		if code, _, _ := runCmd(t, time.Now(), args...); code != 2 {
			t.Errorf("args %v: code=%d, want 2", args, code)
		}
	}
}

func TestStatusBlindExitsNonZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	now := time.Unix(1_000_000, 0)
	s := blindSnap(now)
	if err := status.Write(path, s); err != nil {
		t.Fatal(err)
	}
	code, _, _ := runCmd(t, now.Add(time.Second), "status", "--file", path)
	if code == 0 {
		t.Fatalf("code = 0: a running but blind guard is not protecting the machine")
	}
}

// blindSnap is the field incident: live, running, and unable to see the UPS.
func blindSnap(updated time.Time) status.Snapshot {
	s := liveSnap(updated)
	s.Valid, s.Error = false, "nut: <ups> ups.status: DRIVER-NOT-CONNECTED"
	s.BlindSeconds = 302400 // 84h
	return s
}

func writeSnap(t *testing.T, s status.Snapshot) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.json")
	if err := status.Write(path, s); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStatusExitCodes(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	later := now.Add(time.Second)

	triggered := liveSnap(now)
	triggered.Triggered, triggered.OnBattery, triggered.Charge, triggered.HeldSecs = true, true, 40, 125

	bogus := liveSnap(now)
	bogus.Mode = "bogus"

	garbage := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(garbage, []byte("not json"), 0o644)

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"protected", []string{"status", "--file", writeSnap(t, liveSnap(now))}, 0},
		{"protected and acting", []string{"status", "--file", writeSnap(t, triggered)}, 0},
		{"blind", []string{"status", "--file", writeSnap(t, blindSnap(now))}, 3},
		{"unknown mode", []string{"status", "--file", writeSnap(t, bogus)}, 3},
		{"dry-run", []string{"status", "--file", writeSnap(t, snap(now))}, 4},
		{"missing file", []string{"status", "--file", filepath.Join(t.TempDir(), "none.json")}, 1},
		{"unparsable file", []string{"status", "--file", garbage}, 1},
		{"unknown subcommand", []string{"bogus"}, 2},
		{"bad flag", []string{"status", "--nope"}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code, out, errOut := runCmd(t, later, c.args...); code != c.want {
				t.Fatalf("code=%d want %d\nstdout: %s\nstderr: %s", code, c.want, out, errOut)
			}
		})
	}
	// Freshness is judged against now, so the stale case needs its own clock.
	if code, _, _ := runCmd(t, now.Add(time.Hour), "status", "--file", writeSnap(t, liveSnap(now))); code != 1 {
		t.Fatalf("stale snapshot: code=%d want 1", code)
	}
}

func TestStatusJSONCarriesProtection(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	path := writeSnap(t, blindSnap(now))
	code, out, _ := runCmd(t, now.Add(time.Second), "status", "--json", "--file", path)
	if code != 3 {
		t.Fatalf("code=%d want 3", code)
	}
	var s status.Snapshot
	if err := json.Unmarshal([]byte(out), &s); err != nil || s.Valid {
		t.Fatalf("output must still unmarshal into a Snapshot: err=%v out=%s", err, out)
	}
	var extra struct {
		Protection string `json:"protection"`
		ExitCode   int    `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(out), &extra); err != nil {
		t.Fatal(err)
	}
	if extra.Protection != string(status.Blind) || extra.ExitCode != 3 {
		t.Fatalf("got %+v, want protection=blind exit_code=3", extra)
	}
}
