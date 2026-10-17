// Command econut reports what econut-guard is doing: is it running, what the
// battery level is, and how close it is to shutting the machine down.
//
//	econut [status] [--json] [--file PATH]
//	econut check [--max-age DURATION] [--allow-dry-run] [--quiet] [--file PATH]
//	sudo econut set [--shutdown-below PERCENT] [--debounce DURATION]
//	sudo econut install [--shutdown-below PERCENT] [--enable-service] [--dry-run] [--uninstall]
//
// Exit status: 0 protected, 1 guard not running or a failure, 2 usage error,
// 3 running but blind or in an unknown mode, 4 dry-run. Only 0 means a low
// battery will actually power the machine off, so an unattended job can gate
// on "econut status || exit".
//
// Two long-standing quirks of --json are left as they are, since consumers
// already handle both: a missing status file prints the plain-text NOT RUNNING
// line to stdout, and a stale snapshot pairs valid JSON with exit 1.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/x7even/econut/internal/status"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now()))
}

// useColor reports whether out is a terminal and the user has not set NO_COLOR.
func useColor(out io.Writer) bool {
	f, ok := out.(*os.File)
	if !ok || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func run(args []string, out, errOut io.Writer, now time.Time) int {
	if len(args) > 0 && args[0] == "install" {
		return runInstall(args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "set" {
		return runSet(args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "check" {
		return runCheck(args[1:], out, errOut, now)
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
		fmt.Fprintf(errOut, "econut: unknown command %q (usage: econut [status|check|set|install] ...)\n", fs.Arg(0))
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

	p := s.Protection(now)
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		// Embedded, so every existing key keeps its name and position and
		// unmarshalling into a status.Snapshot still works.
		enc.Encode(struct {
			status.Snapshot
			Protection string `json:"protection"`
			ExitCode   int    `json:"exit_code"`
		}{s, string(p), p.ExitCode()})
	} else {
		fmt.Fprint(out, status.RenderColor(s, now, useColor(out)))
	}
	return p.ExitCode()
}
