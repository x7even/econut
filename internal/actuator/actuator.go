// Package actuator performs the shutdown (Poweroff), or only logs it (LogOnly, for --dry-run).
package actuator

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
)

type Actuator interface {
	Shutdown(ctx context.Context, reason string) error
}

// LogOnly is the dry-run actuator. It never touches the system.
type LogOnly struct{ Log *slog.Logger }

func (a LogOnly) Shutdown(_ context.Context, reason string) error {
	a.Log.Warn("WOULD shut down (--dry-run)", "reason", reason)
	return nil
}

// Runner executes a command. Injected so tests never run real commands.
type Runner func(ctx context.Context, name string, args ...string) error

type Poweroff struct {
	Log *slog.Logger
	Run Runner
}

// NewPoweroff returns a Poweroff wired to the real command runner. This is the
// only constructor that can actually power the machine off.
func NewPoweroff(log *slog.Logger) Poweroff {
	return Poweroff{Log: log, Run: func(ctx context.Context, name string, args ...string) error {
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w: %s", name, err, out)
		}
		return nil
	}}
}

func (a Poweroff) Shutdown(ctx context.Context, reason string) error {
	a.Log.Error("SHUTTING DOWN", "reason", reason)
	return a.Run(ctx, "systemctl", "poweroff")
}
