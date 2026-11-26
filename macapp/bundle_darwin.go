//go:build cgo

package macapp

/*
#include <stdio.h>
#include <errno.h>
#include <stdlib.h>

static int macapp_swap(const char *a, const char *b) {
	return renamex_np(a, b, RENAME_SWAP) == 0 ? 0 : errno;
}
*/
import "C"

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/debkosh/amarit"
)

// ErrOtherVersion is a bundle that says it is another version than the
// manifest promised.
var ErrOtherVersion = fmt.Errorf("the bundle is another version: %w", amarit.ErrRefused)

// Bundle is what a bundle says about itself in Contents/Info.plist.
type Bundle struct {
	Version    string // CFBundleShortVersionString
	Executable string // CFBundleExecutable, the name in Contents/MacOS
}

// BundleOf returns the .app the executable runs from, or "" when it runs
// from elsewhere, such as a build directory.
func BundleOf(executable string) string {
	for d := filepath.Dir(executable); d != "/" && d != "."; d = filepath.Dir(d) {
		if strings.HasSuffix(d, ".app") {
			return d
		}
	}
	return ""
}

// Running returns the bundle this process runs from, symlinks resolved, or
// "" when it runs from elsewhere.
func Running() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return BundleOf(exe)
}

// Info reads the bundle's version and executable from its Info.plist, an
// XML plist.
func Info(app string) (Bundle, error) {
	f, err := os.Open(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return Bundle{}, err
	}
	defer f.Close()
	values := map[string]string{}
	dec := xml.NewDecoder(f)
	key := ""
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Bundle{}, fmt.Errorf("Info.plist: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		var text string
		switch se.Name.Local {
		case "key":
			dec.DecodeElement(&text, &se)
			key = text
		case "string":
			dec.DecodeElement(&text, &se)
			if key != "" {
				values[key] = text
			}
			key = ""
		default:
			key = ""
		}
	}
	b := Bundle{Version: values["CFBundleShortVersionString"], Executable: values["CFBundleExecutable"]}
	if b.Version == "" || b.Executable == "" {
		return Bundle{}, errors.New("Info.plist has no version or executable")
	}
	return b, nil
}

// ExecutablePath is the bundle's main executable.
func ExecutablePath(app string) (string, error) {
	b, err := Info(app)
	if err != nil {
		return "", err
	}
	return filepath.Join(app, "Contents", "MacOS", b.Executable), nil
}

// Unpack extracts a zip archive into dir with ditto, which keeps what the
// signature covers, and returns the one bundle inside.
func Unpack(ctx context.Context, archive, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	keepFileLimit()
	if out, err := exec.CommandContext(ctx, "/usr/bin/ditto", "-x", "-k", archive, dir).CombinedOutput(); err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return "", cause
		}
		return "", fmt.Errorf("unpack: %s", firstLine(out, err))
	}
	apps, _ := filepath.Glob(filepath.Join(dir, "*.app"))
	if len(apps) != 1 {
		return "", fmt.Errorf("the archive holds %d apps, not one: %w", len(apps), amarit.ErrRefused)
	}
	return apps[0], nil
}

// keepFileLimit gives the programs this one starts its own limit on open
// files. Go raises its own at start but starts children with the limit it
// found, 256 under launchd, and a child also inherits the files the app holds
// open, which can be most of that: ditto then failed with "Too many open
// files". Setting the limit, even to what it is, makes Go pass it on.
var keepFileLimit = sync.OnceFunc(func() {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err == nil {
		syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lim)
	}
})

func firstLine(out []byte, err error) string {
	if line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n"); line != "" {
		return line
	}
	return err.Error()
}

// Writable says whether the bundle can be replaced where it is installed: a
// copy still on its disk image, or in a folder this user cannot write, can't.
func Writable(app string) error {
	dir := filepath.Dir(app)
	if err := syscall.Access(dir, 2); err != nil {
		return fmt.Errorf("%s: %w", dir, amarit.ErrNotWritable)
	}
	return nil
}

func stageDir(stateDir string) string    { return filepath.Join(stateDir, "stage") }
func previousDir(stateDir string) string { return filepath.Join(stateDir, "previous") }

// Stage moves a checked bundle into stateDir's stage, replacing what was
// there, and returns where it is now. Move only a bundle that has passed
// every check: every copy sharing stateDir offers what it finds there.
func Stage(stateDir, app string) (string, error) {
	dir := stageDir(stateDir)
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	staged := filepath.Join(dir, filepath.Base(app))
	return staged, os.Rename(app, staged)
}

// Staged returns the bundle in stateDir's stage and its version, or "" when
// there is none.
func Staged(stateDir string) (app, version string) {
	apps, _ := filepath.Glob(filepath.Join(stageDir(stateDir), "*.app"))
	if len(apps) != 1 {
		return "", ""
	}
	b, err := Info(apps[0])
	if err != nil {
		return "", ""
	}
	return apps[0], b.Version
}

// Swap puts staged where installed is in one atomic exchange, so the app is
// never missing, and keeps the one that was installed at previous for
// Rollback.
func Swap(staged, installed, previous string) error {
	if err := exchange(staged, installed); err != nil {
		return fmt.Errorf("couldn't put the new app in place: %w", err)
	}
	os.RemoveAll(previous)
	if err := os.MkdirAll(filepath.Dir(previous), 0o700); err != nil {
		return err
	}
	// staged now holds the release that was installed.
	return os.Rename(staged, previous)
}

// Rollback puts previous back where installed is, in one exchange, and
// removes the release that failed.
func Rollback(installed, previous string) error {
	if _, err := os.Stat(previous); err != nil {
		return fmt.Errorf("no previous release: %w", err)
	}
	if err := exchange(previous, installed); err != nil {
		return fmt.Errorf("couldn't put the previous app back: %w", err)
	}
	return os.RemoveAll(previous)
}

func exchange(a, b string) error {
	ca, cb := C.CString(a), C.CString(b)
	defer C.free(unsafe.Pointer(ca))
	defer C.free(unsafe.Pointer(cb))
	if errno := C.macapp_swap(ca, cb); errno != 0 {
		return syscall.Errno(errno)
	}
	return nil
}
