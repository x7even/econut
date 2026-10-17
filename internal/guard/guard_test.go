package guard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/x7even/econut/internal/nut"
	"github.com/x7even/econut/internal/policy"
	"github.com/x7even/econut/internal/status"
)

type seqSource struct {
	readings []nut.Reading
	errs     []error
	i        int
}

func (s *seqSource) Read(context.Context) (nut.Reading, error) {
	i := s.i
	if i >= len(s.readings) {
		i = len(s.readings) - 1
	}
	s.i++
	var err error
	if i < len(s.errs) {
		err = s.errs[i]
	}
	return s.readings[i], err
}

type fakeAct struct {
	calls   int
	reasons []string
	failFor int // fail the first N calls
}

func (a *fakeAct) Shutdown(_ context.Context, reason string) error {
	a.calls++
	a.reasons = append(a.reasons, reason)
	if a.calls <= a.failFor {
		return errors.New("poweroff failed")
	}
	return nil
}

func newGuard(src Source, act *fakeAct, buf *bytes.Buffer) *Guard {
	now := time.Unix(1_000_000, 0)
	return &Guard{
		Cfg:  policy.Config{ShutdownBelow: 55, Debounce: 120 * time.Second, StaleAfter: 300 * time.Second},
		Src:  src,
		Act:  act,
		Log:  slog.New(slog.NewTextHandler(buf, nil)),
		Poll: 5 * time.Second,
		Now:  func() time.Time { now = now.Add(5 * time.Second); return now },
	}
}

func tick(g *Guard, n int) {
	for i := 0; i < n; i++ {
		g.Tick(context.Background())
	}
}

func constSrc(status string, charge float64) *seqSource {
	return &seqSource{readings: []nut.Reading{{Status: status, Charge: charge}}}
}

func TestOnBatteryTokenMatching(t *testing.T) {
	cases := map[string]bool{
		"OB":         true,
		"OB DISCHRG": true,
		"OB LB":      true,
		"DISCHRG OB": true,
		"OL":         false,
		"OL CHRG":    false,
		"ROB":        false,
		"OBX":        false,
		"":           false,
	}
	for in, want := range cases {
		if got := onBattery(in); got != want {
			t.Errorf("onBattery(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestOutageTriggersOnceWithReason(t *testing.T) {
	var buf bytes.Buffer
	act := &fakeAct{}
	g := newGuard(constSrc("OB DISCHRG", 50), act, &buf)
	tick(g, 60)
	if act.calls != 1 {
		t.Fatalf("calls = %d, want 1", act.calls)
	}
	if !strings.Contains(act.reasons[0], "50%") || !strings.Contains(act.reasons[0], "55%") {
		t.Fatalf("reason = %q", act.reasons[0])
	}
}

func TestStatusVariantsOnBatteryTrigger(t *testing.T) {
	for _, st := range []string{"OB", "OB DISCHRG", "OB LB"} {
		var buf bytes.Buffer
		act := &fakeAct{}
		tick(newGuard(constSrc(st, 40), act, &buf), 40)
		if act.calls != 1 {
			t.Errorf("status %q: calls = %d, want 1", st, act.calls)
		}
	}
}

func TestOnMainsNeverTriggers(t *testing.T) {
	for _, st := range []string{"OL", "OL CHRG", "ROB"} {
		var buf bytes.Buffer
		act := &fakeAct{}
		tick(newGuard(constSrc(st, 5), act, &buf), 100)
		if act.calls != 0 {
			t.Errorf("status %q: calls = %d, want 0", st, act.calls)
		}
	}
}

func TestReadErrorsNeverTrigger(t *testing.T) {
	var buf bytes.Buffer
	act := &fakeAct{}
	src := &seqSource{
		readings: []nut.Reading{{}},
		errs:     []error{errors.New("nut down")},
	}
	tick(newGuard(src, act, &buf), 200)
	if act.calls != 0 {
		t.Fatalf("calls = %d, want 0", act.calls)
	}
}

func TestFailedShutdownIsRetriedOnNextPoll(t *testing.T) {
	var buf bytes.Buffer
	act := &fakeAct{failFor: 2}
	g := newGuard(constSrc("OB", 50), act, &buf)
	tick(g, 60)
	// Fails on the first two attempts (consecutive polls), succeeds on the
	// third, then stays quiet.
	if act.calls != 3 {
		t.Fatalf("calls = %d, want 3", act.calls)
	}
	if !strings.Contains(buf.String(), "shutdown action failed") {
		t.Fatalf("failure not logged: %s", buf.String())
	}
}

func TestCancelledContextNeverTriggers(t *testing.T) {
	var buf bytes.Buffer
	act := &fakeAct{}
	g := newGuard(constSrc("OB", 10), act, &buf)
	g.Cfg.Debounce = 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g.Tick(ctx)
	if act.calls != 0 {
		t.Fatalf("calls = %d, want 0", act.calls)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	var buf bytes.Buffer
	act := &fakeAct{}
	var reads atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	src := funcSource(func(context.Context) (nut.Reading, error) {
		if reads.Add(1) >= 3 {
			cancel()
		}
		return nut.Reading{Status: "OL", Charge: 100}, nil
	})
	g := newGuard(src, act, &buf)
	g.Poll = 5 * time.Millisecond
	done := make(chan struct{})
	go func() { g.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if reads.Load() < 3 {
		t.Fatalf("reads = %d, want >= 3", reads.Load())
	}
}

type funcSource func(context.Context) (nut.Reading, error)

func (f funcSource) Read(ctx context.Context) (nut.Reading, error) { return f(ctx) }

func TestReportPublishesSnapshotEachTick(t *testing.T) {
	var buf bytes.Buffer
	g := newGuard(constSrc("OB DISCHRG", 48), &fakeAct{}, &buf)
	g.Mode, g.Target = status.ModeDryRun, "<ups>"
	var snaps []status.Snapshot
	g.Report = func(s status.Snapshot) { snaps = append(snaps, s) }

	tick(g, 3)

	if len(snaps) != 3 {
		t.Fatalf("got %d snapshots, want 3", len(snaps))
	}
	last := snaps[2]
	if !last.Valid || !last.OnBattery || last.Charge != 48 || last.UPSStatus != "OB DISCHRG" {
		t.Fatalf("reading not reflected: %+v", last)
	}
	if last.Mode != status.ModeDryRun || last.UPS != "<ups>" ||
		last.ShutdownBelow != 55 || last.DebounceSecs != 120 || last.PollSeconds != 5 {
		t.Fatalf("config not reflected: %+v", last)
	}
	if last.HeldSecs != 10 { // first tick holds 0s, then +5s per tick
		t.Fatalf("HeldSecs = %v, want 10", last.HeldSecs)
	}
}

func TestReportMarksFailedReadInvalid(t *testing.T) {
	var buf bytes.Buffer
	src := &seqSource{readings: []nut.Reading{{}}, errs: []error{errors.New("connection refused")}}
	g := newGuard(src, &fakeAct{}, &buf)
	var got status.Snapshot
	g.Report = func(s status.Snapshot) { got = s }
	tick(g, 1)
	if got.Valid || got.Error == "" {
		t.Fatalf("failed read must be Valid=false with an error, got %+v", got)
	}
}

func TestLogsVariableChanges(t *testing.T) {
	var buf bytes.Buffer
	src := &seqSource{readings: []nut.Reading{
		{Status: "OL CHRG", Charge: 99, Vars: map[string]string{"input.voltage": "230", "battery.runtime": "11000"}},
		{Status: "OL CHRG", Charge: 99, Vars: map[string]string{"input.voltage": "230", "battery.runtime": "10990"}}, // runtime noise only
		{Status: "OB DISCHRG", Charge: 98, Vars: map[string]string{"input.voltage": "0", "battery.runtime": "10900", "ups.alarm": "power failure"}},
	}}
	g := newGuard(src, &fakeAct{}, &buf)

	tick(g, 1)
	if strings.Contains(buf.String(), "changed") {
		t.Fatalf("first reading is the baseline, nothing should be logged as changed:\n%s", buf.String())
	}
	tick(g, 1)
	if strings.Contains(buf.String(), "changed") {
		t.Fatalf("battery.runtime alone must not be logged:\n%s", buf.String())
	}
	tick(g, 1)
	out := buf.String()
	for _, want := range []string{"UPS values changed", "ups.status", "OL CHRG -> OB DISCHRG", "battery.charge", "99 -> 98",
		"input.voltage", "230 -> 0", "ups.alarm", "<unset> -> power failure"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "battery.runtime") {
		t.Errorf("battery.runtime must be excluded:\n%s", out)
	}
}

func TestDriverStateFlapIsNotLogged(t *testing.T) {
	var buf bytes.Buffer
	src := &seqSource{readings: []nut.Reading{
		{Status: "OL", Charge: 99, Vars: map[string]string{"driver.state": "quiet"}},
		{Status: "OL", Charge: 99, Vars: map[string]string{"driver.state": "updateinfo"}},
	}}
	g := newGuard(src, &fakeAct{}, &buf)
	tick(g, 2)
	if strings.Contains(buf.String(), "changed") {
		t.Fatalf("driver.state flaps every poll and must not be logged:\n%s", buf.String())
	}
}

func TestNilReportIsFine(t *testing.T) {
	var buf bytes.Buffer
	g := newGuard(constSrc("OL", 100), &fakeAct{}, &buf)
	tick(g, 2) // must not panic
}

func TestBlindOnMainsEscalates(t *testing.T) {
	var buf bytes.Buffer
	act := &fakeAct{}
	src := &seqSource{
		readings: []nut.Reading{{Status: "OL", Charge: 100}, {}},
		errs:     []error{nil, errors.New("nut: <ups> ups.status: DRIVER-NOT-CONNECTED")},
	}
	g := newGuard(src, act, &buf)
	tick(g, 200)
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("telemetry died on mains and stayed dead: that must escalate, got only:\n%s", buf.String())
	}
	if act.calls != 0 {
		t.Fatalf("calls = %d, want 0: blindness must never power the machine off", act.calls)
	}
}

func TestBlindFromFirstPollEscalates(t *testing.T) {
	var buf bytes.Buffer
	act := &fakeAct{}
	src := &seqSource{readings: []nut.Reading{{}}, errs: []error{errors.New("nut: connect refused")}}
	g := newGuard(src, act, &buf)
	tick(g, 200)
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("a guard that never once read the UPS must escalate, got only:\n%s", buf.String())
	}
	if act.calls != 0 {
		t.Fatalf("calls = %d, want 0", act.calls)
	}
}

// TestFirstEscalationIsIndexPrecise pins the escalation to the first poll where
// the blind time actually exceeds --stale-after. With a 5s poll the first read
// is blind for 0s, so 300s is first exceeded on tick 62 (305s); a shared
// "time since the last log line" window would land it near twice that.
func TestFirstEscalationIsIndexPrecise(t *testing.T) {
	var buf bytes.Buffer
	src := &seqSource{readings: []nut.Reading{{}}, errs: []error{errors.New("nut: connect refused")}}
	g := newGuard(src, &fakeAct{}, &buf)
	first := 0
	for i := 1; i <= 100; i++ {
		before := buf.Len()
		g.Tick(context.Background())
		if first == 0 && strings.Contains(buf.String()[before:], "level=ERROR") {
			first = i
		}
	}
	if first != 62 {
		t.Fatalf("first ERROR on tick %d, want 62 (blind_for 305s > stale_after 300s)", first)
	}
	if !strings.Contains(buf.String(), "blind_for=5m5s") {
		t.Fatalf("escalation must report how long it has been blind:\n%s", buf.String())
	}
}

// TestFailureLoggingIsBounded bounds the journal volume: the field incident
// emitted one warning per poll for days and never escalated.
func TestFailureLoggingIsBounded(t *testing.T) {
	var buf bytes.Buffer
	src := &seqSource{readings: []nut.Reading{{}}, errs: []error{errors.New("nut: connect refused")}}
	g := newGuard(src, &fakeAct{}, &buf)
	tick(g, 200)
	n := strings.Count(buf.String(), "NUT read failed") + strings.Count(buf.String(), "not protected")
	if n > 6 {
		t.Fatalf("%d failure lines over 200 failed polls, want at most 6", n)
	}
	if n < 2 {
		t.Fatalf("%d failure lines: a days-long outage must not be silent either", n)
	}
}

func TestChangedErrorTextLogsAgain(t *testing.T) {
	var buf bytes.Buffer
	src := &seqSource{
		readings: []nut.Reading{{}, {}, {}},
		errs: []error{
			errors.New("nut: connect refused"),
			errors.New("nut: connect refused"),
			errors.New("nut: <ups> ups.status: DRIVER-NOT-CONNECTED"),
		},
	}
	g := newGuard(src, &fakeAct{}, &buf)
	tick(g, 20)
	if n := strings.Count(buf.String(), "NUT read failed"); n != 2 {
		t.Fatalf("warned %d times, want 2: a new error text is new information", n)
	}
}

func TestRecoveryIsLogged(t *testing.T) {
	var buf bytes.Buffer
	src := &seqSource{
		readings: []nut.Reading{{}, {}, {Status: "OL", Charge: 100}},
		errs:     []error{errors.New("nut: connect refused"), errors.New("nut: connect refused"), nil},
	}
	g := newGuard(src, &fakeAct{}, &buf)
	tick(g, 5)
	if n := strings.Count(buf.String(), "UPS reading recovered"); n != 1 {
		t.Fatalf("recovery logged %d times, want 1:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "failed_reads=2") {
		t.Fatalf("recovery must say how many reads failed:\n%s", buf.String())
	}
}

func TestSnapshotCarriesBlindFacts(t *testing.T) {
	var buf bytes.Buffer
	src := &seqSource{
		readings: []nut.Reading{{}, {}, {}, {Status: "OL", Charge: 100}},
		errs: []error{
			errors.New("nut: connect refused"), errors.New("nut: connect refused"),
			errors.New("nut: connect refused"), nil,
		},
	}
	g := newGuard(src, &fakeAct{}, &buf)
	var snaps []status.Snapshot
	g.Report = func(s status.Snapshot) { snaps = append(snaps, s) }
	tick(g, 4)

	for i, s := range snaps[:3] {
		if s.Valid || s.EverValid {
			t.Fatalf("snapshot %d: a guard that has never read the UPS must say so: %+v", i, s)
		}
		if s.StaleAfterSecs != 300 {
			t.Fatalf("snapshot %d: StaleAfterSecs = %v, want 300", i, s.StaleAfterSecs)
		}
		if want := float64(i) * 5; s.BlindSeconds != want {
			t.Fatalf("snapshot %d: BlindSeconds = %v, want %v", i, s.BlindSeconds, want)
		}
	}
	last := snaps[3]
	if !last.Valid || !last.EverValid || last.BlindSeconds != 0 {
		t.Fatalf("a usable reading must clear the blind facts: %+v", last)
	}
}

// TestNowCalledOncePerTick pins the invariant the whole guard suite rests on:
// the simulated clock advances once per poll, so an extra clock read would
// silently shift every timeline in these tests.
func TestNowCalledOncePerTick(t *testing.T) {
	var buf bytes.Buffer
	g := newGuard(constSrc("OL", 100), &fakeAct{}, &buf)
	calls := 0
	inner := g.Now
	g.Now = func() time.Time { calls++; return inner() }
	tick(g, 10)
	if calls != 10 {
		t.Fatalf("Now called %d times over 10 ticks, want 10", calls)
	}
}

// TestEscalationAnchorsAtTheLastUsableReading: the machine stops being
// watched when the last usable reading goes stale, not one poll later. With a
// 5s poll and a good read on tick 1, 300s of blindness is first exceeded on
// tick 62; measuring from the first failed poll instead put it on tick 63, so
// every escalation was one poll late and a gap of exactly one poll either side
// of --stale-after was never reported at all.
func TestEscalationAnchorsAtTheLastUsableReading(t *testing.T) {
	var buf bytes.Buffer
	src := &seqSource{
		readings: []nut.Reading{{Status: "OL", Charge: 100}, {}},
		errs:     []error{nil, errors.New("nut: connect refused")},
	}
	g := newGuard(src, &fakeAct{}, &buf)
	first := 0
	for i := 1; i <= 100; i++ {
		before := buf.Len()
		g.Tick(context.Background())
		if first == 0 && strings.Contains(buf.String()[before:], "level=ERROR") {
			first = i
		}
	}
	if first != 62 {
		t.Fatalf("first ERROR on tick %d, want 62: the last usable reading was tick 1, so 300s is exceeded at tick 62", first)
	}
	if !strings.Contains(buf.String(), "blind_for=5m5s") {
		t.Fatalf("escalation must report the time since the last usable reading:\n%s", buf.String())
	}
}

// TestBridgedGapIsReportedAsAnIncident: one usable reading every 61 polls
// means no single poll is ever blind for longer than --stale-after, so nothing
// escalates, while each gap is longer than the debounce can bridge - an outage
// inside one would not have powered the machine off. That silent band is the
// bug; the gap is now reported when the reading comes back.
func TestBridgedGapIsReportedAsAnIncident(t *testing.T) {
	var buf bytes.Buffer
	act := &fakeAct{}
	i := 0
	src := funcSource(func(context.Context) (nut.Reading, error) {
		i++
		if i%61 == 1 {
			return nut.Reading{Status: "OL", Charge: 100}, nil
		}
		return nut.Reading{}, errors.New("nut: connect refused")
	})
	g := newGuard(src, act, &buf)
	tick(g, 183) // three cycles: usable readings on ticks 1, 62 and 123

	out := buf.String()
	if n := strings.Count(out, "unusable for longer than stale-after"); n != 2 {
		t.Fatalf("%d gap incidents reported over two 305s gaps, want 2:\n%s", n, out)
	}
	if !strings.Contains(out, "blind_for=5m5s") {
		t.Errorf("the incident must say how long the gap was:\n%s", out)
	}
	if act.calls != 0 {
		t.Errorf("calls = %d, want 0: a reporting gap must never power the machine off", act.calls)
	}
}

// TestBoundedLoggingWithErrorTextThatChangesEveryPoll: a hung or remote upsd
// fails with a timeout whose text carries an ephemeral local port, so it
// differs on every poll. Keying the rate limit on the error text alone would
// then warn once per poll for as long as the fault lasts.
func TestBoundedLoggingWithErrorTextThatChangesEveryPoll(t *testing.T) {
	var buf bytes.Buffer
	i := 0
	src := funcSource(func(context.Context) (nut.Reading, error) {
		i++
		return nut.Reading{}, fmt.Errorf("nut: i/o timeout on local port %d", 40000+i)
	})
	g := newGuard(src, &fakeAct{}, &buf)
	tick(g, 200) // 1000s, so about three stale-after windows

	out := buf.String()
	n := strings.Count(out, "NUT read failed") + strings.Count(out, "not protected")
	if n > 20 {
		t.Fatalf("%d lines over 200 failed polls with a per-poll error text, want at most 20:\n%s", n, out)
	}
	if n < 2 {
		t.Fatalf("%d lines: the fault must not be silent either", n)
	}
	if !strings.Contains(out, "level=ERROR") {
		t.Fatalf("a fault lasting this long must escalate:\n%s", out)
	}
}

// TestBoundedLoggingWhileTheDriverFlaps: a driver that answers every other
// poll starts a fresh failure run each time, which reopened a per-run budget.
// The window now survives the recovery, so flapping cannot buy more lines.
func TestBoundedLoggingWhileTheDriverFlaps(t *testing.T) {
	var buf bytes.Buffer
	act := &fakeAct{}
	i := 0
	src := funcSource(func(context.Context) (nut.Reading, error) {
		i++
		if i%2 == 0 {
			return nut.Reading{Status: "OL", Charge: 100}, nil
		}
		return nut.Reading{}, errors.New("nut: connect refused")
	})
	g := newGuard(src, act, &buf)
	tick(g, 400) // 2000s of flapping

	out := buf.String()
	n := strings.Count(out, "NUT read failed") + strings.Count(out, "UPS reading recovered")
	if n > 25 {
		t.Fatalf("%d health lines over 200 failures and 200 recoveries, want at most 25:\n%s", n, out)
	}
	if n < 2 {
		t.Fatalf("%d health lines: flapping must not be silent either", n)
	}
	if act.calls != 0 {
		t.Errorf("calls = %d, want 0: the UPS is on mains", act.calls)
	}
}

// TestFailedShutdownIsPublishedUntilItSucceeds: the field incident reported
// ARMED while every poweroff attempt was failing. The retry is not enough on
// its own - the snapshot has to say the machine is not protected, or nothing
// watching the exit code can tell.
func TestFailedShutdownIsPublishedUntilItSucceeds(t *testing.T) {
	var buf bytes.Buffer
	act := &fakeAct{failFor: 1}
	g := newGuard(constSrc("OB", 50), act, &buf)
	g.Mode, g.Target = status.ModeLive, "<ups>"
	var snaps []status.Snapshot
	g.Report = func(s status.Snapshot) { snaps = append(snaps, s) }
	tick(g, 30)

	// Held reaches the 120s debounce on tick 25, so the failing attempt is
	// published in that tick's snapshot and the successful retry in tick 26's.
	failed, retried := snaps[24], snaps[25]
	if failed.ShutdownFailed == "" {
		t.Fatalf("the failed attempt is missing from the snapshot: %+v", failed)
	}
	if got := failed.Protection(failed.Updated); got != status.ActionFailed {
		t.Fatalf("Protection = %q, want %q", got, status.ActionFailed)
	}
	if got := failed.Protection(failed.Updated).ExitCode(); got == 0 {
		t.Fatalf("a guard whose poweroff failed exits %d", got)
	}
	if retried.ShutdownFailed != "" {
		t.Fatalf("the retry succeeded, so the snapshot must be clean again: %+v", retried)
	}
	if got := retried.Protection(retried.Updated); got != status.Protected {
		t.Fatalf("after a successful shutdown request Protection = %q, want %q", got, status.Protected)
	}
	if act.calls != 2 {
		t.Fatalf("calls = %d, want 2", act.calls)
	}
}

// TestFailedShutdownClearsWhenTheConditionGoesAway: mains came back, nothing
// is being asked of the actuator, and an attempt that failed during the last
// outage is history rather than the state now.
func TestFailedShutdownClearsWhenTheConditionGoesAway(t *testing.T) {
	var buf bytes.Buffer
	act := &fakeAct{failFor: 100}
	i := 0
	src := funcSource(func(context.Context) (nut.Reading, error) {
		i++
		if i > 30 {
			return nut.Reading{Status: "OL CHRG", Charge: 90}, nil
		}
		return nut.Reading{Status: "OB DISCHRG", Charge: 50}, nil
	})
	g := newGuard(src, act, &buf)
	g.Mode = status.ModeLive
	var snaps []status.Snapshot
	g.Report = func(s status.Snapshot) { snaps = append(snaps, s) }
	tick(g, 40)

	if snaps[29].ShutdownFailed == "" {
		t.Fatalf("while the attempt keeps failing the snapshot must say so: %+v", snaps[29])
	}
	last := snaps[len(snaps)-1]
	if last.ShutdownFailed != "" {
		t.Fatalf("on mains with the condition clear, the old failure must not persist: %+v", last)
	}
	if got := last.Protection(last.Updated); got != status.Protected {
		t.Fatalf("Protection = %q, want %q", got, status.Protected)
	}
}

// TestUninterpretableStatusIsReportedAndNeverTriggers: the decision reads the
// "OB" token alone, so a status saying neither OB nor OL is read as mains -
// the guard sits quiet while the battery drains. It cannot be promoted to an
// outage either (acting on a reading nobody can interpret would power the
// machine off on a healthy mains), so it is reported instead.
func TestUninterpretableStatusIsReportedAndNeverTriggers(t *testing.T) {
	var buf bytes.Buffer
	act := &fakeAct{}
	g := newGuard(constSrc("DISCHRG", 10), act, &buf)
	g.Mode = status.ModeLive
	var last status.Snapshot
	g.Report = func(s status.Snapshot) { last = s }
	tick(g, 200)

	out := buf.String()
	if n := strings.Count(out, "says neither OB nor OL"); n != 1 {
		t.Fatalf("logged %d times over 200 polls, want exactly 1 while it persists:\n%s", n, out)
	}
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, `ups_status=DISCHRG`) {
		t.Fatalf("the unusable status must be named at ERROR:\n%s", out)
	}
	if act.calls != 0 {
		t.Fatalf("calls = %d, want 0: econut must not act on a reading it cannot interpret", act.calls)
	}
	if !last.Valid {
		t.Fatalf("the read itself worked, so the snapshot stays Valid: %+v", last)
	}
	if got := last.Protection(last.Updated); got != status.UnknownStatus {
		t.Fatalf("Protection = %q, want %q", got, status.UnknownStatus)
	}
}

func TestInterpretableStatusAgainIsLogged(t *testing.T) {
	var buf bytes.Buffer
	src := &seqSource{readings: []nut.Reading{
		{Status: "TRIM", Charge: 80},
		{Status: "OL TRIM", Charge: 80},
	}}
	g := newGuard(src, &fakeAct{}, &buf)
	tick(g, 4)
	out := buf.String()
	if !strings.Contains(out, "says neither OB nor OL") {
		t.Fatalf("the first reading is uninterpretable and must be reported:\n%s", out)
	}
	if n := strings.Count(out, "interpretable again"); n != 1 {
		t.Fatalf("recovery logged %d times, want 1:\n%s", n, out)
	}
}

// TestRecoveryAfterEscalationIsNotReportedTwice: a run that already escalated
// has had its incident reported, so the recovering poll logs the recovery and
// not a second ERROR for the same gap. (This is the only path on which the
// escalated flag is read, which is why it is no longer dead state.)
func TestRecoveryAfterEscalationIsNotReportedTwice(t *testing.T) {
	var buf bytes.Buffer
	i := 0
	src := funcSource(func(context.Context) (nut.Reading, error) {
		i++
		if i == 1 || i > 80 {
			return nut.Reading{Status: "OL", Charge: 100}, nil
		}
		return nut.Reading{}, errors.New("nut: connect refused")
	})
	g := newGuard(src, &fakeAct{}, &buf)
	tick(g, 85)

	out := buf.String()
	if n := strings.Count(out, "not protected: no usable UPS reading"); n != 1 {
		t.Fatalf("%d escalations over one 395s blind run, want 1:\n%s", n, out)
	}
	if n := strings.Count(out, "unusable for longer than stale-after"); n != 0 {
		t.Fatalf("the gap was already escalated while blind; reporting it again on recovery is a duplicate:\n%s", out)
	}
	if n := strings.Count(out, "UPS reading recovered"); n != 1 {
		t.Fatalf("recovery logged %d times, want 1:\n%s", n, out)
	}
}
