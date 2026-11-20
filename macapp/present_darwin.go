//go:build cgo

package macapp

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AppKit -framework UserNotifications
#include <stdlib.h>
#include "present_darwin.h"
*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/sudiptadeb/amarit/appupdate"
)

// Words are what the Dock and the notification say; Present fills in the
// English ones an app leaves empty. Ready is a format of the app's name and
// the version.
type Words struct {
	Ready    string // "%s %s is ready"
	Body     string // "Relaunch to update."
	Relaunch string // "Relaunch"
}

// Category is the notification category of amarit's ready notification. An
// app with its own UNUserNotificationCenter delegate forwards a response in
// it to HandleNotificationResponse; amarit installs a delegate only when the
// app has none.
const Category = "amarit.update"

var (
	presented   *appupdate.Updater
	presentedMu sync.Mutex
)

// Present shows u's state the Mac's way: a progress bar on the app's Dock
// icon while it downloads and checks, and, when a release is ready, a
// notification with a Relaunch button that sends Relaunch. The app needs an
// NSApplication, which an app with a Dock icon has, and a bundle identifier
// for the notification. One updater per process.
func Present(u *appupdate.Updater, w Words) {
	if w.Ready == "" {
		w.Ready = "%s %s is ready"
	}
	if w.Body == "" {
		w.Body = "Relaunch to update."
	}
	if w.Relaunch == "" {
		w.Relaunch = "Relaunch"
	}
	presentedMu.Lock()
	presented = u
	presentedMu.Unlock()
	cname := C.macapp_app_name()
	name := C.GoString(cname)
	C.free(unsafe.Pointer(cname))
	var notified string
	u.OnChange(func(s appupdate.State) {
		switch s.Phase {
		case appupdate.Downloading:
			f := 0.0
			if s.Total > 0 {
				f = float64(s.Done) / float64(s.Total)
			}
			C.macapp_dock_progress(C.double(f))
		case appupdate.Verifying:
			C.macapp_dock_progress(1)
		default:
			C.macapp_dock_clear()
		}
		switch {
		case s.Phase == appupdate.Ready && notified != s.Version:
			notified = s.Version
			title := C.CString(fmt.Sprintf(w.Ready, name, s.Version))
			body, button := C.CString(w.Body), C.CString(w.Relaunch)
			C.macapp_notify(title, body, button)
			C.free(unsafe.Pointer(title))
			C.free(unsafe.Pointer(body))
			C.free(unsafe.Pointer(button))
		case s.Phase != appupdate.Ready && notified != "":
			notified = ""
			C.macapp_notify_remove()
		}
	})
}

// HandleNotificationResponse is for an app with its own notification
// delegate: it hands amarit the responses in Category and says whether the
// response was amarit's.
func HandleNotificationResponse(category, action string) bool {
	if category != Category {
		return false
	}
	if action == "amarit.relaunch" {
		presentedMu.Lock()
		u := presented
		presentedMu.Unlock()
		if u != nil {
			u.Relaunch()
		}
	}
	return true
}

//export macappNotificationResponse
func macappNotificationResponse(category, action *C.char) {
	HandleNotificationResponse(C.GoString(category), C.GoString(action))
}
