package status

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sample() Snapshot {
	return Snapshot{
		Updated:      time.Unix(1_000_000, 0).UTC(),
		Mode:         ModeDryRun,
		UPS:          "ecoflow@127.0.0.1:3493",
		PollSeconds:  5,
		TriggerBelow: 55,
		DebounceSecs: 120,
		Valid:        true,
		UPSStatus:    "OB DISCHRG",
		Charge:       48,
		OnBattery:    true,
		HeldSecs:     35,
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	want := sample()
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
