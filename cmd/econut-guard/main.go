// Command econut-guard shuts the machine down when the UPS is on battery and
// the charge is low. Pass --dry-run to log instead of shutting down.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"econut/internal/actuator"
	"econut/internal/guard"
	"econut/internal/nut"
	"econut/internal/policy"
	"econut/internal/status"
)

// envFloat returns the float in environment variable key, or def if it is
// unset. A set but unparsable value is an error: it configures a safety
// threshold, so a typo must not silently fall back to the default.
func envFloat(getenv func(string) string, key string, def float64) (float64, error) {
	s := getenv(key)
	if s == "" {
		return def, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) {
		return 0, fmt.Errorf("%s=%q is not a number", key, s)
	}
	return v, nil
}

func envDuration(getenv func(string) string, key string, def time.Duration) (time.Duration, error) {
	s := getenv(key)
	if s == "" {
		return def, nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a duration like 120s: %w", key, s, err)
	}
	return v, nil
}

// snapshotWriter returns a Report func that publishes snapshots to path, or nil
// if path is empty. A write failure is logged once per failure streak: the
// status file is a convenience and must never disturb the guard.
func snapshotWriter(path string, log *slog.Logger) func(status.Snapshot) {
	if path == "" {
		return nil
	}
	os.MkdirAll(filepath.Dir(path), 0o755) // best effort; systemd normally creates it
	failing := false
	return func(s status.Snapshot) {
		if err := status.Write(path, s); err != nil {
			if !failing {
				log.Warn("cannot write status file; `econut status` will not see this guard", "path", path, "err", err)
			}
			failing = true
			return
		}
		failing = false
	}
}

func validate(trigger float64, debounce, poll, stale time.Duration) error {
	switch {
	case math.IsNaN(trigger) || trigger <= 0 || trigger > 100:
		return fmt.Errorf("--trigger-below must be in (0, 100], got %v", trigger)
	case debounce < 0:
		return fmt.Errorf("--debounce must be >= 0, got %s", debounce)
	case poll <= 0:
		return fmt.Errorf("--poll must be > 0, got %s", poll)
	case stale <= 0:
		return fmt.Errorf("--stale-after must be > 0, got %s", stale)
	case 2*poll >= stale:
		// The policy does not credit gaps of stale-after or more, and one
		// slow NUT read can take up to two poll intervals.
		return fmt.Errorf("--poll %s too large for --stale-after %s (need 2*poll < stale-after)", poll, stale)
	}
	return nil
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// ECONUT_TRIGGER_BELOW and ECONUT_DEBOUNCE (from /etc/econut/econut.conf under
	// systemd) set the defaults; flags override them.
	defTrigger, err := envFloat(os.Getenv, "ECONUT_TRIGGER_BELOW", 55)
	if err != nil {
		log.Error("bad environment", "err", err)
		os.Exit(2)
	}
	defDebounce, err := envDuration(os.Getenv, "ECONUT_DEBOUNCE", 120*time.Second)
	if err != nil {
		log.Error("bad environment", "err", err)
		os.Exit(2)
	}

	var (
		target     = flag.String("ups", "ecoflow@127.0.0.1:3493", "NUT UPS as name@host[:port]")
		trigger    = flag.Float64("trigger-below", defTrigger, "trigger when on battery and charge is below this percent (env ECONUT_TRIGGER_BELOW)")
		debounce   = flag.Duration("debounce", defDebounce, "condition must hold this long before acting (env ECONUT_DEBOUNCE)")
		poll       = flag.Duration("poll", 5*time.Second, "poll interval")
		stale      = flag.Duration("stale-after", 300*time.Second, "log an error if NUT data is invalid this long while on battery")
		dryRun     = flag.Bool("dry-run", false, "only log what would happen; never power the machine off (for testing)")
		once       = flag.Bool("once", false, "read the UPS once, print the reading and exit (diagnostic)")
		statusFile = flag.String("status-file", status.DefaultPath, "write a status snapshot here for the econut command (empty disables)")
	)
	flag.Parse()

	if err := validate(*trigger, *debounce, *poll, *stale); err != nil {
		log.Error("bad flags", "err", err)
		os.Exit(2)
	}
	ups, addr, err := nut.ParseTarget(*target)
	if err != nil {
		log.Error("bad flags", "err", err)
		os.Exit(2)
	}
	client := &nut.Client{Addr: addr, UPS: ups, Timeout: *poll}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *once {
		r, err := client.Read(ctx)
		if err != nil {
			log.Error("read failed", "err", err)
			os.Exit(1)
		}
		fmt.Printf("ups.status=%q battery.charge=%v\n", r.Status, r.Charge)
		return
	}

	var act actuator.Actuator = actuator.NewPoweroff(log)
	mode, modeText := status.ModeLive, "LIVE (will power off the machine)"
	if *dryRun {
		act = actuator.LogOnly{Log: log}
		mode, modeText = status.ModeDryRun, "DRY-RUN (no shutdown will occur)"
	}
	log.Info("starting econut-guard", "mode", modeText, "ups", *target,
		"trigger_below", *trigger, "debounce", *debounce, "poll", *poll)

	g := &guard.Guard{
		Cfg:    policy.Config{TriggerBelow: *trigger, Debounce: *debounce, StaleAfter: *stale},
		Src:    client,
		Act:    act,
		Log:    log,
		Poll:   *poll,
		Now:    time.Now,
		Mode:   mode,
		Target: *target,
		Report: snapshotWriter(*statusFile, log),
	}
	g.Run(ctx)
	log.Info("stopped")
}
