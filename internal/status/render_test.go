package status

import (
	"strings"
	"testing"
	"time"
)

func TestRender(t *testing.T) {
	base := sample() // dry-run, OB DISCHRG, 48%, held 35s of 120s, trigger 55
	now := base.Updated.Add(2 * time.Second)

	mains := base
	mains.UPSStatus, mains.OnBattery, mains.Charge, mains.HeldSecs = "OL CHRG", false, 99, 0

	highBatt := base
	highBatt.Charge, highBatt.HeldSecs = 80, 0

	enabled := base
	enabled.Mode = ModeEnabled

	triggered := base
	triggered.Triggered, triggered.HeldSecs = true, 125

	triggeredEnabled := triggered
	triggeredEnabled.Mode = ModeEnabled

	noRead := base
	noRead.Valid, noRead.Error = false, "nut: connect refused"

	cases := []struct {
		name string
		s    Snapshot
		now  time.Time
		want []string
		deny []string
	}{
		{"mains", mains, now,
			[]string{"running", "dry-run", "will NOT shut down", "on mains", "99%", "below 55%", "120s", "Condition: not met"},
			[]string{"NOT RUNNING"}},
		{"on battery above level", highBatt, now,
			[]string{"on battery", "80%", "Condition: not met", "not below 55%"}, nil},
		{"counting down", base, now,
			[]string{"on battery", "48%", "Condition: MET", "35s of 120s", "85s"}, nil},
		{"armed for real", enabled, now,
			[]string{"shutdown ENABLED", "shuts down in 85s"}, []string{"will NOT"}},
		{"dry-run triggered", triggered, now,
			[]string{"WOULD have shut down"}, nil},
		{"real triggered", triggeredEnabled, now,
			[]string{"shutdown requested"}, nil},
		{"no reading", noRead, now,
			[]string{"no reading", "connect refused"}, nil},
		{"guard gone quiet", base, base.Updated.Add(90 * time.Second),
			[]string{"NOT RUNNING", "1m30s ago"}, []string{"Condition"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Render(c.s, c.now)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, d := range c.deny {
				if strings.Contains(got, d) {
					t.Errorf("unexpected %q in:\n%s", d, got)
				}
			}
		})
	}
}
