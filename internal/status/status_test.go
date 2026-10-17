package status

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sample() Snapshot {
	return Snapshot{
		Updated:        time.Unix(1_000_000, 0).UTC(),
		Mode:           ModeDryRun,
		UPS:            "<ups>",
		PollSeconds:    5,
		ShutdownBelow:  55,
		DebounceSecs:   120,
		Valid:          true,
		UPSStatus:      "OB DISCHRG",
		Charge:         48,
		OnBattery:      true,
		HeldSecs:       35,
		EverValid:      true,
		StaleAfterSecs: 300,
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	want := sample()
	want.BlindSeconds = 302400 // nonzero, so the new keys are covered too
	if err := Write(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestWriteIsAtomicAndWorldReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	if err := Write(path, sample()); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, sample()); err != nil { // overwrite
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want 0644 so unprivileged users can run `econut`", fi.Mode().Perm())
	}
}

func TestReadMissingIsDistinguishable(t *testing.T) {
	_, err := Read(filepath.Join(t.TempDir(), "nope.json"))
	if !os.IsNotExist(err) {
		t.Fatalf("want a not-exist error, got %v", err)
	}
}

func TestReadRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	os.WriteFile(path, []byte("not json"), 0o644)
	if _, err := Read(path); err == nil {
		t.Fatal("want error for garbage")
	}
}

func TestFresh(t *testing.T) {
	s := sample() // PollSeconds 5
	now := s.Updated
	if !s.Fresh(now.Add(14 * time.Second)) {
		t.Fatal("within 3 polls must be fresh")
	}
	if s.Fresh(now.Add(16 * time.Second)) {
		t.Fatal("older than 3 polls must be stale")
	}
}

// TestProtectionIsFailClosed enumerates every combination the guard can publish,
// including modes this binary does not recognise. An unrecognised mode is a
// fault, not a safe default.
func TestProtectionIsFailClosed(t *testing.T) {
	// want[fresh][valid][mode]
	want := map[bool]map[bool]map[string]Protection{
		false: {
			true:  {ModeLive: NotRunning, ModeDryRun: NotRunning, "": NotRunning, "bogus": NotRunning},
			false: {ModeLive: NotRunning, ModeDryRun: NotRunning, "": NotRunning, "bogus": NotRunning},
		},
		true: {
			true:  {ModeLive: Protected, ModeDryRun: DryRun, "": UnknownMode, "bogus": UnknownMode},
			false: {ModeLive: Blind, ModeDryRun: Blind, "": Blind, "bogus": Blind},
		},
	}
	for fresh, byValid := range want {
		for valid, byMode := range byValid {
			for mode, exp := range byMode {
				s := sample()
				s.Mode, s.Valid = mode, valid
				now := s.Updated.Add(2 * time.Second)
				if !fresh {
					now = s.Updated.Add(time.Hour)
				}
				got := s.Protection(now)
				if got != exp {
					t.Errorf("fresh=%v valid=%v mode=%q: Protection = %q, want %q", fresh, valid, mode, got, exp)
				}
				if zero := got.ExitCode() == 0; zero != (exp == Protected) {
					t.Errorf("fresh=%v valid=%v mode=%q: exit 0 must mean Protected, got %q -> %d",
						fresh, valid, mode, got, got.ExitCode())
				}
			}
		}
	}
}

// TestEveryProtectionValueIsCovered is the test that stops this bug class
// recurring when a state is added: Go cannot check a switch for exhaustiveness,
// so every value must be reachable, carry a documented exit code, and render a
// headline that names it.
func TestEveryProtectionValueIsCovered(t *testing.T) {
	all := []Protection{NotRunning, Blind, UnknownMode, DryRun, Protected}

	protected := sample()
	protected.Mode = ModeLive
	dry := sample()
	blind := protected
	blind.Valid, blind.Error = false, "nut: <ups> ups.status: DRIVER-NOT-CONNECTED"
	unknown := protected
	unknown.Mode = "bogus"

	fresh := func(s Snapshot) time.Time { return s.Updated.Add(2 * time.Second) }
	fixtures := map[Protection]struct {
		s    Snapshot
		now  time.Time
		word string
	}{
		NotRunning:  {protected, protected.Updated.Add(time.Hour), "NOT RUNNING"},
		Blind:       {blind, fresh(blind), "NOT PROTECTED"},
		UnknownMode: {unknown, fresh(unknown), "NOT PROTECTED"},
		DryRun:      {dry, fresh(dry), "dry-run"},
		Protected:   {protected, fresh(protected), "ARMED"},
	}
	words := []string{"NOT RUNNING", "NOT PROTECTED", "ARMED", "dry-run"}

	for _, p := range all {
		f, ok := fixtures[p]
		if !ok {
			t.Fatalf("Protection %q has no fixture: every state needs one", p)
		}
		if got := f.s.Protection(f.now); got != p {
			t.Fatalf("fixture for %q classified as %q", p, got)
		}
		switch p.ExitCode() {
		case 0, 1, 3, 4:
		default:
			t.Errorf("%q exits %d, which is not a documented code", p, p.ExitCode())
		}
		got := Render(f.s, f.now)
		for _, w := range words {
			if has := strings.Contains(got, w); has != (w == f.word) {
				t.Errorf("%q: %q present=%v, want %v in:\n%s", p, w, has, w == f.word, got)
			}
		}
	}
}
