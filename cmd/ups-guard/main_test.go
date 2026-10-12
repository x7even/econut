package main

import (
	"testing"
	"time"
)

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
