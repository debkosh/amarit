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
// and the options, with no command line. It follows channel until a
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
	if cfg.Version == "" {
		return nil, errors.New("amarit: no version")
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
// download leaves nothing behind.
func (c *Client) Download(ctx context.Context, d *Decision, dir string) (string, error) {
	if !d.Update {
		return "", errors.New("nothing to download")
	}
	return c.u.download(ctx, d.Asset, dir, c.u.cfg.Project+"-"+d.Version+"-")
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
