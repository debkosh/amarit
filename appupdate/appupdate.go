// Package appupdate is the updater of an app a person uses: it checks on a
// timer, downloads with progress, checks the release, stages it and installs
// it when told, and it draws nothing. It exposes what an interface needs, a
// State and its changes, and takes the person's commands: Check, Retry,
// Cancel and Relaunch, and the channel and automatic-updates settings. Which
// commands each phase takes is a table in docs/appupdate.md; a command sent
// in a phase that does not take it does nothing.
//
// The install form is an Installer: macapp.Installer for a macOS bundle, a
// function handing the file to the system's installer for an APK.
package appupdate

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sudiptadeb/amarit"
)

// Phase is where the updater is.
type Phase string

const (
	Idle        Phase = ""            // nothing to show
	Checking    Phase = "checking"    // a check the person asked for is running
	UpToDate    Phase = "current"     // that check found nothing newer
	Downloading Phase = "downloading" // Done of Total bytes have arrived
	Verifying   Phase = "verifying"   // the download is being unpacked and checked
	Ready       Phase = "ready"       // Version is staged; Relaunch installs it
	Failed      Phase = "failed"      // Kind says why
)

// Kind is why the updater failed. Whether Retry can help is in the table.
type Kind string

const (
	Offline       Kind = "offline"      // the manifest couldn't be read
	Network       Kind = "network"      // the download failed on the way
	Stalled       Kind = "stalled"      // the download stopped arriving
	Damaged       Kind = "damaged"      // the download didn't match the manifest
	Unchecked     Kind = "unchecked"    // the release's checks couldn't be made
	Refused       Kind = "refused"      // the release's checks refused it; not tried again
	NotWritable   Kind = "not-writable" // the app can't be replaced where it is
	Busy          Kind = "busy"         // another copy's download is still stopping
	InstallFailed Kind = "install"      // putting the release in place failed
)

// Retryable says whether Retry is taken after a failure of this kind.
func (k Kind) Retryable() bool {
	switch k {
	case Offline, Network, Stalled, Damaged, Unchecked, InstallFailed:
		return true
	}
	return false
}

// State is what an interface shows. It is JSON as it is.
type State struct {
	Phase   Phase  `json:"phase"`
	Version string `json:"version,omitempty"` // the release checked, downloading, ready or failed
	Done    int64  `json:"done,omitempty"`    // bytes downloaded, while Downloading
	Total   int64  `json:"total,omitempty"`
	Kind    Kind   `json:"kind,omitempty"`  // why it failed
	Error   string `json:"error,omitempty"` // the error itself, for a log, not a person
	// The settings every copy sharing the state directory follows.
	Channel    string `json:"channel"`
	AutoUpdate bool   `json:"autoUpdate"`
}

// Installer is how a release becomes the installed app.
type Installer interface {
	// Usable says whether the installed app can be replaced at all; an error
	// wrapping amarit.ErrNotWritable shows as NotWritable.
	Usable() error
	// Prepare turns a verified download into a staged release and returns
	// it. A refusal wraps amarit.ErrRefused, and the release is rejected; any
	// other error is a check that couldn't be made.
	Prepare(ctx context.Context, file, version string) (staged string, err error)
	// Staged is a release another copy sharing the state staged, or "".
	Staged() (staged, version string)
	// Install puts staged in place and restarts into it, or hands it to the
	// system's installer. It returns once that has started.
	Install(staged string) error
}

// Config is what the updater needs.
type Config struct {
	Client    *amarit.Client
	Installer Installer
	// Dir is where downloads go, in Dir/download; the client's state
	// directory does.
	Dir string
	// Interval is the time between rounds, an hour unless set; the first
	// round runs FirstCheck after Start, two seconds unless set.
	Interval   time.Duration
	FirstCheck time.Duration
	// StallAfter gives up on a download receiving nothing for that long, 30
	// seconds unless set. LockWait is how long a check the person asks for
	// waits for another copy's, 15 seconds unless set.
	StallAfter time.Duration
	LockWait   time.Duration
	// Logf logs what the updater does; log.Printf unless set.
	Logf func(format string, args ...any)
}

var (
	errCancelled = errors.New("cancelled")
	errRetried   = errors.New("retried")
)

// Updater keeps an app current. Its methods may be called from any
// goroutine.
type Updater struct {
	cfg Config

	mu            sync.Mutex
	state         State
	staged        string // the release Relaunch installs, and its version
	stagedVersion string
	fetching      *attempt // the download running now
	declined      string   // a release the person cancelled: automatic checks leave it alone
	busy          bool     // an Install is running

	timer   chan struct{} // closed to end the round loop; nil when stopped
	wake    chan struct{}
	events  *queue
	started bool
}

type attempt struct {
	version string
	stop    context.CancelCauseFunc
}

// New returns an updater; Start begins its rounds.
func New(cfg Config) *Updater {
	if cfg.Interval == 0 {
		cfg.Interval = time.Hour
	}
	if cfg.FirstCheck == 0 {
		cfg.FirstCheck = 2 * time.Second
	}
	if cfg.StallAfter == 0 {
		cfg.StallAfter = 30 * time.Second
	}
	if cfg.LockWait == 0 {
		cfg.LockWait = 15 * time.Second
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	u := &Updater{cfg: cfg, wake: make(chan struct{}, 1), events: newQueue()}
	u.state.Channel, u.state.AutoUpdate = cfg.Client.Channel(), cfg.Client.AutoUpdate()
	return u
}

// State is the state now.
func (u *Updater) State() State {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.state
}

// OnChange subscribes to the state changing. Listeners are called in order,
// one change at a time, on a goroutine of the updater's: an interface posts
// the state to its own thread.
func (u *Updater) OnChange(f func(State)) { u.events.subscribe(f) }

// Start begins a round every Interval: each offers a release another copy
// staged and, with automatic updates on, checks.
func (u *Updater) Start() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.timer != nil {
		return
	}
	u.timer = make(chan struct{})
	go u.loop(u.timer)
}

// Stop ends the rounds. A download running goes on.
func (u *Updater) Stop() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.timer != nil {
		close(u.timer)
		u.timer = nil
	}
}

func (u *Updater) loop(stop chan struct{}) {
	wait := u.cfg.FirstCheck
	for {
		select {
		case <-stop:
			return
		case <-time.After(wait):
		case <-u.wake:
		}
		u.round()
		wait = u.cfg.Interval
	}
}

// round is one turn of the timer: a release another copy staged, then a
// check when automatic updates are on and no other copy is checking.
func (u *Updater) round() {
	u.readSettings()
	u.readStaged()
	if !u.cfg.Client.AutoUpdate() {
		return
	}
	unlock, ok, err := u.cfg.Client.TryLock()
	if err != nil {
		u.cfg.Logf("appupdate: %v", err)
		return
	}
	if !ok {
		return
	}
	defer unlock()
	u.check(false)
}

// Check checks now, whether or not automatic updates are on. A release a
// person asks for is tried even if it was refused or cancelled before.
func (u *Updater) Check() {
	if !u.takes(Idle, UpToDate, Ready, Failed) {
		return
	}
	u.asked()
}

// Retry stops the download running, or follows a failure Retry can help,
// and checks and downloads again at once.
func (u *Updater) Retry() {
	s := u.State()
	if s.Phase != Downloading && !(s.Phase == Failed && s.Kind.Retryable()) {
		return
	}
	u.stopFetch(errRetried)
	u.cfg.Logf("appupdate: retrying")
	u.asked()
}

// Cancel stops the download, or the checks after it, and shows nothing.
// Automatic checks leave that release alone until a person asks.
func (u *Updater) Cancel() {
	s := u.State()
	if s.Phase != Downloading && s.Phase != Verifying {
		return
	}
	u.mu.Lock()
	u.declined = s.Version
	u.mu.Unlock()
	u.stopFetch(errCancelled)
	u.cfg.Logf("appupdate: %s: cancelled", s.Version)
	u.set(func(st *State) { *st = State{} })
}

// Relaunch installs the release that is ready. For a bundle the app ends
// and the new release starts; for an APK the system's installer opens, and
// the release stays ready, so a person who cancels it can ask again.
func (u *Updater) Relaunch() {
	u.mu.Lock()
	s, staged := u.state, u.staged
	if s.Phase != Ready || staged == "" || u.busy {
		u.mu.Unlock()
		return
	}
	u.busy = true
	u.mu.Unlock()
	go func() {
		defer func() {
			u.mu.Lock()
			u.busy = false
			u.mu.Unlock()
		}()
		if err := u.cfg.Client.Installing(s.Version); err != nil {
			u.cfg.Logf("appupdate: %v", err)
		}
		u.cfg.Logf("appupdate: installing %s", s.Version)
		if err := u.cfg.Installer.Install(staged); err != nil {
			u.cfg.Logf("appupdate: install %s: %v", s.Version, err)
			u.mu.Lock()
			u.staged, u.stagedVersion = "", ""
			u.mu.Unlock()
			u.fail(s.Version, InstallFailed, err)
		}
	}()
}

// SetChannel picks the channel for every copy sharing the state, and checks
// it at once. A download of the other channel's release stops.
func (u *Updater) SetChannel(name string) error {
	if name == u.cfg.Client.Channel() {
		return nil
	}
	if err := u.cfg.Client.SetChannel(name); err != nil {
		return err
	}
	u.cfg.Logf("appupdate: following %s", name)
	u.stopFetch(errRetried)
	u.set(func(st *State) { *st = State{} })
	u.asked()
	return nil
}

// SetAutoUpdate turns automatic checks on or off for every copy sharing the
// state. Turned on, a round runs soon.
func (u *Updater) SetAutoUpdate(on bool) error {
	if err := u.cfg.Client.SetAutoUpdate(on); err != nil {
		return err
	}
	u.set(func(st *State) {})
	if on {
		select {
		case u.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// takes says whether the phase now is one of phases.
func (u *Updater) takes(phases ...Phase) bool {
	p := u.State().Phase
	for _, q := range phases {
		if p == q {
			return true
		}
	}
	return false
}

// asked runs a check the person asked for: it waits for another copy's,
// tries a refused or cancelled release again, and says how it went.
func (u *Updater) asked() {
	u.set(func(st *State) { st.Phase, st.Kind, st.Error = Checking, "", "" })
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), u.cfg.LockWait)
		unlock, err := u.cfg.Client.Lock(ctx)
		cancel()
		if err != nil {
			u.fail("", Busy, err)
			return
		}
		defer unlock()
		if err := u.cfg.Client.Unreject(); err != nil {
			u.cfg.Logf("appupdate: %v", err)
		}
		u.mu.Lock()
		u.declined = ""
		u.mu.Unlock()
		u.readStaged()
		u.check(true)
	}()
}

// readSettings brings the settings another copy changed into the state.
func (u *Updater) readSettings() {
	ch, auto := u.cfg.Client.Channel(), u.cfg.Client.AutoUpdate()
	u.set(func(st *State) { st.Channel, st.AutoUpdate = ch, auto })
}

// readStaged offers a release another copy staged, once it is newer than
// this one.
func (u *Updater) readStaged() {
	staged, v := u.cfg.Installer.Staged()
	if staged == "" || !u.newer(v) {
		return
	}
	u.mu.Lock()
	u.staged, u.stagedVersion = staged, v
	u.mu.Unlock()
	u.set(func(st *State) {
		switch st.Phase {
		case Downloading, Verifying, Checking:
		default:
			*st = State{Phase: Ready, Version: v, Channel: st.Channel, AutoUpdate: st.AutoUpdate}
		}
	})
}

func (u *Updater) newer(v string) bool {
	c, err := amarit.CompareVersions(v, u.cfg.Client.Version())
	return err == nil && c > 0
}

// check is one look at the manifest, and the download and checks when it
// names a newer release. The caller holds the lock. An automatic check only
// logs a manifest it couldn't read, and leaves a cancelled release alone.
func (u *Updater) check(asked bool) {
	if err := u.cfg.Installer.Usable(); err != nil {
		u.fail("", kindOf(err, Unchecked), err)
		return
	}
	d, err := u.cfg.Client.Check(context.Background())
	if err != nil {
		u.cfg.Logf("appupdate: check: %v", err)
		if asked {
			u.fail("", Offline, err)
		}
		return
	}
	u.cfg.Logf("appupdate: %s", d.Reason)
	u.mu.Lock()
	staged, stagedVersion, declined := u.staged, u.stagedVersion, u.declined
	u.mu.Unlock()
	ready := func(st *State) {
		*st = State{Phase: Ready, Version: stagedVersion, Channel: st.Channel, AutoUpdate: st.AutoUpdate}
	}
	switch {
	case staged != "" && (!d.Update || stagedVersion == d.Version):
		// What is staged is what the channel names, or newer than it.
		u.set(func(st *State) {
			if st.Phase == Checking || st.Phase == Idle {
				ready(st)
			}
		})
	case !d.Update:
		u.set(func(st *State) {
			switch {
			case st.Phase == Checking:
				*st = State{Phase: UpToDate, Channel: st.Channel, AutoUpdate: st.AutoUpdate}
			case st.Phase == Failed && st.Version != d.Version:
				// The failure was a release the channel no longer names.
				*st = State{Channel: st.Channel, AutoUpdate: st.AutoUpdate}
			}
		})
	case !asked && declined == d.Version:
		u.cfg.Logf("appupdate: %s was cancelled; waiting for a check a person asks for", d.Version)
	default:
		u.fetch(d)
	}
}

// fetch downloads the release, has the installer check and stage it, and
// shows it ready. A refusal rejects the release, so it is downloaded once,
// not every round.
func (u *Updater) fetch(d *amarit.Decision) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	a := &attempt{version: d.Version, stop: cancel}
	u.mu.Lock()
	u.fetching = a
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		if u.fetching == a {
			u.fetching = nil
		}
		u.mu.Unlock()
	}()
	u.mu.Lock()
	// A new download replaces what is staged.
	u.staged, u.stagedVersion = "", ""
	u.mu.Unlock()
	u.set(func(st *State) {
		*st = State{Phase: Downloading, Version: d.Version, Total: d.Asset.Size, Channel: st.Channel, AutoUpdate: st.AutoUpdate}
	})
	dir := filepath.Join(u.cfg.Dir, "download")
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		u.fail(d.Version, Network, err)
		return
	}
	defer os.RemoveAll(dir)
	file, err := u.cfg.Client.Download(ctx, d, dir,
		amarit.StallAfter(u.cfg.StallAfter),
		amarit.OnProgress(func(done, total int64) {
			u.set(func(st *State) {
				if st.Phase == Downloading && st.Version == d.Version {
					st.Done, st.Total = done, total
				}
			})
		}))
	if stopped(ctx) {
		return
	}
	if err != nil {
		u.cfg.Logf("appupdate: %s: %v", d.Version, err)
		u.fail(d.Version, kindOf(err, Network), err)
		return
	}
	u.set(func(st *State) { st.Phase, st.Done, st.Total = Verifying, 0, 0 })
	staged, err := u.cfg.Installer.Prepare(ctx, file, d.Version)
	if stopped(ctx) {
		return
	}
	if err != nil {
		u.cfg.Logf("appupdate: %s: %v", d.Version, err)
		if errors.Is(err, amarit.ErrRefused) {
			if rerr := u.cfg.Client.Reject(d.Version); rerr != nil {
				u.cfg.Logf("appupdate: %v", rerr)
			}
		}
		u.fail(d.Version, kindOf(err, Unchecked), err)
		return
	}
	u.mu.Lock()
	u.staged, u.stagedVersion = staged, d.Version
	u.mu.Unlock()
	u.cfg.Logf("appupdate: %s is staged at %s", d.Version, staged)
	u.set(func(st *State) {
		*st = State{Phase: Ready, Version: d.Version, Channel: st.Channel, AutoUpdate: st.AutoUpdate}
	})
}

// stopped says the person's Cancel or Retry stopped the attempt; those set
// the state themselves.
func stopped(ctx context.Context) bool {
	why := context.Cause(ctx)
	return errors.Is(why, errCancelled) || errors.Is(why, errRetried)
}

func (u *Updater) stopFetch(why error) {
	u.mu.Lock()
	a := u.fetching
	u.mu.Unlock()
	if a != nil {
		a.stop(why)
	}
}

// kindOf is the kind of a failure, from amarit's errors, else fallback.
func kindOf(err error, fallback Kind) Kind {
	switch {
	case errors.Is(err, amarit.ErrRefused):
		return Refused
	case errors.Is(err, amarit.ErrDamaged):
		return Damaged
	case errors.Is(err, amarit.ErrStalled):
		return Stalled
	case errors.Is(err, amarit.ErrNotWritable):
		return NotWritable
	}
	return fallback
}

func (u *Updater) fail(version string, k Kind, err error) {
	u.set(func(st *State) {
		*st = State{Phase: Failed, Version: version, Kind: k, Error: err.Error(), Channel: st.Channel, AutoUpdate: st.AutoUpdate}
	})
}

// set changes the state and tells the listeners when it changed.
func (u *Updater) set(f func(*State)) {
	u.mu.Lock()
	was := u.state
	f(&u.state)
	u.state.Channel, u.state.AutoUpdate = u.cfg.Client.Channel(), u.cfg.Client.AutoUpdate()
	now := u.state
	u.mu.Unlock()
	if now != was {
		u.events.push(now)
	}
}
