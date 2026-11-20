//go:build unix

// Package relaunch starts a program again once the process asking has gone:
// the restart after an update for a program that installs itself, which
// cannot start the new copy any earlier without two copies running on the
// same state.
package relaunch

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// After starts executable with args from a shell that outlives this process
// and waits for it to exit. The caller then ends itself.
func After(executable string, args []string) error {
	script := `pid=$1; shift; while kill -0 "$pid" 2>/dev/null; do sleep 0.2; done; exec "$@"`
	cmd := exec.Command("/bin/sh", append([]string{"-c", script, "relaunch", strconv.Itoa(os.Getpid()), executable}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
