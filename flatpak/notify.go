package flatpak

import (
	"github.com/godbus/dbus/v5"

	"github.com/debkosh/amarit/appupdate"
)

const (
	desktopName = "org.freedesktop.portal.Desktop"
	desktopPath = "/org/freedesktop/portal/desktop"
	notifyIface = "org.freedesktop.portal.Notification"
	notifyID    = "amarit-update"
)

// Words are what the notification says; Present fills in the English ones
// an app leaves empty. The portal names commits, not versions, so they
// carry none.
type Words struct {
	Title    string // "An update is ready"
	Body     string // "Relaunch to use it."
	Relaunch string // "Relaunch"
}

// Present shows u's ready release the Linux desktop's way: a notification
// through the Notification portal, which a sandboxed app must use, with a
// Relaunch button that sends Relaunch. A Linux desktop has no Dock progress
// to draw on.
func (s *Source) Present(u *appupdate.Updater, w Words) error {
	if w.Title == "" {
		w.Title = "An update is ready"
	}
	if w.Body == "" {
		w.Body = "Relaunch to use it."
	}
	if w.Relaunch == "" {
		w.Relaunch = "Relaunch"
	}
	desk := s.conn.Object(desktopName, desktopPath)
	s.mu.Lock()
	s.desk = desk
	s.mu.Unlock()
	if err := s.conn.AddMatchSignal(dbus.WithMatchInterface(notifyIface), dbus.WithMatchMember("ActionInvoked")); err != nil {
		return err
	}
	actions := make(chan *dbus.Signal, 4)
	s.conn.Signal(actions)
	go func() {
		for sig := range actions {
			if sig.Name != notifyIface+".ActionInvoked" || len(sig.Body) < 2 {
				continue
			}
			if id, _ := sig.Body[0].(string); id != notifyID {
				continue
			}
			if action, _ := sig.Body[1].(string); action == "relaunch" {
				u.Relaunch()
			}
		}
	}()
	shown := false
	u.OnChange(func(st appupdate.State) {
		switch {
		case st.Phase == appupdate.Ready && !shown:
			shown = true
			desk.Call(notifyIface+".AddNotification", 0, notifyID, map[string]dbus.Variant{
				"title":    dbus.MakeVariant(w.Title),
				"body":     dbus.MakeVariant(w.Body),
				"priority": dbus.MakeVariant("normal"),
				"buttons": dbus.MakeVariant([]map[string]dbus.Variant{{
					"label":  dbus.MakeVariant(w.Relaunch),
					"action": dbus.MakeVariant("relaunch"),
				}}),
			})
		case st.Phase != appupdate.Ready && shown:
			shown = false
			desk.Call(notifyIface+".RemoveNotification", 0, notifyID)
		}
	})
	return nil
}

// unpresent takes the ready notification away, before a relaunch the
// person started in the app rather than from the notification: the new
// release has no update to announce.
func (s *Source) unpresent() {
	s.mu.Lock()
	desk := s.desk
	s.mu.Unlock()
	if desk != nil {
		desk.Call(notifyIface+".RemoveNotification", 0, notifyID)
	}
}
