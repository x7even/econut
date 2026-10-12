// Command econut reports what econut-guard is doing: is it running, what the
// battery level is, and how close it is to shutting the machine down.
//
//	econut [status] [--json] [--file PATH]
//	sudo econut set [--shutdown-below PERCENT] [--debounce DURATION]
//
// Exit status: 0 ok / guard running, 1 guard not running or a failure, 2 usage error.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"econut/internal/status"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now()))
}

func run(args []string, out, errOut io.Writer, now time.Time) int {
	if len(args) > 0 && args[0] == "set" {
		return runSet(args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "status" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("econut status", flag.ContinueOnError)
	fs.SetOutput(errOut)
	file := fs.String("file", status.DefaultPath, "status file written by econut-guard")
	asJSON := fs.Bool("json", false, "print the raw snapshot as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(errOut, "econut: unknown command %q (usage: econut [status] [--json] [--file PATH])\n", fs.Arg(0))
		return 2
	}

	s, err := status.Read(*file)
	switch {
	case os.IsNotExist(err):
		fmt.Fprintf(out, "econut-guard: NOT RUNNING (no status file at %s)\n", *file)
		return 1
	case err != nil:
		fmt.Fprintf(errOut, "econut: cannot read %s: %v\n", *file, err)
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.Encode(s)
	} else {
		fmt.Fprint(out, status.Render(s, now))
	}
	if !s.Fresh(now) {
		return 1
	}
	return 0
}
