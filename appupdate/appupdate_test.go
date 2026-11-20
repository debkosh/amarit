package appupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sudiptadeb/amarit"
)

// release serves a manifest whose stable channel names version, with asset
// as its file; hang makes the asset stop after its first bytes.
type release struct {
	mu      sync.Mutex
	version string
	asset   []byte
	sum     string // the manifest's sha256; the asset's own unless set
	hang    chan struct{}
}

func (r *release) set(version string, asset []byte) {
	r.mu.Lock()
	r.version, r.asset, r.sum = version, asset, ""
	r.mu.Unlock()
}

func serve(t *testing.T, r *release) string {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/releases.json", func(w http.ResponseWriter, _ *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		sum := r.sum
		if sum == "" {
			h := sha256.Sum256(r.asset)
			sum = hex.EncodeToString(h[:])
		}
		fmt.Fprintf(w, `{"spec":1,"project":"demo","published":"2026-11-20T00:00:00Z",
		  "channels":{"stable":"%s","dev":"%s"},
		  "releases":{"%s":{"assets":{"%s/%s":{"url":"%s/asset","sha256":"%s","size":%d}}}}}`,
			r.version, r.version, r.version, runtime.GOOS, runtime.GOARCH, srv.URL, sum, len(r.asset))
	})
	mux.HandleFunc("/asset", func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		asset, hang := r.asset, r.hang
		r.mu.Unlock()
		if hang != nil {
			w.Header().Set("Content-Length", fmt.Sprint(len(asset)))
			w.Write(asset[:1])
			w.(http.Flusher).Flush()
			select {
			case <-hang:
			case <-req.Context().Done():
			}
			return
		}
		w.Write(asset)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/releases.json"
}

// installer stands in for a bundle: it refuses an asset that says "bad",
// and records what it installed.
type installer struct {
	mu        sync.Mutex
	dir       string
	installed string
	other     string // a release another copy staged
	otherV    string
}

func (f *installer) Usable() error { return nil }
func (f *installer) Prepare(ctx context.Context, file, version string) (string, error) {
	b, _ := os.ReadFile(file)
	if string(b) == "bad" {
		return "", fmt.Errorf("its signature is broken: %w", amarit.ErrRefused)
	}
	staged := filepath.Join(f.dir, "staged-"+version)
	return staged, os.Rename(file, staged)
}
func (f *installer) Staged() (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.other, f.otherV
}
func (f *installer) Install(staged string) error {
	f.mu.Lock()
	f.installed = staged
	f.mu.Unlock()
	return nil
}

type rig struct {
	t   *testing.T
	rel *release
	in  *installer
	u   *Updater
	c   *amarit.Client
	mu  sync.Mutex
	saw []State
}

func newRig(t *testing.T, running string) *rig {
	t.Helper()
	r := &rig{t: t, rel: &release{version: "1.1.0", asset: []byte("good release")}}
	url := serve(t, r.rel)
	state := t.TempDir()
	c, err := amarit.NewClient(state, "stable", amarit.Project("demo"), amarit.Version(running), amarit.Releases(url))
	if err != nil {
		t.Fatal(err)
	}
	r.c = c
	r.in = &installer{dir: t.TempDir()}
	r.u = New(Config{Client: c, Installer: r.in, Dir: state, Interval: time.Hour, FirstCheck: time.Hour, StallAfter: 300 * time.Millisecond, Logf: t.Logf})
	r.u.OnChange(func(s State) {
		r.mu.Lock()
		r.saw = append(r.saw, s)
		r.mu.Unlock()
	})
	return r
}

func (r *rig) waitFor(what string, cond func(State) bool) State {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := r.u.State(); cond(s) {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("timed out waiting for %s; state %+v", what, r.u.State())
	return State{}
}

func phase(p Phase) func(State) bool { return func(s State) bool { return s.Phase == p } }

// The phases a check goes through, in order, each heard by the listener.
func TestCheckDownloadsChecksAndStagesARelease(t *testing.T) {
	r := newRig(t, "1.0.0")
	r.u.Check()
	s := r.waitFor("ready", phase(Ready))
	if s.Version != "1.1.0" || s.Channel != "stable" || !s.AutoUpdate {
		t.Fatalf("ready: %+v", s)
	}
	time.Sleep(50 * time.Millisecond)
	r.mu.Lock()
	var phases []Phase
	for _, s := range r.saw {
		if len(phases) == 0 || phases[len(phases)-1] != s.Phase {
			phases = append(phases, s.Phase)
		}
	}
	r.mu.Unlock()
	if fmt.Sprint(phases) != fmt.Sprint([]Phase{Checking, Downloading, Verifying, Ready}) {
		t.Fatalf("phases heard: %v", phases)
	}
	r.u.Relaunch()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.in.mu.Lock()
		done := r.in.installed != ""
		r.in.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Relaunch did not install")
}

func TestUpToDateAndOffline(t *testing.T) {
	r := newRig(t, "1.1.0")
	r.u.Check()
	r.waitFor("up to date", phase(UpToDate))
	r2 := newRig(t, "1.0.0")
	r2.u.cfg.Client, _ = amarit.NewClient(t.TempDir(), "stable", amarit.Project("demo"), amarit.Version("1.0.0"), amarit.Releases("http://127.0.0.1:1/releases.json"))
	r2.u.Check()
	if s := r2.waitFor("failed", phase(Failed)); s.Kind != Offline || !s.Kind.Retryable() {
		t.Fatalf("offline: %+v", s)
	}
}

// A refused release is rejected: an automatic round leaves it alone, and a
// check the person asks for tries it again.
func TestARefusedReleaseIsNotTriedAgainUntilAsked(t *testing.T) {
	r := newRig(t, "1.0.0")
	r.rel.set("1.1.0", []byte("bad"))
	r.u.Check()
	if s := r.waitFor("failed", phase(Failed)); s.Kind != Refused || s.Kind.Retryable() || s.Version != "1.1.0" {
		t.Fatalf("refused: %+v", s)
	}
	r.u.Retry() // not taken after a refusal
	r.u.round()
	if s := r.u.State(); s.Phase != Failed || s.Kind != Refused {
		t.Fatalf("an automatic round after the refusal: %+v", s)
	}
	r.rel.set("1.1.0", []byte("fixed"))
	r.u.Check()
	r.waitFor("ready", phase(Ready))
}

func TestADamagedDownloadIsRetried(t *testing.T) {
	r := newRig(t, "1.0.0")
	r.rel.mu.Lock()
	r.rel.sum = strings.Repeat("0", 64)
	r.rel.mu.Unlock()
	r.u.Check()
	if s := r.waitFor("failed", phase(Failed)); s.Kind != Damaged {
		t.Fatalf("damaged: %+v", s)
	}
	r.rel.set("1.1.0", []byte("good release"))
	r.u.Retry()
	r.waitFor("ready", phase(Ready))
}

// Cancel leaves the release alone for automatic rounds; a stalled download
// fails as Stalled.
func TestCancelAndStall(t *testing.T) {
	r := newRig(t, "1.0.0")
	hang := make(chan struct{})
	defer close(hang)
	r.rel.mu.Lock()
	r.rel.hang = hang
	r.rel.mu.Unlock()
	r.u.Check()
	r.waitFor("downloading", phase(Downloading))
	r.u.Cancel()
	r.waitFor("idle", phase(Idle))
	r.u.round()
	if s := r.u.State(); s.Phase != Idle {
		t.Fatalf("an automatic round fetched a cancelled release: %+v", s)
	}
	r.u.Check()
	if s := r.waitFor("stalled", phase(Failed)); s.Kind != Stalled {
		t.Fatalf("stalled: %+v", s)
	}
}

// A command its phase doesn't take does nothing.
func TestCommandsOutsideTheirPhasesDoNothing(t *testing.T) {
	r := newRig(t, "1.0.0")
	r.u.Cancel()
	r.u.Retry()
	r.u.Relaunch()
	time.Sleep(100 * time.Millisecond)
	if s := r.u.State(); s.Phase != Idle {
		t.Fatalf("idle took a command: %+v", s)
	}
	r.in.mu.Lock()
	defer r.in.mu.Unlock()
	if r.in.installed != "" {
		t.Fatal("Relaunch installed with nothing ready")
	}
}

func TestAReleaseAnotherCopyStagedIsReady(t *testing.T) {
	r := newRig(t, "1.0.0")
	r.in.mu.Lock()
	r.in.other, r.in.otherV = filepath.Join(r.in.dir, "other"), "1.1.0"
	r.in.mu.Unlock()
	r.c.SetAutoUpdate(false)
	r.u.round()
	if s := r.u.State(); s.Phase != Ready || s.Version != "1.1.0" || s.AutoUpdate {
		t.Fatalf("another copy's release: %+v", s)
	}
}

func TestSetChannelChecksIt(t *testing.T) {
	r := newRig(t, "1.1.0")
	if err := r.u.SetChannel("dev"); err != nil {
		t.Fatal(err)
	}
	if s := r.waitFor("up to date", phase(UpToDate)); s.Channel != "dev" {
		t.Fatalf("after the switch: %+v", s)
	}
	if err := r.u.SetChannel("bad name!"); err == nil {
		t.Fatal("a channel with a space was taken")
	}
}

// A development build's updater keeps the settings, never runs rounds, and
// says so when asked to check.
func TestADevelopmentBuildNeverUpdates(t *testing.T) {
	c, err := amarit.NewClient(t.TempDir(), "stable", amarit.Project("demo"), amarit.Releases("http://127.0.0.1:1/releases.json"))
	if err != nil {
		t.Fatal(err)
	}
	u := New(Config{Client: c, Installer: &installer{dir: t.TempDir()}, Dir: t.TempDir(), Logf: t.Logf})
	u.Start()
	u.Check()
	if s := u.State(); s.Phase != Failed || s.Kind != Development || s.Kind.Retryable() {
		t.Fatalf("a development build's check: %+v", s)
	}
	if err := u.SetAutoUpdate(false); err != nil || u.State().AutoUpdate {
		t.Fatalf("settings: %v %+v", err, u.State())
	}
}
