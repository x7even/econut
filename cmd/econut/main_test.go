package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"econut/internal/status"
)

func snap(updated time.Time) status.Snapshot {
	return status.Snapshot{
		Updated: updated, Mode: status.ModeDryRun, UPS: "ecoflow@127.0.0.1:3493",
		PollSeconds: 5, TriggerBelow: 55, DebounceSecs: 120,
		Valid: true, UPSStatus: "OL", Charge: 99,
	}
}

func runCmd(t *testing.T, now time.Time, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = run(args, &o, &e, now)
	return code, o.String(), e.String()
}

func TestStatusRunning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	now := time.Unix(1_000_000, 0)
	if err := status.Write(path, snap(now)); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--file", path}, {"status", "--file", path}} {
		code, out, _ := runCmd(t, now.Add(time.Second), args...)
		if code != 0 || !strings.Contains(out, "running") || !strings.Contains(out, "99%") {
			t.Fatalf("args %v: code=%d out=%q", args, code, out)
		}
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
	status.Write(path, snap(now))
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
