//go:build unix

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/debkosh/amarit"
)

// A job is a command run once, off the shell that started it: a build, a
// test run, a release. A unit is kept running and comes back after a daemon
// restart or a reboot; a job never runs again. A small amarit process, the
// job's keeper, starts the command in a session of its own and waits for it.
// The daemon plays no part, and the keeper is a process of its own image, so
// an update of the daemon or of the amarit binary cannot touch a running job.
//
// amarit knows a job only while it runs: its record and, without -log, its
// output live in ~/.amarit/jobs/ until it ends, and then go. With -log the
// output is appended to that file, the caller's, and the keeper ends it with
// a line saying how the job ended.
type job struct {
	Name    string    `json:"name"`
	Command string    `json:"command"`
	Args    []string  `json:"args,omitempty"`
	Dir     string    `json:"dir"`
	EnvFile string    `json:"env_file,omitempty"`
	Log     string    `json:"log"` // where the output goes
	Started time.Time `json:"started"`
	Keeper  int       `json:"keeper,omitempty"` // the keeper's pid
	PID     int       `json:"pid,omitempty"`    // the command's pid, its process group
	Error   string    `json:"error,omitempty"`  // why it could not start, for startJob to say
}

var jobNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func jobsDir() string { return filepath.Join(string(amarit.DefaultBase()), "jobs") }

func jobPath(name string) string { return filepath.Join(jobsDir(), name+".json") }

// ownLog is the log of a job started without -log, gone when the job ends.
func ownLog(name string) string { return filepath.Join(jobsDir(), name+".log") }

// alive says whether pid is a process; EPERM is one owned by someone else.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// running says whether the job's keeper is still there. A record whose
// keeper is gone without removing it is a job a reboot or a kill -9 ended.
func (j job) running() bool { return j.Keeper != 0 && alive(j.Keeper) }

func readJob(name string) (job, error) {
	var j job
	b, err := os.ReadFile(jobPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return j, fmt.Errorf("no job named %s running", name)
	}
	if err != nil {
		return j, err
	}
	return j, json.Unmarshal(b, &j)
}

// writeJob renames a new record over the old, so no reader sees half of one.
func writeJob(j job) error {
	b, _ := json.MarshalIndent(j, "", "  ")
	tmp := jobPath(j.Name) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, jobPath(j.Name))
}

func forgetJob(name string) {
	os.Remove(jobPath(name))
	os.Remove(waitSock(name))
	os.Remove(ownLog(name))
}

// isJob says whether name is a running job, for stop and logs.
func isJob(name string) bool {
	j, err := readJob(name)
	return err == nil && j.running()
}

// listJobs reads the running jobs, oldest first, and forgets those whose
// keeper is gone.
func listJobs() ([]job, error) {
	paths, err := filepath.Glob(filepath.Join(jobsDir(), "*.json"))
	if err != nil {
		return nil, err
	}
	var out []job
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".json")
		j, err := readJob(name)
		if err != nil {
			continue
		}
		if !j.running() {
			// A keeper writes its pid before anything else; a record without
			// one this young is a job starting.
			if j.Keeper == 0 && time.Since(j.Started) < 10*time.Second {
				continue
			}
			forgetJob(name)
			continue
		}
		out = append(out, j)
	}
	slices.SortFunc(out, func(a, b job) int { return a.Started.Compare(b.Started) })
	return out, nil
}

// startJob is `amarit job`: it records the job, starts its keeper and
// returns once the command is running, or has failed to start.
func startJob(args []string) error {
	fs := flag.NewFlagSet("job", flag.ContinueOnError)
	name := fs.String("name", "", "job name (default: the command's name and the time)")
	dir := fs.String("dir", "", "working directory (default: this one)")
	envFile := fs.String("env-file", "", "file of KEY=VALUE lines added to the environment")
	logFile := fs.String("log", "", "append the output to this file, and a last line saying how the job ended")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: amarit job [-name N] [-dir D] [-env-file F] [-log F] [--] <command> [args...]")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return errors.New("a command is required")
	}
	bin, err := exec.LookPath(fs.Arg(0))
	if err != nil {
		return err
	}
	if bin, err = filepath.Abs(bin); err != nil {
		return err
	}
	if *dir == "" {
		if *dir, err = os.Getwd(); err != nil {
			return err
		}
	}
	for _, p := range []*string{dir, envFile, logFile} {
		if *p != "" {
			if *p, err = filepath.Abs(*p); err != nil {
				return err
			}
		}
	}
	now := time.Now()
	if *name == "" {
		*name = filepath.Base(bin) + "-" + now.Format("20060102-150405")
	}
	if !jobNameRe.MatchString(*name) {
		return fmt.Errorf("job name %q: letters, digits, '.', '_' and '-' only", *name)
	}
	if err := os.MkdirAll(jobsDir(), 0o755); err != nil {
		return err
	}
	if _, err := listJobs(); err != nil {
		return err
	}
	if _, err := readJob(*name); err == nil {
		return fmt.Errorf("job %s is running", *name)
	}
	j := job{Name: *name, Command: bin, Args: fs.Args()[1:], Dir: *dir, EnvFile: *envFile, Log: *logFile, Started: now}
	if j.Log == "" {
		j.Log = ownLog(j.Name)
		os.Remove(j.Log)
	}
	if err := writeJob(j); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer null.Close()
	keeper := exec.Command(self, "__job", j.Name)
	keeper.Stdin, keeper.Stdout, keeper.Stderr = null, null, null
	keeper.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := keeper.Start(); err != nil {
		forgetJob(j.Name)
		return err
	}
	keeper.Process.Release()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		j2, err := readJob(j.Name)
		switch {
		case err != nil:
			// Gone already: it ran and ended, and its log says how.
			fmt.Printf("job %s ended at once; see %s\n", j.Name, j.Log)
			return nil
		case j2.Error != "":
			forgetJob(j.Name)
			return fmt.Errorf("job %s: %s", j.Name, j2.Error)
		case j2.PID != 0:
			fmt.Printf("job %s running, pid %d, log %s\n", j.Name, j2.PID, j2.Log)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("job %s did not start", j.Name)
}

// keepJob is the hidden `amarit __job <name>`: the keeper. It runs the
// command with its output in the log, passes a stop on to it, writes how it
// ended to a -log file, and forgets the job.
func keepJob(args []string) error {
	if len(args) != 1 {
		return errors.New("__job: a job name is required")
	}
	j, err := readJob(args[0])
	if err != nil {
		return err
	}
	j.Keeper = os.Getpid()
	fail := func(err error) error {
		j.Error = err.Error()
		return writeJob(j)
	}
	log, err := os.OpenFile(j.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fail(err)
	}
	defer log.Close()
	env := os.Environ()
	if j.EnvFile != "" {
		extra, err := amarit.ReadEnvFile(j.EnvFile)
		if err != nil {
			return fail(err)
		}
		env = append(env, extra...)
	}
	cmd := exec.Command(j.Command, j.Args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = j.Dir, env, log, log
	// Its own process group, so a stop reaches what it starts as well.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Signals for the keeper go to the command; the keeper stays to write
	// how it ended.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	// Listening before the record says the job runs, so a wait right after
	// amarit job finds it.
	waiters := listenWaiters(j.Name)
	j.PID = cmd.Process.Pid
	if err := writeJob(j); err != nil {
		cmd.Process.Kill()
		return err
	}
	go func() {
		for s := range sigs {
			syscall.Kill(-j.PID, s.(syscall.Signal))
		}
	}()
	end := ended(cmd.Wait())
	if j.Log != ownLog(j.Name) {
		fmt.Fprintf(log, "amarit job %s: %s\n", j.Name, end)
	}
	forgetJob(j.Name)
	waiters(end)
	return nil
}

// waitSock is where a running job's keeper answers `amarit wait`.
func waitSock(name string) string { return filepath.Join(jobsDir(), name+".sock") }

// listenWaiters takes `amarit wait` connections while the job runs; the
// function it returns tells each how the job ended and closes them. Nothing
// is kept: a waiter hears the end only while it is connected.
func listenWaiters(name string) func(end string) {
	os.Remove(waitSock(name))
	ln, err := net.Listen("unix", waitSock(name))
	if err != nil {
		return func(string) {}
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	return func(end string) {
		ln.Close()
		os.Remove(waitSock(name))
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			fmt.Fprintln(c, end)
			c.Close()
		}
	}
}

// waitJob is `amarit wait N`: it blocks until the running job N ends,
// prints how, and exits with its code; a job a signal ended exits 128 plus
// the signal's number, as a shell reports it.
func waitJob(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: amarit wait <job>")
	}
	c, err := net.Dial("unix", waitSock(args[0]))
	if err != nil {
		return fmt.Errorf("no job named %s running", args[0])
	}
	defer c.Close()
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return fmt.Errorf("job %s: its keeper ended without saying how", args[0])
	}
	end := strings.TrimSpace(line)
	fmt.Printf("job %s: %s\n", args[0], end)
	var code int
	switch {
	case strings.HasPrefix(end, "exit "):
		code, _ = strconv.Atoi(strings.TrimPrefix(end, "exit "))
	case strings.HasPrefix(end, "killed: "):
		code = 128 + signalNumber(strings.TrimPrefix(end, "killed: "))
	default:
		code = 1
	}
	os.Exit(code)
	return nil
}

func signalNumber(name string) int {
	for n := syscall.Signal(1); n < 32; n++ {
		if n.String() == name {
			return int(n)
		}
	}
	return 0
}

// ended words how a command ended: "exit N", or "killed: <signal>".
func ended(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return "killed: " + ws.Signal().String()
		}
		return fmt.Sprintf("exit %d", ee.ExitCode())
	}
	if err != nil {
		return "failed: " + err.Error()
	}
	return "exit 0"
}

// stopJob sends SIGTERM to a running job's process group, and SIGKILL to
// what is left after grace.
func stopJob(name string, grace time.Duration) error {
	j, err := readJob(name)
	if err != nil {
		return err
	}
	if !j.running() {
		return fmt.Errorf("no job named %s running", name)
	}
	syscall.Kill(-j.PID, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if syscall.Kill(-j.PID, 0) != nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return syscall.Kill(-j.PID, syscall.SIGKILL)
}

// jobLogPath is the log `amarit logs` shows for a running job.
func jobLogPath(name string) (string, error) {
	j, err := readJob(name)
	if err != nil {
		return "", err
	}
	return j.Log, nil
}

// showJobs is `amarit jobs`: the running jobs, oldest first.
func showJobs(args []string) error {
	jobs, err := listJobs()
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "JOB\tPID\tRUNNING\tLOG\tCOMMAND")
	for _, j := range jobs {
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s %s\n", j.Name, j.PID, time.Since(j.Started).Round(time.Second), j.Log, filepath.Base(j.Command), strings.Join(j.Args, " "))
	}
	return w.Flush()
}
