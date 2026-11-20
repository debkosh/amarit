//go:build cgo

package macapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sudiptadeb/amarit"
)

// fakeApp writes a bundle that says it is version, with a real executable
// so codesign takes it.
func fakeApp(t *testing.T, dir, name, version string) string {
	t.Helper()
	app := filepath.Join(dir, name+".app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>demo</string>
<key>CFBundleIdentifier</key><string>com.example.demo</string>
<key>CFBundleShortVersionString</key><string>%s</string>
<key>LSUIElement</key><true/>
</dict></plist>
`, version)
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	exe, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "demo"), exe, 0o755); err != nil {
		t.Fatal(err)
	}
	return app
}

func zipApp(t *testing.T, app string) string {
	t.Helper()
	zip := filepath.Join(t.TempDir(), "demo.zip")
	if out, err := exec.Command("/usr/bin/ditto", "-c", "-k", "--keepParent", app, zip).CombinedOutput(); err != nil {
		t.Fatalf("zip: %v %s", err, out)
	}
	return zip
}

func sign(t *testing.T, app string) {
	t.Helper()
	if out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", app).CombinedOutput(); err != nil {
		t.Fatalf("sign: %v %s", err, out)
	}
}

func TestInfoAndBundleOf(t *testing.T) {
	app := fakeApp(t, t.TempDir(), "Demo", "1.2.3")
	b, err := Info(app)
	if err != nil || b != (Bundle{Version: "1.2.3", Executable: "demo"}) {
		t.Fatalf("Info: %+v %v", b, err)
	}
	if got := BundleOf(filepath.Join(app, "Contents", "MacOS", "demo")); got != app {
		t.Fatalf("BundleOf: %s", got)
	}
	if got := BundleOf("/usr/local/bin/demo"); got != "" {
		t.Fatalf("BundleOf outside a bundle: %s", got)
	}
}

func TestUnpackStageAndStaged(t *testing.T) {
	zip := zipApp(t, fakeApp(t, t.TempDir(), "Demo", "1.2.3"))
	app, err := Unpack(context.Background(), zip, filepath.Join(t.TempDir(), "unpacked"))
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	if got, v := Staged(state); got != "" || v != "" {
		t.Fatalf("an empty stage offers %s %s", got, v)
	}
	staged, err := Stage(state, app)
	if err != nil {
		t.Fatal(err)
	}
	if got, v := Staged(state); got != staged || v != "1.2.3" {
		t.Fatalf("Staged: %s %s", got, v)
	}
	empty := zipApp(t, t.TempDir())
	if _, err := Unpack(context.Background(), empty, t.TempDir()); !errors.Is(err, amarit.ErrRefused) {
		t.Fatalf("an archive without an app: %v", err)
	}
}

// The new bundle takes the installed one's place in one exchange, the old
// one is kept, and Rollback puts it back.
func TestSwapAndRollback(t *testing.T) {
	dir := t.TempDir()
	installed := fakeApp(t, filepath.Join(dir, "Applications"), "Demo", "1.0.0")
	staged := fakeApp(t, filepath.Join(dir, "stage"), "Demo", "1.1.0")
	previous := filepath.Join(dir, "previous", "Demo.app")
	if err := Swap(staged, installed, previous); err != nil {
		t.Fatal(err)
	}
	if b, _ := Info(installed); b.Version != "1.1.0" {
		t.Fatalf("installed after the swap: %s", b.Version)
	}
	if b, _ := Info(previous); b.Version != "1.0.0" {
		t.Fatalf("previous after the swap: %s", b.Version)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("the stage still holds a bundle: %v", err)
	}
	if err := Rollback(installed, previous); err != nil {
		t.Fatal(err)
	}
	if b, _ := Info(installed); b.Version != "1.0.0" {
		t.Fatalf("installed after the rollback: %s", b.Version)
	}
	if err := Rollback(installed, previous); err == nil {
		t.Fatal("a second rollback had nothing to put back")
	}
}

// The Security framework's own answers: Apple's app meets Apple's anchor; a
// bundle signed by someone else, or altered, or unsigned, is refused; one
// that isn't there is a check that couldn't be made.
func TestVerify(t *testing.T) {
	if err := Verify("/System/Applications/Calculator.app", "anchor apple"); err != nil {
		t.Fatalf("Apple's own app: %v", err)
	}
	app := fakeApp(t, t.TempDir(), "Demo", "1.0.0")
	if err := Verify(app, ""); !errors.Is(err, ErrBadSignature) || !errors.Is(err, amarit.ErrRefused) {
		t.Fatalf("unsigned: %v", err)
	}
	sign(t, app)
	if err := Verify(app, ""); err != nil {
		t.Fatalf("signed: %v", err)
	}
	if err := Verify(app, SignedBy(strings.Repeat("0", 40))); !errors.Is(err, ErrWrongSigner) || !errors.Is(err, amarit.ErrRefused) {
		t.Fatalf("another key: %v", err)
	}
	plist := filepath.Join(app, "Contents", "Info.plist")
	b, _ := os.ReadFile(plist)
	os.WriteFile(plist, append(b, ' '), 0o644)
	if err := Verify(app, ""); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("altered: %v", err)
	}
	if err := Verify(filepath.Join(t.TempDir(), "None.app"), ""); err == nil || errors.Is(err, amarit.ErrRefused) {
		t.Fatalf("missing must be a check that couldn't be made: %v", err)
	}
}

func TestInstallerPrepareRefusesAnotherVersion(t *testing.T) {
	zip := zipApp(t, fakeApp(t, t.TempDir(), "Demo", "1.2.3"))
	file := filepath.Join(t.TempDir(), "download.zip")
	os.Rename(zip, file)
	i := &Installer{App: "/Applications/Demo.app", StateDir: t.TempDir()}
	if _, err := i.Prepare(context.Background(), file, "1.2.4"); !errors.Is(err, ErrOtherVersion) || !errors.Is(err, amarit.ErrRefused) {
		t.Fatalf("another version: %v", err)
	}
	staged, err := i.Prepare(context.Background(), file, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if got, v := i.Staged(); got != staged || v != "1.2.3" {
		t.Fatalf("Staged: %s %s", got, v)
	}
}

func TestWritable(t *testing.T) {
	dir := t.TempDir()
	app := fakeApp(t, dir, "Demo", "1.0.0")
	if err := Writable(app); err != nil {
		t.Fatal(err)
	}
	os.Chmod(dir, 0o555)
	defer os.Chmod(dir, 0o755)
	if err := Writable(app); !errors.Is(err, amarit.ErrNotWritable) {
		t.Fatalf("a read-only folder: %v", err)
	}
}

type fakeCopies struct {
	quit    bool
	started []string
	exe     string
}

func (f *fakeCopies) QuitOthers() []string { f.quit = true; return []string{"mine", "other"} }
func (f *fakeCopies) StartAfter(exe string, copies []string) {
	f.exe, f.started = exe, copies
}

// Install quits the other copies before the swap, starts every copy on the
// new release after it, and ends the app.
func TestInstallerInstallRestartsEveryCopy(t *testing.T) {
	dir := t.TempDir()
	installed := fakeApp(t, filepath.Join(dir, "Applications"), "Demo", "1.0.0")
	state := filepath.Join(dir, "state")
	staged, err := Stage(state, fakeApp(t, filepath.Join(dir, "new"), "Demo", "1.1.0"))
	if err != nil {
		t.Fatal(err)
	}
	copies := &fakeCopies{}
	quit := false
	i := &Installer{App: installed, StateDir: state, Copies: copies, Quit: func() { quit = true }}
	if err := i.Install(staged); err != nil {
		t.Fatal(err)
	}
	if b, _ := Info(installed); b.Version != "1.1.0" {
		t.Fatalf("installed: %s", b.Version)
	}
	if b, _ := Info(i.previous()); b.Version != "1.0.0" {
		t.Fatalf("previous: %s", b.Version)
	}
	want := filepath.Join(installed, "Contents", "MacOS", "demo")
	if !copies.quit || copies.exe != want || strings.Join(copies.started, ",") != "mine,other" || !quit {
		t.Fatalf("copies %+v, quit %v", copies, quit)
	}
}
