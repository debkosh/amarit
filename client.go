package amarit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Client is the updater for a program that installs its own updates: one
// whose installed form is not a single binary, such as a macOS application
// bundle of several binaries, a library and resources. It checks the manifest
// and downloads and verifies the asset exactly as the in-place updater does,
// and stops there; the program swaps the installed form and restarts itself.
type Client struct {
	u *Updater
}

// settings is <stateDir>/settings.json: what a person chose in the program,
// shared by every copy that uses the same state directory. Absent fields
// leave the build's defaults.
type settings struct {
	AutoUpdate *bool  `json:"auto_update,omitempty"`
	Channel    string `json:"channel,omitempty"`
}

// NewClient resolves the configuration the way Start does, from the stamp
// and the options, with no command line. A build with no version is a
// development build: it keeps the settings, and Check and Download say
// ErrDevelopment. It follows channel until a
// SetChannel in any copy sharing stateDir picks another. stateDir is
// where the install id and the state kept between runs live; a bundle has
// no "beside the binary" that survives a code signature, so the program
// names a directory of its own.
func NewClient(stateDir, channel string, options ...Option) (*Client, error) {
	cfg, err := resolve(options, Options{})
	if err != nil {
		return nil, err
	}
	if cfg.Releases == "" {
		return nil, errors.New("amarit: no releases URL")
	}
	if stateDir == "" {
		return nil, errors.New("amarit: no state directory")
	}
	if channel == "" {
		channel = "stable"
	}
	return &Client{u: &Updater{
		cfg:    cfg,
		opts:   Options{Channel: channel},
		client: &http.Client{Timeout: 30 * time.Minute},
		target: runtime.GOOS + "/" + runtime.GOARCH,
		state:  stateDir,
	}}, nil
}

// Version is the running version the client compares against.
func (c *Client) Version() string { return c.u.cfg.Version }

// Check fetches the manifest and decides whether the program should update:
// the same rules as the in-place updater, including the version a Reject
// took out. It reads the channel each time, so a SetChannel in another copy
// holds from that copy's next check.
func (c *Client) Check(ctx context.Context) (*Decision, error) {
	if c.u.cfg.Version == "" {
		return nil, ErrDevelopment
	}
	u := *c.u
	u.opts.Channel = c.Channel()
	return u.Check(ctx)
}

// Channel is the channel the program follows: the one a person last picked
// with SetChannel, in this copy or another sharing the state directory, else
// the one NewClient was given.
func (c *Client) Channel() string {
	if s := c.readSettings(); s.Channel != "" {
		return s.Channel
	}
	return c.u.opts.Channel
}

// SetChannel picks the channel for every copy sharing the state directory.
// The updater never goes back a version, so a copy on a rolling build that
// picks stable stays on it until a stable release passes it.
func (c *Client) SetChannel(name string) error {
	if !exactVersionRe.MatchString(name) {
		return fmt.Errorf("amarit: channel %q has invalid characters", name)
	}
	s := c.readSettings()
	s.Channel = name
	return c.writeSettings(s)
}

// AutoUpdate says whether the program checks on its own: the person's
// choice from SetAutoUpdate, else the project's auto_update. The program
// runs its checks; amarit keeps the choice.
func (c *Client) AutoUpdate() bool {
	if s := c.readSettings(); s.AutoUpdate != nil {
		return *s.AutoUpdate
	}
	return c.u.cfg.AutoUpdate
}

// SetAutoUpdate records the person's choice for every copy sharing the
// state directory.
func (c *Client) SetAutoUpdate(on bool) error {
	s := c.readSettings()
	s.AutoUpdate = &on
	return c.writeSettings(s)
}

func (c *Client) settingsPath() string { return filepath.Join(c.u.state, "settings.json") }

func (c *Client) readSettings() settings {
	var s settings
	if data, err := os.ReadFile(c.settingsPath()); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	return s
}

// writeSettings renames a new file over the old, so no copy reads half of one.
func (c *Client) writeSettings(s settings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.u.state, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(c.u.state, "settings-*.json")
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), c.settingsPath())
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// Download fetches the decided asset into dir, checking its size and sha256
// against the manifest as it streams, and returns the file's path. A failed
// download leaves nothing behind. A mismatch is ErrDamaged, a download
// StallAfter gave up on is ErrStalled, and one whose context was cancelled
// with a cause returns that cause.
func (c *Client) Download(ctx context.Context, d *Decision, dir string, opts ...DownloadOption) (string, error) {
	if c.u.cfg.Version == "" {
		return "", ErrDevelopment
	}
	if !d.Update {
		return "", errors.New("nothing to download")
	}
	return c.u.download(ctx, d.Asset, dir, c.u.cfg.Project+"-"+d.Version+"-", newWatch(opts))
}

// TryLock takes the state directory's lock, which one copy at a time holds
// while it checks and downloads, when no other copy holds it. The lock ends
// with the process, so a copy that dies cannot leave it behind.
func (c *Client) TryLock() (unlock func(), ok bool, err error) {
	if err := os.MkdirAll(c.u.state, 0o755); err != nil {
		return nil, false, err
	}
	return tryLockFile(filepath.Join(c.u.state, "lock"))
}

// Lock takes the state directory's lock, waiting for a copy holding it until
// ctx ends.
func (c *Client) Lock(ctx context.Context) (unlock func(), err error) {
	for {
		unlock, ok, err := c.TryLock()
		if err != nil || ok {
			return unlock, err
		}
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Installing records that version is about to replace the running one, so
// the starts of the new one can be counted: the rollback the in-place
// updater gives a binary, for a program that installs itself.
func (c *Client) Installing(version string) error {
	return c.withState(func(st *state) {
		st.Attempt, st.From = strings.TrimPrefix(version, "v"), strings.TrimPrefix(c.u.cfg.Version, "v")
		st.Starts, st.Pending = 0, nil
	})
}

// Started records that this process has started, and says whether the
// program should roll back to the version before: after the release
// Installing named has failed to start maxStartsBeforeRollback times. A
// start fails when its process ends before any copy calls Healthy, so
// several copies starting at once are not failures. Call it once, early.
func (c *Client) Started() (rollBack bool, err error) {
	running := strings.TrimPrefix(c.u.cfg.Version, "v")
	err = c.withState(func(st *state) {
		switch {
		case st.Attempt == "":
		case st.Attempt != running:
			// The release never took over, or was rolled back already.
			st.Attempt, st.From, st.Starts, st.Pending = "", "", 0, nil
		default:
			var alive []int
			for _, pid := range st.Pending {
				if processAlive(pid) {
					alive = append(alive, pid)
				} else {
					st.Starts++
				}
			}
			st.Pending = append(alive, os.Getpid())
			rollBack = st.Starts >= maxStartsBeforeRollback
		}
	})
	return rollBack, err
}

// Healthy records that the running version works, once the program has come
// up far enough to say so: its starts are no longer counted.
func (c *Client) Healthy() error {
	running := strings.TrimPrefix(c.u.cfg.Version, "v")
	return c.withState(func(st *state) {
		if st.Attempt == running {
			*st = state{Healthy: running, Rejected: st.Rejected}
		}
	})
}

// RolledBack records that the program put the version before back after
// Started said to: the release is rejected, as Reject does, and not offered
// again until a newer one.
func (c *Client) RolledBack() error {
	return c.withState(func(st *state) {
		*st = state{Healthy: st.From, Rejected: st.Attempt}
	})
}

// withState changes state.json under its own lock, so copies starting
// together do not lose each other's writes. It is not the check lock, which
// a copy holds for a whole download.
func (c *Client) withState(f func(*state)) error {
	if err := os.MkdirAll(c.u.state, 0o755); err != nil {
		return err
	}
	unlock, err := lockFile(filepath.Join(c.u.state, "state.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	st := c.u.loadState()
	f(&st)
	return c.u.saveState(st)
}

// Reject records that version as refused on this install, so Check does not
// offer it again; a newer release is offered. It is what the in-place
// updater records after a rollback, for a program that found the downloaded
// asset unfit by its own checks.
func (c *Client) Reject(version string) error {
	st := c.u.loadState()
	st.Rejected = version
	return c.u.saveState(st)
}

// Unreject forgets the version a Reject took out, so the next Check offers it
// again. It is for a check a person asked for: an automatic check keeps
// holding a refused release, but someone who presses "check" means "try it".
func (c *Client) Unreject() error {
	st := c.u.loadState()
	if st.Rejected == "" {
		return nil
	}
	st.Rejected = ""
	return c.u.saveState(st)
}
