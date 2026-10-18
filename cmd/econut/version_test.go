package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/x7even/econut/internal/version"
)

// writeVerSnap puts a live snapshot carrying guardVer at a fresh temp path.
func writeVerSnap(t *testing.T, now time.Time, guardVer string) string {
	t.Helper()
	s := liveSnap(now)
	s.Version = guardVer
	return writeSnap(t, s)
}

// The ordinary case: both builds named, the running one marked as such.
func TestVersionReportsBothBuilds(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	p := writeVerSnap(t, now, "v9.9.9")

	code, out, errOut := runCmd(t, now.Add(time.Second), "version", "--file", p)
	if code != 0 {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, "econut        "+version.String()) {
		t.Errorf("this command's build missing from %q (want %q)", out, version.String())
	}
	if !strings.Contains(out, "econut-guard  v9.9.9 (running)") {
		t.Errorf("guard build missing from %q", out)
	}
}

// The gap this subcommand exists for: go install has refreshed the CLI while
// the daemon still runs an older build.
func TestVersionCallsOutAMismatch(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	p := writeVerSnap(t, now, "v0.3.0")

	code, out, _ := runCmd(t, now.Add(time.Second), "version", "--file", p)
	if code != 0 {
		t.Fatalf("a mismatch is reported, not judged: code=%d", code)
	}
	if !strings.Contains(out, "different build") || !strings.Contains(out, "install") {
		t.Errorf("no mismatch notice in %q", out)
	}
}

// ...and it must stay quiet when they agree, or the notice means nothing.
func TestVersionQuietWhenBuildsAgree(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	p := writeVerSnap(t, now, version.String())

	code, out, _ := runCmd(t, now.Add(time.Second), "version", "--file", p)
	if code != 0 {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if strings.Contains(out, "different build") {
		t.Errorf("matching builds reported as a mismatch: %q", out)
	}
}

// Three ways there is no running guard to name. Each says why, none says
// "unknown", and none of them is an error: the question was still answered.
func TestVersionWhenTheGuardCannotBeNamed(t *testing.T) {
	now := time.Unix(1_000_000, 0)

	t.Run("no status file", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "absent.json")
		code, out, _ := runCmd(t, now, "version", "--file", missing)
		if code != 0 || !strings.Contains(out, "not running") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})

	t.Run("stale snapshot does not name its writer", func(t *testing.T) {
		p := writeVerSnap(t, now, "v0.3.0")
		// Well beyond three poll intervals, so the guard has stopped.
		code, out, _ := runCmd(t, now.Add(time.Hour), "version", "--file", p)
		if code != 0 || !strings.Contains(out, "not running") {
			t.Fatalf("code=%d out=%q", code, out)
		}
		if strings.Contains(out, "v0.3.0") {
			t.Errorf("named the build in a stale snapshot as running: %q", out)
		}
	})

	t.Run("a guard too old to report its version", func(t *testing.T) {
		p := writeVerSnap(t, now, "") // what every pre-v0.3.2 guard writes
		code, out, _ := runCmd(t, now.Add(time.Second), "version", "--file", p)
		if code != 0 {
			t.Fatalf("code=%d out=%q", code, out)
		}
		if !strings.Contains(out, "does not report its version") || !strings.Contains(out, versionAdded) {
			t.Errorf("unhelpful answer for an old guard: %q", out)
		}
		if strings.Contains(out, "unknown") || strings.Contains(out, "not running") {
			t.Errorf("an old guard is running and must not read as absent or unknown: %q", out)
		}
	})
}

// --version is what people actually type, and it must not reach the status
// FlagSet, where it would be an undefined flag and exit 2.
func TestVersionFlagSpellings(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	for _, arg := range []string{"version", "--version", "-version"} {
		code, out, errOut := runCmd(t, now, arg, "--file", filepath.Join(t.TempDir(), "absent.json"))
		if code != 0 {
			t.Errorf("%s: code=%d err=%q", arg, code, errOut)
		}
		if !strings.Contains(out, "econut ") {
			t.Errorf("%s: no version line in %q", arg, out)
		}
	}
}

func TestVersionRejectsExtraArguments(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	code, _, errOut := runCmd(t, now, "version", "please")
	if code != 2 {
		t.Fatalf("code=%d, want 2", code)
	}
	if !strings.Contains(errOut, "unexpected argument") {
		t.Errorf("err=%q", errOut)
	}
}
