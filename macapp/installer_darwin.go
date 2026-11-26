//go:build cgo

package macapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/debkosh/amarit"
	"github.com/debkosh/amarit/relaunch"
)

// Copies are the other running copies of the app sharing the state
// directory, for an app that runs several, one per profile say. Install quits
// them before the swap and starts each again after it.
type Copies interface {
	// QuitOthers tells every other running copy to quit and returns once
	// their processes have ended, with what to start again: this copy's
	// first, then each other one's.
	QuitOthers() []string
	// StartAfter starts executable once this process has ended, once for
	// each of what QuitOthers returned.
	StartAfter(executable string, copies []string)
}

// Installer is the appupdate.Installer of a bundle.
type Installer struct {
	App      string // the installed bundle; Running() for this process's
	StateDir string // the stage and the previous release live here, shared by every copy
	// Requirement is the code requirement a release must meet, such as
	// SignedBy(sha1). Empty checks nothing: an unsigned app updates too.
	Requirement string
	// Copies, when set, are quit before the swap and started after it; else
	// this copy starts again with Args.
	Copies Copies
	Args   []string
	// Quit ends the app once the new release is in place.
	Quit func()
}

func (i *Installer) previous() string {
	return filepath.Join(previousDir(i.StateDir), filepath.Base(i.App))
}

// Usable says whether the bundle can be replaced where it is.
func (i *Installer) Usable() error { return Writable(i.App) }

// Prepare unpacks the download beside it, checks that the bundle is the
// version the manifest promised and meets the requirement, and stages it.
func (i *Installer) Prepare(ctx context.Context, file, version string) (string, error) {
	app, err := Unpack(ctx, file, filepath.Join(filepath.Dir(file), "unpacked"))
	if err != nil {
		return "", err
	}
	b, err := Info(app)
	if err != nil {
		return "", fmt.Errorf("%w: %w", err, amarit.ErrRefused)
	}
	if b.Version != version {
		return "", fmt.Errorf("%w: it says %s, the manifest %s", ErrOtherVersion, b.Version, version)
	}
	if i.Requirement != "" {
		if err := Verify(app, i.Requirement); err != nil {
			return "", err
		}
	}
	return Stage(i.StateDir, app)
}

// Staged is the bundle any copy staged.
func (i *Installer) Staged() (string, string) { return Staged(i.StateDir) }

// Install quits the other copies, swaps the staged bundle in, keeping the
// installed one for a rollback, and ends the app, leaving the new release
// to start once this process has gone.
func (i *Installer) Install(staged string) error {
	exe, err := Info(staged)
	if err != nil {
		return err
	}
	var copies []string
	if i.Copies != nil {
		copies = i.Copies.QuitOthers()
	}
	if err := Swap(staged, i.App, i.previous()); err != nil {
		return err
	}
	// The release is in place: a ready notification would outlive it.
	removeNotification()
	path := filepath.Join(i.App, "Contents", "MacOS", exe.Executable)
	if i.Copies != nil {
		i.Copies.StartAfter(path, copies)
	} else if err := relaunch.After(path, i.Args); err != nil {
		return err
	}
	i.Quit()
	return nil
}

// Settle is the start of a copy: it counts the start, and when the release
// Install put in has failed to start too often, puts the previous one back
// and starts it with this process's arguments. rolledBack says the caller
// should end now. Call the client's Healthy once the app has come up.
func (i *Installer) Settle(c *amarit.Client) (rolledBack bool, err error) {
	back, err := c.Started()
	if err != nil || !back {
		return false, err
	}
	if err := Rollback(i.App, i.previous()); err != nil {
		return false, err
	}
	if err := c.RolledBack(); err != nil {
		return false, err
	}
	exe, err := ExecutablePath(i.App)
	if err != nil {
		return false, err
	}
	return true, relaunch.After(exe, os.Args[1:])
}
