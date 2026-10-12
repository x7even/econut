package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleConf = `# comment kept
ECONUT_TRIGGER_BELOW=55
# note
ECONUT_DEBOUNCE=120s
`

func setup(t *testing.T, content string) (conf string, restarts *int) {
	t.Helper()
	conf = filepath.Join(t.TempDir(), "econut.conf")
	if content != "" {
		if err := os.WriteFile(conf, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	old := restartGuard
	restartGuard = func() error { n++; return nil }
	t.Cleanup(func() { restartGuard = old })
	return conf, &n
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSetTriggerKeepsRestAndRestarts(t *testing.T) {
	conf, restarts := setup(t, sampleConf)
	code, out, errOut := runCmd(t, time.Now(), "set", "--conf", conf, "--trigger-below", "40")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	got := read(t, conf)
	if !strings.Contains(got, "ECONUT_TRIGGER_BELOW=40\n") || !strings.Contains(got, "ECONUT_DEBOUNCE=120s\n") ||
		!strings.Contains(got, "# comment kept") || !strings.Contains(got, "# note") {
		t.Fatalf("conf = %q", got)
	}
	if *restarts != 1 || !strings.Contains(out, "40%") {
		t.Fatalf("restarts=%d out=%q", *restarts, out)
	}
}

func TestSetBothAndMissingKeysAppended(t *testing.T) {
	conf, _ := setup(t, "ECONUT_TRIGGER_BELOW=55\n") // no debounce line
	code, _, errOut := runCmd(t, time.Now(), "set", "--conf", conf, "--trigger-below", "30", "--debounce", "90s")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	got := read(t, conf)
	if !strings.Contains(got, "ECONUT_TRIGGER_BELOW=30\n") || !strings.Contains(got, "ECONUT_DEBOUNCE=90s\n") {
		t.Fatalf("conf = %q", got)
	}
}

func TestSetCreatesMissingFile(t *testing.T) {
	conf, _ := setup(t, "")
	if code, _, e := runCmd(t, time.Now(), "set", "--conf", conf, "--debounce", "60s"); code != 0 {
		t.Fatalf("code=%d err=%q", code, e)
	}
	if !strings.Contains(read(t, conf), "ECONUT_DEBOUNCE=60s\n") {
		t.Fatal("not written")
	}
}

func TestSetRejectsBadValuesAndLeavesFileAlone(t *testing.T) {
	for _, args := range [][]string{
		{"--trigger-below", "0"}, {"--trigger-below", "101"}, {"--trigger-below", "abc"},
		{"--trigger-below", "NaN"}, {"--trigger-below", ""},
		{"--debounce", "-5s"}, {"--debounce", "soon"}, {"--debounce", ""},
		{}, // nothing to set
	} {
		conf, restarts := setup(t, sampleConf)
		code, _, _ := runCmd(t, time.Now(), append([]string{"set", "--conf", conf}, args...)...)
		if code != 2 {
			t.Errorf("args %v: code=%d, want 2", args, code)
		}
		if read(t, conf) != sampleConf || *restarts != 0 {
			t.Errorf("args %v: file changed or restarted", args)
		}
	}
}

func TestSetNoRestart(t *testing.T) {
	conf, restarts := setup(t, sampleConf)
	if code, _, _ := runCmd(t, time.Now(), "set", "--conf", conf, "--trigger-below", "70", "--no-restart"); code != 0 {
		t.Fatal("failed")
	}
	if *restarts != 0 {
		t.Fatal("restarted despite --no-restart")
	}
}

func TestSetRestartFailureIsReportedButConfSaved(t *testing.T) {
	conf, _ := setup(t, sampleConf)
	restartGuard = func() error { return errors.New("unit not found") }
	code, _, errOut := runCmd(t, time.Now(), "set", "--conf", conf, "--trigger-below", "70")
	if code != 1 || !strings.Contains(errOut, "unit not found") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if !strings.Contains(read(t, conf), "ECONUT_TRIGGER_BELOW=70\n") {
		t.Fatal("conf should still be saved")
	}
}

func TestSetUnwritableConfSuggestsSudo(t *testing.T) {
	setup(t, "")
	code, _, errOut := runCmd(t, time.Now(), "set", "--conf", "/nonexistent-dir/econut.conf", "--trigger-below", "50")
	if code != 1 || !strings.Contains(errOut, "sudo") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
}
