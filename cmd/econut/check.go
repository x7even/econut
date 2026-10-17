package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/x7even/econut/internal/status"
)

// runCheck is a preflight for unattended work: one line and one exit code
// answering "is this machine protected right now", both derived from the same
// status.Protection the status output uses, so the two cannot disagree.
func runCheck(args []string, out, errOut io.Writer, now time.Time) int {
	fs := flag.NewFlagSet("econut check", flag.ContinueOnError)
	fs.SetOutput(errOut)
	file := fs.String("file", status.DefaultPath, "status file written by econut-guard")
	maxAge := fs.Duration("max-age", 0, "also require the snapshot to be newer than this (0 uses the guard's own 3x poll rule, e.g. 30s)")
	allowDryRun := fs.Bool("allow-dry-run", false, "exit 0 for a dry-run guard that can see the UPS")
	quiet := fs.Bool("quiet", false, "print nothing; exit code only")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(errOut, "econut check: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	// The one line goes to stdout when the answer is yes and to stderr when it
	// is no, so a cron job's mail contains only the problem.
	say := func(code int, format string, a ...any) int {
		if !*quiet {
			w := errOut
			if code == 0 {
				w = out
			}
			fmt.Fprintf(w, "econut: "+format+"\n", a...)
		}
		return code
	}

	s, err := status.Read(*file)
	switch {
	case os.IsNotExist(err):
		return say(1, "NOT RUNNING - no status file at %s", *file)
	case err != nil:
		return say(1, "cannot read %s: %v", *file, err)
	}

	p := s.Protection(now)
	// --max-age is the only thing check adds to the verdict: the snapshot's own
	// freshness window is fixed at three poll intervals, which a job that runs
	// for hours may want to tighten.
	if p != status.NotRunning && *maxAge > 0 && now.Sub(s.Updated) > *maxAge {
		p = status.NotRunning
	}
	code := p.ExitCode()
	if p == status.DryRun && *allowDryRun {
		code = 0
	}

	switch p {
	case status.Protected:
		return say(code, "protected (%s, %.0f%%)", powerWord(s), s.Charge)
	case status.DryRun:
		if code == 0 {
			return say(code, "dry-run (allowed, and it can see the UPS)")
		}
		return say(code, "NOT PROTECTED - dry-run")
	case status.Blind:
		return say(code, "NOT PROTECTED - %s", s.BlindPhrase())
	case status.NotRunning:
		return say(code, "NOT RUNNING - last update %s ago", now.Sub(s.Updated).Round(time.Second))
	default:
		return say(code, "NOT PROTECTED - unknown mode %q", s.Mode)
	}
}

func powerWord(s status.Snapshot) string {
	if s.OnBattery {
		return "on battery"
	}
	return "on mains"
}
