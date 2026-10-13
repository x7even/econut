package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeInstallEnv(t *testing.T, withGuard bool) {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "econut")
	os.WriteFile(exe, []byte("econut-binary"), 0o755)
	if withGuard {
		os.WriteFile(filepath.Join(dir, "econut-guard"), []byte("guard-binary"), 0o755)
	}
	oldExe, oldExec := executablePath, execInstaller
	executablePath = func() (string, error) { return exe, nil }
	t.Cleanup(func() { executablePath, execInstaller = oldExe, oldExec })
}

func TestInstallStagesFilesAndRunsInstaller(t *testing.T) {
	fakeInstallEnv(t, true)
	var script string
	var gotArgs []string
	var staged map[string]os.FileMode
	execInstaller = func(path string, args []string, _, _ io.Writer) error {
		script, gotArgs = path, args
		root := filepath.Dir(filepath.Dir(path))
		staged = map[string]os.FileMode{}
		for _, rel := range []string{"deploy/install.sh", "deploy/econut-guard.service", "deploy/99-ecoflow-ups.rules",
			"deploy/nut.conf", "deploy/ups.conf", "deploy/upsd.conf", "deploy/upsd.users", "deploy/econut.conf",
			"bin/econut", "bin/econut-guard"} {
			fi, err := os.Stat(filepath.Join(root, rel))
			if err != nil {
				t.Errorf("missing staged %s: %v", rel, err)
				continue
			}
			staged[rel] = fi.Mode()
		}
		return nil
	}
	code, _, errOut := runCmd(t, time.Now(), "install", "--enable-service", "--shutdown-below", "40")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if strings.Join(gotArgs, " ") != "--enable-service --shutdown-below 40" {
		t.Fatalf("args not passed through: %v", gotArgs)
	}
	if staged["deploy/install.sh"]&0o100 == 0 || staged["bin/econut-guard"]&0o100 == 0 {
		t.Fatalf("install.sh and the binaries must be executable: %v", staged)
	}
	if _, err := os.Stat(script); err == nil {
		t.Fatal("staging directory must be removed afterwards")
	}
}

func TestInstallNeedsSiblingGuardBinary(t *testing.T) {
	fakeInstallEnv(t, false)
	execInstaller = func(string, []string, io.Writer, io.Writer) error { t.Fatal("must not run"); return nil }
	code, _, errOut := runCmd(t, time.Now(), "install")
	if code != 1 || !strings.Contains(errOut, "econut-guard") || !strings.Contains(errOut, "go install") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
}

func TestInstallPropagatesInstallerFailure(t *testing.T) {
	fakeInstallEnv(t, true)
	execInstaller = func(string, []string, io.Writer, io.Writer) error { return errors.New("boom") }
	if code, _, errOut := runCmd(t, time.Now(), "install"); code != 1 || !strings.Contains(errOut, "boom") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
}
