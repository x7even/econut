// Package status is the snapshot econut-guard publishes for the econut command.
package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	ModeDryRun = "dry-run"
	ModeLive   = "live"

	// DefaultPath is where the systemd unit's RuntimeDirectory puts the file.
	DefaultPath = "/run/econut/status.json"
)

// Snapshot is the guard's state after its latest poll.
type Snapshot struct {
	Updated       time.Time `json:"updated"`
	Mode          string    `json:"mode"` // ModeDryRun or ModeLive
	UPS           string    `json:"ups"`
	PollSeconds   float64   `json:"poll_seconds"`
	ShutdownBelow float64   `json:"shutdown_below"`
	DebounceSecs  float64   `json:"debounce_seconds"`

	Valid     bool    `json:"valid"` // false when the last NUT read failed
	Error     string  `json:"error,omitempty"`
	UPSStatus string  `json:"ups_status,omitempty"`
	Charge    float64 `json:"charge"`
	OnBattery bool    `json:"on_battery"`
	HeldSecs  float64 `json:"held_seconds"` // how long the trigger condition has held
	Triggered bool    `json:"triggered"`

	BlindSeconds   float64 `json:"blind_seconds"`       // how long readings have been unusable; 0 when valid
	EverValid      bool    `json:"ever_valid"`          // false until the guard has had one usable reading
	StaleAfterSecs float64 `json:"stale_after_seconds"` // --stale-after: the escalation threshold
}

// Protection is what econut-guard can do for the machine right now. It is the
// one judgement the status output, the exit codes and the JSON all derive
// from, so they cannot disagree. Every unrecognised value resolves to an
// unprotected one.
type Protection string

const (
	NotRunning  Protection = "not-running"
	Blind       Protection = "blind"
	UnknownMode Protection = "unknown-mode"
	DryRun      Protection = "dry-run"
	Protected   Protection = "protected"
)

// Protection classifies a snapshot. Protected is the only value that means a
// low battery will power the machine off.
func (s Snapshot) Protection(now time.Time) Protection {
	switch {
	case !s.Fresh(now):
		return NotRunning
	case !s.Valid:
		return Blind // a fault outranks a deliberate dry-run
	case s.Mode == ModeDryRun:
		return DryRun
	case s.Mode != ModeLive:
		return UnknownMode // an empty or garbled mode is a fault, not a safe default
	default:
		return Protected
	}
}

// ExitCode is the status econut and econut check exit with for p.
func (p Protection) ExitCode() int {
	switch p {
	case Protected:
		return 0
	case NotRunning:
		return 1
	case DryRun:
		return 4
	default: // Blind, UnknownMode, and any value this binary does not know
		return 3
	}
}

// Fresh reports whether the snapshot is recent enough to mean the guard is
// still running: updated within three poll intervals of now.
func (s Snapshot) Fresh(now time.Time) bool {
	return now.Sub(s.Updated) <= time.Duration(3*s.PollSeconds*float64(time.Second))
}

// Write replaces path atomically with mode 0644, so a reader never sees a
// partial file and unprivileged users can read it.
func Write(path string, s Snapshot) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".status-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Read loads a snapshot. A missing file returns an error satisfying os.IsNotExist.
func Read(path string) (Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}
