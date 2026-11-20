//go:build unix

package relaunch

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWaitExitedReturnsOnceEachHasEnded(t *testing.T) {
	quick := exec.Command("/bin/sh", "-c", "exit 0")
	if err := quick.Run(); err != nil {
		t.Fatal(err)
	}
	slow := exec.Command("/bin/sleep", "0.3")
	if err := slow.Start(); err != nil {
		t.Fatal(err)
	}
	go slow.Wait()
	start := time.Now()
	if err := WaitExited([]int{quick.Process.Pid, slow.Process.Pid}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < 250*time.Millisecond {
		t.Fatalf("returned after %s, before the slow one ended", took)
	}
}

// After starts the program only once the asking process has gone: here a
// helper process stands in for the app.
func TestAfterWaitsForTheProcessToEnd(t *testing.T) {
	if os.Getenv("RELAUNCH_HELPER") != "" {
		if err := After("/bin/sh", []string{"-c", `echo started > "$0"`, os.Getenv("RELAUNCH_HELPER")}); err != nil {
			os.Exit(1)
		}
		time.Sleep(300 * time.Millisecond)
		if _, err := os.Stat(os.Getenv("RELAUNCH_HELPER")); err == nil {
			os.Exit(2) // started while this process still ran
		}
		os.Exit(0)
	}
	marker := filepath.Join(t.TempDir(), "started")
	cmd := exec.Command(os.Args[0], "-test.run", "TestAfterWaitsForTheProcessToEnd")
	cmd.Env = append(os.Environ(), "RELAUNCH_HELPER="+marker)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v %s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the program was not started after the process ended")
}
