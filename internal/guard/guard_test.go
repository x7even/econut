package guard

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"econut/internal/nut"
	"econut/internal/policy"
	"econut/internal/status"
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
		Cfg:  policy.Config{TriggerBelow: 55, Debounce: 120 * time.Second, StaleAfter: 300 * time.Second},
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

func TestStaleWarningLoggedOnce(t *testing.T) {
	var buf bytes.Buffer
	act := &fakeAct{}
	src := &seqSource{readings: []nut.Reading{{Status: "OB", Charge: 80}, {}}}
	// first read valid, rest return an error
	src.errs = []error{nil, errors.New("down")}
	g := newGuard(src, act, &buf)
	tick(g, 200)
	if n := strings.Count(buf.String(), "NUT data stale while on battery"); n != 1 {
		t.Fatalf("stale logged %d times, want 1", n)
	}
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
	g.Mode, g.Target = status.ModeDryRun, "ecoflow@127.0.0.1:3493"
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
	if last.Mode != status.ModeDryRun || last.UPS != "ecoflow@127.0.0.1:3493" ||
		last.TriggerBelow != 55 || last.DebounceSecs != 120 || last.PollSeconds != 5 {
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
