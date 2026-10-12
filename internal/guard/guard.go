// Package guard wires a UPS source, the policy and an actuator into a poll loop.
package guard

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"econut/internal/actuator"
	"econut/internal/nut"
	"econut/internal/policy"
	"econut/internal/status"
)

type Source interface {
	Read(ctx context.Context) (nut.Reading, error)
}

type Guard struct {
	Cfg  policy.Config
	Src  Source
	Act  actuator.Actuator
	Log  *slog.Logger
	Poll time.Duration
	Now  func() time.Time

	// Optional, for the econut status command.
	Mode   string                // status.ModeDryRun or status.ModeEnabled
	Target string                // NUT target as given by the user
	Report func(status.Snapshot) // called after every completed poll

	st          policy.State
	staleLogged bool
}

// onBattery reports whether ups.status contains the exact token "OB".
func onBattery(status string) bool {
	for _, f := range strings.Fields(status) {
		if f == "OB" {
			return true
		}
	}
	return false
}

func toSample(r nut.Reading, err error) policy.Sample {
	if err != nil {
		return policy.Sample{}
	}
	return policy.Sample{OnBattery: onBattery(r.Status), Charge: r.Charge, Valid: true}
}

// Tick performs one poll: read, decide, and act.
func (g *Guard) Tick(ctx context.Context) {
	r, err := g.Src.Read(ctx)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		g.Log.Warn("NUT read failed", "err", err)
	}

	now := g.Now()
	st, res := policy.Step(g.Cfg, g.st, toSample(r, err), now)
	g.st = st

	if res.StaleOnBattery {
		if !g.staleLogged {
			g.Log.Error("NUT data stale while on battery", "stale_after", g.Cfg.StaleAfter)
			g.staleLogged = true
		}
	} else {
		g.staleLogged = false
	}

	if res.Decision == policy.Shutdown {
		reason := fmt.Sprintf("on battery, charge %.0f%% < %.0f%% for %s", r.Charge, g.Cfg.TriggerBelow, g.Cfg.Debounce)
		if err := g.Act.Shutdown(ctx, reason); err != nil {
			g.Log.Error("shutdown action failed; will retry on next poll", "err", err)
			g.st.Triggered = false
		}
	}
	g.report(now, r, err)
}

func (g *Guard) report(now time.Time, r nut.Reading, readErr error) {
	if g.Report == nil {
		return
	}
	s := status.Snapshot{
		Updated:      now,
		Mode:         g.Mode,
		UPS:          g.Target,
		PollSeconds:  g.Poll.Seconds(),
		TriggerBelow: g.Cfg.TriggerBelow,
		DebounceSecs: g.Cfg.Debounce.Seconds(),
		HeldSecs:     g.st.Held.Seconds(),
		Triggered:    g.st.Triggered,
	}
	if readErr != nil {
		s.Error = readErr.Error()
	} else {
		s.Valid = true
		s.UPSStatus = r.Status
		s.Charge = r.Charge
		s.OnBattery = onBattery(r.Status)
	}
	g.Report(s)
}

// Run polls immediately and then every Poll until ctx is done.
func (g *Guard) Run(ctx context.Context) {
	t := time.NewTicker(g.Poll)
	defer t.Stop()
	g.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.Tick(ctx)
		}
	}
}
