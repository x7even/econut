package main

import (
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultConf = "/etc/econut/econut.conf"

// restartGuard restarts the service so it picks up the new settings. A variable
// so tests never touch systemd.
var restartGuard = func() error {
	if out, err := exec.Command("systemctl", "restart", "econut-guard.service").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl restart econut-guard: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// setConf returns content with each key=value in kv replacing the existing
// assignment of that key, or appended if there is none. Other lines are kept.
func setConf(content string, kv [][2]string) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if content == "" {
		lines = nil
	}
	// ECONUT_TRIGGER_BELOW is the earlier name of ECONUT_SHUTDOWN_BELOW; drop it so
	// the file does not carry a dead setting.
	kept := lines[:0]
	for _, l := range lines {
		if !strings.HasPrefix(l, "ECONUT_TRIGGER_BELOW=") {
			kept = append(kept, l)
		}
	}
	lines = kept
	for _, p := range kv {
		found := false
		for i, l := range lines {
			if strings.HasPrefix(l, p[0]+"=") {
				lines[i], found = p[0]+"="+p[1], true
			}
		}
		if !found {
			lines = append(lines, p[0]+"="+p[1])
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// writeFileAtomic replaces path with content (mode 0644) via a temp file.
func writeFileAtomic(path, content string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".econut-conf-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// runSet implements `econut set`: validate, save to the config file, restart the guard.
func runSet(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("econut set", flag.ContinueOnError)
	fs.SetOutput(errOut)
	trigger := fs.String("shutdown-below", "", "shut down when on battery and charge is below this percent, e.g. 40")
	debounce := fs.String("debounce", "", "condition must hold this long before shutting down, e.g. 90s")
	conf := fs.String("conf", defaultConf, "config file read by the econut-guard service")
	noRestart := fs.Bool("no-restart", false, "save only; do not restart the service")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	usage := func(format string, a ...any) int {
		fmt.Fprintf(errOut, "econut set: "+format+"\n", a...)
		return 2
	}
	if fs.NArg() > 0 {
		return usage("unexpected argument %q", fs.Arg(0))
	}
	if !given["shutdown-below"] && !given["debounce"] {
		return usage("nothing to set (usage: econut set [--shutdown-below N] [--debounce DURATION])")
	}

	var kv [][2]string
	if given["shutdown-below"] {
		v, err := strconv.ParseFloat(*trigger, 64)
		if err != nil || math.IsNaN(v) || v <= 0 || v > 100 {
			return usage("--shutdown-below must be a percent in (0, 100], got %q", *trigger)
		}
		kv = append(kv, [2]string{"ECONUT_SHUTDOWN_BELOW", strconv.FormatFloat(v, 'f', -1, 64)})
	}
	if given["debounce"] {
		d, err := time.ParseDuration(*debounce)
		if err != nil || d < 0 {
			return usage("--debounce must be a duration like 120s or 2m, got %q", *debounce)
		}
		val := d.String()
		if d%time.Second == 0 {
			val = fmt.Sprintf("%ds", int64(d/time.Second)) // "60s", not Go's "1m0s"
		}
		kv = append(kv, [2]string{"ECONUT_DEBOUNCE", val})
	}

	old, err := os.ReadFile(*conf)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(errOut, "econut: cannot read %s: %v\n", *conf, err)
		return 1
	}
	if err := writeFileAtomic(*conf, setConf(string(old), kv)); err != nil {
		fmt.Fprintf(errOut, "econut: cannot write %s: %v (try: sudo econut set ...)\n", *conf, err)
		return 1
	}
	for _, p := range kv {
		if p[0] == "ECONUT_SHUTDOWN_BELOW" {
			fmt.Fprintf(out, "shutdown level set to %s%%\n", p[1])
		} else {
			fmt.Fprintf(out, "debounce set to %s\n", p[1])
		}
	}

	if *noRestart {
		fmt.Fprintln(out, "saved; restart the service to apply: sudo systemctl restart econut-guard")
		return 0
	}
	if err := restartGuard(); err != nil {
		fmt.Fprintf(errOut, "econut: saved to %s but could not restart the guard: %v\n", *conf, err)
		return 1
	}
	fmt.Fprintln(out, "econut-guard restarted; check with: econut status")
	return 0
}
