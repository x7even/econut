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
