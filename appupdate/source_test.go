package appupdate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sudiptadeb/amarit"
)

// source stands in for a Flatpak's update portal.
type source struct {
	mu        sync.Mutex
	found     Found
	fetchErr  error
	hold      chan struct{} // Fetch waits on it, when set
	installed bool
	changed   chan struct{}
}

func (s *source) Look(ctx context.Context) (Found, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.found, nil
}

func (s *source) Fetch(ctx context.Context, f Found, progress func(done, total int64)) error {
	s.mu.Lock()
	hold, err := s.hold, s.fetchErr
	s.mu.Unlock()
	progress(50, 100)
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	progress(100, 100)
	s.mu.Lock()
	s.found = Found{ID: f.ID, Ready: true}
	s.mu.Unlock()
	return nil
}

func (s *source) Install() error {
	s.mu.Lock()
	s.installed = true
	s.mu.Unlock()
	return nil
}

func (s *source) Changed() <-chan struct{} { return s.changed }

func sourceRig(t *testing.T, src *source) *Updater {
	t.Helper()
	c, err := amarit.NewClient(t.TempDir(), "stable", amarit.ProjectFile([]byte(`{"project":"demo","releases":"http://127.0.0.1:1/r.json","auto_update":true}`)))
	if err != nil {
		t.Fatal(err)
	}
	u := New(Config{Client: c, Source: src, Dir: t.TempDir(), Interval: time.Hour, FirstCheck: time.Hour, Logf: t.Logf})
	t.Cleanup(u.Close)
	return u
}

func waitState(t *testing.T, u *Updater, what string, cond func(State) bool) State {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := u.State(); cond(s) {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; state %+v", what, u.State())
	return State{}
}

// The source's news starts a round: it updates, with progress, and Relaunch
// hands over to the source.
func TestSourceUpdatesWhenItHearsOfARelease(t *testing.T) {
	src := &source{changed: make(chan struct{}, 1)}
	u := sourceRig(t, src)
	u.Start()
	src.mu.Lock()
	src.found = Found{ID: "c2"}
	src.mu.Unlock()
	src.changed <- struct{}{}
	waitState(t, u, "ready", phase(Ready))
	u.Relaunch()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		src.mu.Lock()
		done := src.installed
		src.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Relaunch did not reach the source")
}

// An update made outside the app is ready at once; a check that finds
// nothing is current.
func TestSourceReadyAndCurrent(t *testing.T) {
	src := &source{}
	u := sourceRig(t, src)
	u.Check()
	waitState(t, u, "current", phase(UpToDate))
	src.mu.Lock()
	src.found = Found{ID: "c2", Ready: true}
	src.mu.Unlock()
	u.Check()
	waitState(t, u, "ready", phase(Ready))
}

func TestSourceNeedsUserAndNetwork(t *testing.T) {
	src := &source{found: Found{ID: "c2"}, fetchErr: fmt.Errorf("new permissions: %w", ErrNeedsUser)}
	u := sourceRig(t, src)
	u.Check()
	if s := waitState(t, u, "failed", phase(Failed)); s.Kind != NeedsUser || s.Kind.Retryable() {
		t.Fatalf("needs user: %+v", s)
	}
	src.mu.Lock()
	src.fetchErr = errors.New("connection reset")
	src.mu.Unlock()
	u.Check()
	if s := waitState(t, u, "failed", func(s State) bool { return s.Phase == Failed && s.Kind == Network }); !s.Kind.Retryable() {
		t.Fatalf("network: %+v", s)
	}
}

// Cancel during the update declines it for automatic rounds; SetChannel
// isn't something a source does.
func TestSourceCancelAndChannel(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	src := &source{found: Found{ID: "c2"}, hold: hold}
	u := sourceRig(t, src)
	u.Check()
	if s := waitState(t, u, "downloading", func(s State) bool { return s.Phase == Downloading && s.Done == 50 }); s.Total != 100 {
		t.Fatalf("progress: %+v", s)
	}
	u.Cancel()
	waitState(t, u, "idle", phase(Idle))
	u.round()
	if s := u.State(); s.Phase != Idle {
		t.Fatalf("an automatic round fetched a cancelled update: %+v", s)
	}
	if err := u.SetChannel("dev"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("SetChannel: %v", err)
	}
}
