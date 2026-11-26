//go:build darwin && cgo

// Command macupdate is an app that updates itself with appupdate and macapp,
// from a release it serves itself, slowly, so the Dock's progress bar and
// the ready notification can be seen. ./build.sh makes "amarit demo.app"
// 1.0.0; open it, and it downloads 1.1.0, shows Relaunch, and relaunches
// into it.
package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>

static void run_app(const char *ctitle) {
	[NSApplication sharedApplication];
	[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
	NSWindow *w = [[NSWindow alloc] initWithContentRect:NSMakeRect(200, 200, 420, 120)
	                                           styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable
	                                             backing:NSBackingStoreBuffered defer:NO];
	w.title = [NSString stringWithUTF8String:ctitle];
	w.releasedWhenClosed = NO;
	[w makeKeyAndOrderFront:nil];
	[NSApp activateIgnoringOtherApps:YES];
	[NSApp run];
}

static void quit_app(void) {
	dispatch_async(dispatch_get_main_queue(), ^{ [NSApp terminate:nil]; });
}
*/
import "C"

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/debkosh/amarit"
	"github.com/debkosh/amarit/appupdate"
	"github.com/debkosh/amarit/macapp"
)

func init() { runtime.LockOSThread() }

func main() {
	app := macapp.Running()
	b, err := macapp.Info(app)
	if err != nil {
		log.Fatal(err)
	}
	state := filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "amarit-demo")
	logf, _ := os.OpenFile(filepath.Join(os.TempDir(), "amarit-demo.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	log.SetOutput(logf)
	if b.Version == "1.0.0" {
		go serveNext(app, state, b)
	}
	C.run_app(C.CString("amarit demo " + b.Version))
}

// serveNext serves 1.1.0 of this same bundle, slowly, and updates to it.
func serveNext(app, state string, b macapp.Bundle) {
	dir, _ := os.MkdirTemp("", "amarit-demo")
	next := filepath.Join(dir, filepath.Base(app))
	exec.Command("/usr/bin/ditto", app, next).Run()
	plist := filepath.Join(next, "Contents", "Info.plist")
	raw, _ := os.ReadFile(plist)
	os.WriteFile(plist, []byte(replace(string(raw), "<string>1.0.0</string>", "<string>1.1.0</string>")), 0o644)
	zip := filepath.Join(dir, "next.zip")
	if out, err := exec.Command("/usr/bin/ditto", "-c", "-k", "--keepParent", next, zip).CombinedOutput(); err != nil {
		log.Fatalf("zip: %v %s", err, out)
	}
	body, _ := os.ReadFile(zip)
	sum := sha256.Sum256(body)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	base := "http://" + ln.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("/releases.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"spec":1,"project":"demo","published":"2026-11-20T00:00:00Z","channels":{"stable":"1.1.0"},
		  "releases":{"1.1.0":{"assets":{"%s/%s":{"url":"%s/next.zip","sha256":"%s","size":%d}}}}}`,
			runtime.GOOS, runtime.GOARCH, base, hex.EncodeToString(sum[:]), len(body))
	})
	mux.HandleFunc("/next.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		// About twelve seconds, whatever the size.
		step := len(body)/120 + 1
		for i := 0; i < len(body); i += step {
			w.Write(body[i:min(i+step, len(body))])
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	})
	go http.Serve(ln, mux)
	// The project's amarit.json, which turns automatic updates on.
	project := fmt.Sprintf(`{"project":"demo","releases":"%s/releases.json","auto_update":true}`, base)
	c, err := amarit.NewClient(state, "stable", amarit.ProjectFile([]byte(project)), amarit.Version(b.Version))
	if err != nil {
		log.Fatal(err)
	}
	inst := &macapp.Installer{App: app, StateDir: state, Quit: func() { C.quit_app() }}
	u := appupdate.New(appupdate.Config{Client: c, Installer: inst, Dir: state, FirstCheck: time.Second})
	u.OnChange(func(s appupdate.State) { log.Printf("state %+v", s) })
	macapp.Present(u, macapp.Words{})
	u.Start()
}

func replace(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}
