package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/x7even/econut/internal/status"
	"github.com/x7even/econut/internal/version"
)

// versionAdded is the first release in which econut-guard publishes its build
// in the snapshot. A running guard older than this reports no version at all,
// which has to read as "did not say" rather than as a fault.
const versionAdded = "v0.3.2"

// runVersion prints the build of this command and, separately, the build of the
// econut-guard that is actually running. They are different questions: "go
// install" refreshes this binary while the daemon protecting the machine keeps
// running whatever it was started with, and that gap is the one worth seeing.
//
// It exits 0 whenever it can answer, including when the two differ - it reports
// a mismatch, it does not judge it. Only a usage error exits 2. "econut check"
// remains the command whose exit code means something.
func runVersion(args []string, out, errOut io.Writer, now time.Time) int {
	fs := flag.NewFlagSet("econut version", flag.ContinueOnError)
	fs.SetOutput(errOut)
	file := fs.String("file", status.DefaultPath, "status file written by econut-guard")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(errOut, "econut version: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	mine := version.String()
	fmt.Fprintf(out, "%-13s %s\n", "econut", mine)

	theirs, note := guardBuild(*file, now)
	if theirs != "" {
		fmt.Fprintf(out, "%-13s %s (running)\n", "econut-guard", theirs)
	} else {
		fmt.Fprintf(out, "%-13s %s\n", "econut-guard", note)
	}

	if theirs != "" && theirs != mine {
		// Plain string comparison: these are opaque build identities, and
		// ordering two pseudo-versions is not something this needs to get
		// right to say "these are not the same build".
		fmt.Fprintf(out, "\nThe running daemon is a different build from this command.\n"+
			"Put this one into service with:  sudo %s install\n", os.Args[0])
	}
	return 0
}

// guardBuild returns the running guard's version, or an empty string and a
// phrase explaining why there is none. The phrase never says "unknown": every
// case here has a specific cause worth naming.
func guardBuild(file string, now time.Time) (string, string) {
	s, err := status.Read(file)
	switch {
	case os.IsNotExist(err):
		return "", fmt.Sprintf("not running (no status file at %s)", file)
	case err != nil:
		return "", fmt.Sprintf("unreadable (%v)", err)
	case !s.Fresh(now):
		// A stale snapshot is a guard that has stopped. Reporting the version
		// in it would name a build that is not running.
		return "", "not running (its last snapshot is stale)"
	case s.Version == "":
		return "", fmt.Sprintf("running, but does not report its version (built before %s)", versionAdded)
	}
	return s.Version, ""
}
