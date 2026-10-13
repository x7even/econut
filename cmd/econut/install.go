package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/x7even/econut"
)

// executablePath and execInstaller are variables so tests never run the real installer.
var (
	executablePath = os.Executable

	execInstaller = func(script string, args []string, out, errOut io.Writer) error {
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, out, errOut
		return cmd.Run()
	}
)

func copyFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, mode)
}

// stage lays out dir/deploy (the embedded files) and dir/bin (this binary and
// its sibling econut-guard), the layout deploy/install.sh expects.
func stage(dir, exe string) error {
	guard := filepath.Join(filepath.Dir(exe), "econut-guard")
	if _, err := os.Stat(guard); err != nil {
		return fmt.Errorf("econut-guard not found next to %s; install both with: go install github.com/x7even/econut/cmd/...@latest", exe)
	}
	err := fs.WalkDir(econut.Deploy, "deploy", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := econut.Deploy.ReadFile(p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		return os.WriteFile(dst, b, mode)
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		return err
	}
	if err := copyFile(exe, filepath.Join(dir, "bin", "econut"), 0o755); err != nil {
		return err
	}
	return copyFile(guard, filepath.Join(dir, "bin", "econut-guard"), 0o755)
}

// runInstall implements `sudo econut install [installer options]`. Every
// argument goes to the installer (--shutdown-below N, --enable-service,
// --dry-run, --uninstall; see `econut install --help`).
func runInstall(args []string, out, errOut io.Writer) int {
	exe, err := executablePath()
	if err != nil {
		fmt.Fprintf(errOut, "econut: cannot locate itself: %v\n", err)
		return 1
	}
	dir, err := os.MkdirTemp("", "econut-install-")
	if err != nil {
		fmt.Fprintf(errOut, "econut: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)
	if err := stage(dir, exe); err != nil {
		fmt.Fprintf(errOut, "econut: %v\n", err)
		return 1
	}
	if err := execInstaller(filepath.Join(dir, "deploy", "install.sh"), args, out, errOut); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode() // the installer already said what went wrong
		}
		fmt.Fprintf(errOut, "econut: install failed: %v\n", err)
		return 1
	}
	return 0
}
