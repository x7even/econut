// Package status is the snapshot econut-guard publishes for the econut command.
package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	ModeDryRun  = "dry-run"
	ModeEnabled = "enabled"

	// DefaultPath is where the systemd unit's RuntimeDirectory puts the file.
	DefaultPath = "/run/econut/status.json"
)

// Snapshot is the guard's state after its latest poll.
type Snapshot struct {
	Updated      time.Time `json:"updated"`
	Mode         string    `json:"mode"` // ModeDryRun or ModeEnabled
	UPS          string    `json:"ups"`
	PollSeconds  float64   `json:"poll_seconds"`
	TriggerBelow float64   `json:"trigger_below"`
	DebounceSecs float64   `json:"debounce_seconds"`

	Valid     bool    `json:"valid"` // false when the last NUT read failed
	Error     string  `json:"error,omitempty"`
	UPSStatus string  `json:"ups_status,omitempty"`
	Charge    float64 `json:"charge"`
	OnBattery bool    `json:"on_battery"`
	HeldSecs  float64 `json:"held_seconds"` // how long the trigger condition has held
	Triggered bool    `json:"triggered"`
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
