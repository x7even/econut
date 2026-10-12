// Command ups-guard shuts the machine down when the UPS is on battery and
// the charge is low. It only logs unless --enable-shutdown is given.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ups-guard/internal/actuator"
	"ups-guard/internal/guard"
	"ups-guard/internal/nut"
	"ups-guard/internal/policy"
)

func validate(trigger float64, debounce, poll, stale time.Duration) error {
	switch {
	case trigger <= 0 || trigger > 100:
		return fmt.Errorf("--trigger-below must be in (0, 100], got %v", trigger)
	case debounce < 0:
		return fmt.Errorf("--debounce must be >= 0, got %s", debounce)
	case poll <= 0:
		return fmt.Errorf("--poll must be > 0, got %s", poll)
	case stale <= 0:
		return fmt.Errorf("--stale-after must be > 0, got %s", stale)
	}
	return nil
}

func main() {
	var (
		target   = flag.String("ups", "ecoflow@127.0.0.1:3493", "NUT UPS as name@host[:port]")
		trigger  = flag.Float64("trigger-below", 55, "trigger when on battery and charge is below this percent")
		debounce = flag.Duration("debounce", 120*time.Second, "condition must hold this long before acting")
		poll     = flag.Duration("poll", 5*time.Second, "poll interval")
		stale    = flag.Duration("stale-after", 300*time.Second, "log an error if NUT data is invalid this long while on battery")
		enable   = flag.Bool("enable-shutdown", false, "actually power the machine off (default: dry-run, log only)")
		once     = flag.Bool("once", false, "read the UPS once, print the reading and exit (diagnostic)")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
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

	var act actuator.Actuator = actuator.LogOnly{Log: log}
	mode := "DRY-RUN (no shutdown will occur)"
	if *enable {
		act = actuator.NewPoweroff(log)
		mode = "ENABLED (will power off the machine)"
	}
	log.Info("starting ups-guard", "mode", mode, "ups", *target,
		"trigger_below", *trigger, "debounce", *debounce, "poll", *poll)

	g := &guard.Guard{
		Cfg:  policy.Config{TriggerBelow: *trigger, Debounce: *debounce, StaleAfter: *stale},
		Src:  client,
		Act:  act,
		Log:  log,
		Poll: *poll,
		Now:  time.Now,
	}
	g.Run(ctx)
	log.Info("stopped")
}
