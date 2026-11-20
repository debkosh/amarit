package appupdate

import (
	"context"
	"os"
	"path/filepath"
)

// File installs a release that is one file the system installs, such as an
// APK: the download is staged as it is, and Install hands it to Open, which
// opens the system's installer. One copy uses the state, so nothing another
// copy staged is offered.
type File struct {
	Dir  string             // the stage is Dir/stage
	Open func(string) error // hands the staged file to the system's installer
}

func (f File) Usable() error { return nil }

func (f File) Prepare(ctx context.Context, file, version string) (string, error) {
	dir := filepath.Join(f.Dir, "stage")
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	staged := filepath.Join(dir, filepath.Base(file))
	return staged, os.Rename(file, staged)
}

func (f File) Staged() (string, string) { return "", "" }

func (f File) Install(staged string) error { return f.Open(staged) }
