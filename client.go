package amarit

import (
	"context"
	"errors"
	"net/http"
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

// NewClient resolves the configuration the way Start does, from the stamp
// and the options, with no command line, and follows channel. stateDir is
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
// took out.
func (c *Client) Check(ctx context.Context) (*Decision, error) { return c.u.Check(ctx) }

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
