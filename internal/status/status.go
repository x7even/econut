// Package status is the snapshot econut-guard publishes for the econut command.
package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	// ShutdownFailed is why the last poweroff attempt failed, empty when none
	// has. A machine whose poweroff does not work is not protected however
	// healthy its telemetry looks, so this is part of the verdict below.
	ShutdownFailed string `json:"shutdown_failed,omitempty"`

	// Version is the build of econut-guard that wrote this snapshot, so a
	// reader can tell which daemon is actually running rather than which
	// binary is on disk. Last, and omitempty, so adding it left every existing
	// key's name and position alone; a guard built before it existed omits it,
	// and absent means "did not say", never "unknown build".
	Version string `json:"version,omitempty"`
}

// StatusKnown reports whether a ups.status string says which power source the
// UPS is on. The decision reads exactly one token, "OB", and treats its
// absence as mains, so a status carrying neither "OB" nor "OL" cannot be
// interpreted at all: a best-fit subdriver that signals discharge some other
// way, or reports nothing, must never be read as healthy mains power.
func StatusKnown(upsStatus string) bool {
	for _, f := range strings.Fields(upsStatus) {
		if f == "OB" || f == "OL" {
			return true
		}
	}
	return false
}

// Protection is what econut-guard can do for the machine right now. It is the
// one judgement the status output, the exit codes and the JSON all derive
// from, so they cannot disagree. Every unrecognised value resolves to an
// unprotected one.
type Protection string

const (
	NotRunning    Protection = "not-running"
	Blind         Protection = "blind"
	ActionFailed  Protection = "action-failed"
	UnknownStatus Protection = "unknown-status"
	UnknownMode   Protection = "unknown-mode"
	DryRun        Protection = "dry-run"
	Protected     Protection = "protected"
)

// Protection classifies a snapshot. Protected is the only value that means a
// low battery will power the machine off. Every fault outranks a deliberate
// dry-run, so `econut check --allow-dry-run` cannot launder one into a yes.
func (s Snapshot) Protection(now time.Time) Protection {
	switch {
	case !s.Fresh(now):
		return NotRunning
	case !s.Valid:
		return Blind
	case s.ShutdownFailed != "":
		return ActionFailed // configured and able to see, but unable to act
	case !StatusKnown(s.UPSStatus):
		return UnknownStatus // the reading arrived but does not say on battery or not
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
	default: // every fault, and any value this binary does not know
		return 3
	}
}

// Fresh reports whether the snapshot is recent enough to mean the guard is
// still running: updated within three poll intervals of now, and not dated in
// the future. A snapshot ahead of the clock means the clock stepped backwards
// after the guard wrote it, or that the file was tampered with; reading one as
// fresh would report a dead guard as running until wall time caught up.
func (s Snapshot) Fresh(now time.Time) bool {
	age := now.Sub(s.Updated)
	return age >= 0 && age <= time.Duration(3*s.PollSeconds*float64(time.Second))
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
