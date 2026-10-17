package econut

import (
	"os/exec"
	"strings"
	"testing"
)

// TestInstallShFunctions covers the part of deploy/install.sh that builds the
// MONITOR line for upsmon.conf. A non-root `install.sh --dry-run` cannot reach
// it, because it needs to read the password out of /etc/nut/upsd.users, and
// getting it wrong is not a visible failure: it writes a config that upsmon
// rejects, which is the dead nut-monitor.service this file exists to prevent.
// The script pulls the functions out of the shipped install.sh and runs them,
// so it tests what is actually installed rather than a copy.
func TestInstallShFunctions(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	out, err := exec.Command(bash, "testdata/install_sh_functions.sh", "deploy/install.sh").CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh function tests failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ALL PASS") {
		t.Fatalf("install.sh function tests did not report ALL PASS:\n%s", out)
	}
}

// TestInstallShSyntax catches a syntax error in the installer, which otherwise
// only shows up when someone runs it as root.
func TestInstallShSyntax(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	if out, err := exec.Command(bash, "-n", "deploy/install.sh").CombinedOutput(); err != nil {
		t.Fatalf("deploy/install.sh has a syntax error: %v\n%s", err, out)
	}
}
