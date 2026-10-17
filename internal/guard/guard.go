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

	"github.com/x7even/econut/internal/actuator"
	"github.com/x7even/econut/internal/nut"
	"github.com/x7even/econut/internal/policy"
	"github.com/x7even/econut/internal/status"
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

	st             policy.State
	blindReads     int       // consecutive failed reads in the current run
	blindWarned    bool      // a WARN has been emitted for this run
	lastErrText    string    // error text of the last failed read in this run
	escalated      bool      // an ERROR has been emitted for this run
	nextEscalation time.Time // when the next ERROR is due while still blind
	windowEnds     time.Time // end of the current read-health logging window
	windowLines    int       // health lines already emitted in that window
	statusWarned   bool      // an uninterpretable ups.status has been warned about
	shutdownErr    string    // why the last shutdown attempt failed; "" when none has
	lastVars       map[string]string
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
	// Exactly one clock read per tick, after the read, so a simulated clock
	// advances once per poll. Step comes before the logging because the level
	// a read failure deserves depends on how long it has been failing.
	now := g.Now()
	st, res := policy.Step(g.Cfg, g.st, toSample(r, err), now)
	g.st = st

	g.logHealth(now, r, err, res)

	if res.Decision == policy.Shutdown {
		reason := fmt.Sprintf("on battery, charge %.0f%% < %.0f%% for %s", r.Charge, g.Cfg.ShutdownBelow, g.Cfg.Debounce)
		if actErr := g.Act.Shutdown(ctx, reason); actErr != nil {
			g.Log.Error("shutdown action failed; will retry on next poll", "err", actErr)
			g.st.Triggered = false
			// Published too: a machine whose poweroff keeps failing is not
			// protected, however healthy its telemetry looks.
			g.shutdownErr = actErr.Error()
		} else {
			g.shutdownErr = ""
		}
	} else if !res.Blind && !g.st.Triggered && g.st.Held == 0 {
		// The trigger condition no longer holds, so nothing is being asked of
		// the actuator: an earlier failure is history, not the state now.
		g.shutdownErr = ""
	}
	g.report(now, r, err, res)
}

// maxHealthLinesPerWindow bounds how many read-health lines - failure warnings
// and recovery notices - one StaleAfter window may contain. A per-error-text
// limit is not enough on its own: a hung or remote upsd fails with a net
// timeout whose text carries the ephemeral local port, so it differs on every
// single poll, and a driver that flaps starts a fresh run every other poll.
// The escalating ERROR below is limited separately and is never dropped here.
const maxHealthLinesPerWindow = 3

// healthLineDue reports whether another read-health line fits in the current
// window, counting it when it does. The window deliberately survives a
// recovery, so flapping cannot reopen the budget every other poll.
func (g *Guard) healthLineDue(now time.Time) bool {
	if g.windowEnds.IsZero() || !now.Before(g.windowEnds) {
		g.windowEnds, g.windowLines = now.Add(g.Cfg.StaleAfter), 0
	}
	if g.windowLines >= maxHealthLinesPerWindow {
		return false
	}
	g.windowLines++
	return true
}

// logHealth records read failures, escalations and recovery at a bounded rate:
// days of failed polls must not flood the journal, and silence must not hide
// them either.
func (g *Guard) logHealth(now time.Time, r nut.Reading, readErr error, res policy.Result) {
	if !res.Blind {
		switch {
		// A gap that the next reading bridged before any single poll was
		// Stale is still an incident: it is longer than the debounce will
		// bridge, so an outage inside it would not have shut the machine
		// down, and nothing else would ever report it.
		case res.Gap > g.Cfg.StaleAfter && !g.escalated:
			g.Log.Error("not protected: UPS readings were unusable for longer than stale-after",
				"blind_for", res.Gap.Round(time.Second), "stale_after", g.Cfg.StaleAfter,
				"failed_reads", g.blindReads)
		case g.blindWarned && g.healthLineDue(now):
			g.Log.Info("UPS reading recovered",
				"blind_for", res.Gap.Round(time.Second), "failed_reads", g.blindReads)
		}
		g.blindReads, g.blindWarned, g.lastErrText = 0, false, ""
		g.escalated, g.nextEscalation = false, time.Time{}
		g.logStatusHealth(now, r)
		g.logChanges(r)
		return
	}
	g.blindReads++
	if txt := readErr.Error(); (!g.blindWarned || txt != g.lastErrText) && g.healthLineDue(now) {
		g.Log.Warn("NUT read failed", "err", readErr,
			"failed_reads", g.blindReads, "blind_for", res.BlindFor.Round(time.Second))
		g.blindWarned, g.lastErrText = true, txt
	}
	// Edge-triggered on Stale going false->true, then once per window: a
	// reading that has been unusable for this long is an incident whatever
	// the last reading said, because nothing is watching the battery.
	if res.Stale && (g.nextEscalation.IsZero() || !now.Before(g.nextEscalation)) {
		g.Log.Error("not protected: no usable UPS reading",
			"blind_for", res.BlindFor.Round(time.Second), "stale_after", g.Cfg.StaleAfter,
			"ever_valid", res.EverValid, "failed_reads", g.blindReads, "err", readErr)
		g.escalated, g.nextEscalation = true, now.Add(g.Cfg.StaleAfter)
	}
}

// logStatusHealth warns while a successful read carries a ups.status that does
// not say which power source the UPS is on. econut decides on the "OB" token
// alone, so such a reading cannot be interpreted at all and the machine is not
// protected, even though the read itself worked.
func (g *Guard) logStatusHealth(now time.Time, r nut.Reading) {
	if status.StatusKnown(r.Status) {
		if g.statusWarned {
			g.Log.Info("ups.status is interpretable again", "ups_status", r.Status)
			g.statusWarned = false
		}
		return
	}
	if !g.statusWarned && g.healthLineDue(now) {
		g.Log.Error("not protected: ups.status says neither OB nor OL, so the power source is unknown",
			"ups_status", r.Status, "battery_charge", r.Charge)
		g.statusWarned = true
	}
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

func (g *Guard) report(now time.Time, r nut.Reading, readErr error, res policy.Result) {
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
		BlindSeconds:  res.BlindFor.Seconds(),
		EverValid:     res.EverValid,
		// Published so a reader can tell a guard that does not record the
		// blind duration at all from one reporting zero, and so JSON
		// consumers can see the escalation threshold they are watching.
		StaleAfterSecs: g.Cfg.StaleAfter.Seconds(),
		ShutdownFailed: g.shutdownErr,
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
