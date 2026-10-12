// Package guard wires a UPS source, the policy and an actuator into a poll loop.
package guard

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
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
	Mode   string                // status.ModeDryRun or status.ModeLive
	Target string                // NUT target as given by the user
	Report func(status.Snapshot) // called after every completed poll

	st          policy.State
	staleLogged bool
	lastVars    map[string]string
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
	} else {
		g.logChanges(r)
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
		reason := fmt.Sprintf("on battery, charge %.0f%% < %.0f%% for %s", r.Charge, g.Cfg.ShutdownBelow, g.Cfg.Debounce)
		if err := g.Act.Shutdown(ctx, reason); err != nil {
			g.Log.Error("shutdown action failed; will retry on next poll", "err", err)
			g.st.Triggered = false
		}
	}
	g.report(now, r, err)
}

// noisyVars change on every poll and would drown out the events worth seeing.
var noisyVars = map[string]bool{"battery.runtime": true, "driver.state": true}

// logChanges logs every UPS value that differs from the previous reading, so the
// journal records what the unit reports when mains drops or returns. The first
// reading is only the baseline.
func (g *Guard) logChanges(r nut.Reading) {
	cur := make(map[string]string, len(r.Vars)+2)
	for k, v := range r.Vars {
		cur[k] = v
	}
	cur["ups.status"] = r.Status
	cur["battery.charge"] = strconv.FormatFloat(r.Charge, 'f', -1, 64)

	prev := g.lastVars
	g.lastVars = cur
	if prev == nil {
		return
	}

	keys := make(map[string]bool)
	for k := range cur {
		keys[k] = true
	}
	for k := range prev {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		if !noisyVars[k] {
			names = append(names, k)
		}
	}
	sort.Strings(names)

	var attrs []any
	for _, k := range names {
		old, hadOld := prev[k]
		now, hasNow := cur[k]
		if hadOld == hasNow && old == now {
			continue
		}
		if !hadOld {
			old = "<unset>"
		}
		if !hasNow {
			now = "<unset>"
		}
		attrs = append(attrs, slog.String(k, old+" -> "+now))
	}
	if len(attrs) > 0 {
		g.Log.Info("UPS values changed", attrs...)
	}
}

func (g *Guard) report(now time.Time, r nut.Reading, readErr error) {
	if g.Report == nil {
		return
	}
	s := status.Snapshot{
		Updated:       now,
		Mode:          g.Mode,
		UPS:           g.Target,
		PollSeconds:   g.Poll.Seconds(),
		ShutdownBelow: g.Cfg.ShutdownBelow,
		DebounceSecs:  g.Cfg.Debounce.Seconds(),
		HeldSecs:      g.st.Held.Seconds(),
		Triggered:     g.st.Triggered,
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
