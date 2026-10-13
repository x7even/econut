package main

import (
	"math"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestEnvFloat(t *testing.T) {
	if v, err := envFloat(env(nil), "ECONUT_SHUTDOWN_BELOW", 55); err != nil || v != 55 {
		t.Fatalf("unset: got %v, %v; want default 55", v, err)
	}
	if v, err := envFloat(env(map[string]string{"ECONUT_SHUTDOWN_BELOW": "30"}), "ECONUT_SHUTDOWN_BELOW", 55); err != nil || v != 30 {
		t.Fatalf("set: got %v, %v; want 30", v, err)
	}
	// A typo in the safety threshold must fail loudly, never fall back silently.
	for _, bad := range []string{"abc", "5 5", "NaN"} {
		if _, err := envFloat(env(map[string]string{"ECONUT_SHUTDOWN_BELOW": bad}), "ECONUT_SHUTDOWN_BELOW", 55); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestEnvDuration(t *testing.T) {
	if v, err := envDuration(env(nil), "ECONUT_DEBOUNCE", 120*time.Second); err != nil || v != 120*time.Second {
		t.Fatalf("unset: got %v, %v", v, err)
	}
	if v, err := envDuration(env(map[string]string{"ECONUT_DEBOUNCE": "90s"}), "ECONUT_DEBOUNCE", 120*time.Second); err != nil || v != 90*time.Second {
		t.Fatalf("set: got %v, %v", v, err)
	}
	if _, err := envDuration(env(map[string]string{"ECONUT_DEBOUNCE": "soon"}), "ECONUT_DEBOUNCE", time.Second); err == nil {
		t.Fatal("garbage: want error")
	}
}

func TestValidate(t *testing.T) {
	s := time.Second
	if err := validate(55, 120*s, 5*s, 300*s); err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
	bad := []struct {
		name                  string
		trigger               float64
		debounce, poll, stale time.Duration
	}{
		{"trigger zero", 0, 120 * s, 5 * s, 300 * s},
		{"trigger over 100", 101, 120 * s, 5 * s, 300 * s},
		{"trigger NaN never fires", math.NaN(), 120 * s, 5 * s, 300 * s},
		{"negative debounce", 55, -s, 5 * s, 300 * s},
		{"zero poll", 55, 120 * s, 0, 300 * s},
		{"zero stale", 55, 120 * s, 5 * s, 0},
		// Gaps of stale-after or more are not credited to the debounce, so a
		// poll this slow could never fire.
		{"poll starves debounce", 55, 120 * s, 150 * s, 300 * s},
	}
	for _, c := range bad {
		if err := validate(c.trigger, c.debounce, c.poll, c.stale); err == nil {
			t.Errorf("%s: want error", c.name)
		}
	}
}
