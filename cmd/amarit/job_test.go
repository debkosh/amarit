//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// buildAmarit builds this command into dir, as an installed amarit.
func buildAmarit(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "amarit")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func amaritCmd(t *testing.T, bin, home string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "AMARIT_HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("amarit %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A job runs once, off the shell, through a replacement of the amarit
// binary under it, as an update makes; its output and how it ended land in
// the caller's -log file, and amarit keeps nothing of it once it ends.
func TestJobSurvivesAnUpdateAndLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	bin := buildAmarit(t, dir)
	gate := filepath.Join(dir, "gate")
	logFile := filepath.Join(dir, "build.log")
	out := amaritCmd(t, bin, home, "job", "-name", "build", "-log", logFile, "--",
		"/bin/sh", "-c", `echo started; while [ ! -e "$1" ]; do sleep 0.05; done; echo done; exit 3`, "sh", gate)
	if !strings.Contains(out, "job build running") {
		t.Fatalf("job said %q", out)
	}
	if out := amaritCmd(t, bin, home, "jobs"); !strings.Contains(out, "build") {
		t.Fatalf("jobs while running: %q", out)
	}

	// An update renames a new binary over the old one.
	next := filepath.Join(dir, "next")
	if err := os.MkdirAll(next, 0o755); err != nil {
		t.Fatal(err)
	}
	newBin := buildAmarit(t, next)
	if err := os.Rename(newBin, bin); err != nil {
		t.Fatal(err)
	}
	if out := amaritCmd(t, bin, home, "jobs"); !strings.Contains(out, "build") {
		t.Fatalf("jobs after the update: %q", out)
	}

	os.WriteFile(gate, nil, 0o644)
	waitUntil(t, "the job to end", func() bool {
		b, _ := os.ReadFile(logFile)
		return strings.Contains(string(b), "amarit job build: exit 3")
	})
	b, _ := os.ReadFile(logFile)
	if got := string(b); !strings.HasPrefix(got, "started\ndone\n") {
		t.Fatalf("log: %q", got)
	}
	left, _ := os.ReadDir(filepath.Join(home, "jobs"))
	if len(left) != 0 {
		t.Fatalf("amarit kept %v after the job ended", left)
	}
	if out := amaritCmd(t, bin, home, "jobs"); strings.Contains(out, "build") {
		t.Fatalf("jobs after the end: %q", out)
	}
}

// amarit stop ends a job's whole process group, and the log says how.
func TestJobStop(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	bin := buildAmarit(t, dir)
	logFile := filepath.Join(dir, "job.log")
	amaritCmd(t, bin, home, "job", "-name", "sleepy", "-log", logFile, "--", "/bin/sh", "-c", "sleep 60 & wait")
	amaritCmd(t, bin, home, "stop", "sleepy")
	waitUntil(t, "the stopped job's last line", func() bool {
		b, _ := os.ReadFile(logFile)
		return strings.Contains(string(b), "amarit job sleepy: killed: terminated")
	})
}

// A job whose keeper died without forgetting it, as at a reboot, is
// forgotten by the next look; a job with no -log leaves no log behind.
func TestJobForgetsWhatAKilledKeeperLeft(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	bin := buildAmarit(t, dir)
	amaritCmd(t, bin, home, "job", "-name", "orphan", "--", "/bin/sh", "-c", "sleep 60")
	var j job
	os.Setenv("AMARIT_HOME", home)
	defer os.Unsetenv("AMARIT_HOME")
	j, err := readJob("orphan")
	if err != nil {
		t.Fatal(err)
	}
	exec.Command("kill", "-9", itoa(j.Keeper), "-"+itoa(j.PID)).Run()
	waitUntil(t, "the keeper to be gone", func() bool { return !alive(j.Keeper) })
	if out := amaritCmd(t, bin, home, "jobs"); strings.Contains(out, "orphan") {
		t.Fatalf("jobs listed a dead job: %q", out)
	}
	if left, _ := os.ReadDir(filepath.Join(home, "jobs")); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// amarit wait blocks until the job ends and exits with its code.
func TestJobWait(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	bin := buildAmarit(t, dir)
	amaritCmd(t, bin, home, "job", "-name", "w", "--", "/bin/sh", "-c", "sleep 0.5; exit 7")
	start := time.Now()
	cmd := exec.Command(bin, "wait", "w")
	cmd.Env = append(os.Environ(), "AMARIT_HOME="+home)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 || !strings.Contains(string(out), "exit 7") {
		t.Fatalf("wait: %v %q", err, out)
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Fatal("wait returned before the job ended")
	}
	cmd = exec.Command(bin, "wait", "w")
	cmd.Env = append(os.Environ(), "AMARIT_HOME="+home)
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "no job named w running") {
		t.Fatalf("wait on an ended job: %v %q", err, out)
	}
}
