// Command demo is a Flatpak that updates itself through amarit/flatpak,
// for trying the portal on a real desktop. It logs every state to
// $XDG_DATA_HOME/amarit-demo.log with the commit it runs, shows the ready
// notification with Relaunch, and with -auto-relaunch relaunches by itself
// once an update is ready. README.md says how to build its repository.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sudiptadeb/amarit"
	"github.com/sudiptadeb/amarit/appupdate"
	"github.com/sudiptadeb/amarit/flatpak"
)

// build is what this commit says it is, set with -ldflags, so the log shows
// the relaunch reached the new deployment.
var build = "1"

func main() {
	auto := flag.Bool("auto-relaunch", false, "relaunch as soon as an update is ready")
	flag.Parse()
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = os.TempDir()
	}
	f, err := os.OpenFile(filepath.Join(data, "amarit-demo.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err == nil {
		log.SetOutput(f)
	}
	log.Printf("demo build %s, commit %s, pid %d", build, commit(), os.Getpid())

	done := make(chan struct{})
	src, err := flatpak.New(flatpak.Options{Args: os.Args, Quit: func() { close(done) }})
	if err != nil {
		log.Fatal(err)
	}
	// Settings only: a Flatpak's releases come from its repository.
	c, err := amarit.NewClient(filepath.Join(data, "update"), "stable",
		amarit.ProjectFile([]byte(`{"project":"demo","releases":"https://example.invalid/releases.json","auto_update":true}`)))
	if err != nil {
		log.Fatal(err)
	}
	u := appupdate.New(appupdate.Config{Client: c, Source: src, Dir: filepath.Join(data, "update"), Interval: time.Minute})
	u.OnChange(func(s appupdate.State) {
		log.Printf("state %+v", s)
		if *auto && s.Phase == appupdate.Ready {
			go func() { time.Sleep(2 * time.Second); u.Relaunch() }()
		}
	})
	if err := src.Present(u, flatpak.Words{}); err != nil {
		log.Printf("present: %v", err)
	}
	u.Start()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	select {
	case <-done:
		log.Printf("relaunched; build %s ends", build)
	case s := <-sigs:
		log.Printf("%v", s)
	}
	u.Close()
	src.Close()
}

// commit is the commit this deployment is, from /.flatpak-info.
func commit() string {
	f, err := os.Open("/.flatpak-info")
	if err != nil {
		return "(not in a Flatpak)"
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "app-commit="); ok {
			return v
		}
	}
	return fmt.Sprint("(no app-commit)")
}
