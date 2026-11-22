// Package flatpak is appupdate's Source for an app that is a Flatpak: it
// listens to the Flatpak portal's update monitor, makes the update through
// it, and relaunches into the new deployment with Spawn. The app's shell
// sees appupdate's State and sends its commands as on any other platform.
//
// What differs from amarit's manifest, in appupdate's terms:
//
//   - The monitor polls the remote on its own timer, every 30 minutes, and
//     says when it heard of a newer commit, local or remote. Check can't ask
//     anyone: it makes the update the monitor heard of, or shows current, so
//     an app knows of an update up to 30 minutes after it starts.
//   - The portal names commits, not versions, so State.Version is empty.
//   - An update that asks for new permissions is refused by the portal; it
//     fails as appupdate.NeedsUser, and the person updates in the software
//     centre.
//   - An update made outside the app, by GNOME Software or `flatpak update`,
//     is ready at once.
//   - There are no channels: SetChannel is appupdate.ErrUnsupported.
//   - The relaunch is Spawn with the latest-version flag, since /app stays the
//     deployment the app started from; then the app quits. The portal starts
//     the new copy with the running copy's sandbox, so a release that adds
//     permissions, which only the software centre installs, gets them at the
//     next start the person makes.
//   - The first update asks the person, once, through the Access portal
//     ("Allow … to update itself?"); appupdate shows downloading at 0 while
//     the dialog waits, and a refusal fails as appupdate.NeedsUser.
//
// It is a module of its own, on github.com/godbus/dbus/v5, so amarit's core
// stays standard library only.
package flatpak

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"

	"github.com/sudiptadeb/amarit/appupdate"
)

const (
	portalName    = "org.freedesktop.portal.Flatpak"
	portalPath    = "/org/freedesktop/portal/Flatpak"
	portalIface   = "org.freedesktop.portal.Flatpak"
	monitorIface  = "org.freedesktop.portal.Flatpak.UpdateMonitor"
	spawnLatest   = 1 << 1 // FLATPAK_SPAWN_FLAGS_LATEST_VERSION
	statusRunning = 0
	statusEmpty   = 1
	statusDone    = 2
	statusError   = 3
)

// Copies are the other running copies of the app, for an app that runs
// several, one per profile say: Install quits them, and starts each again
// on the new release.
type Copies interface {
	// QuitOthers tells every other running copy to quit and returns once
	// they have, with what to start again: this copy's first.
	QuitOthers() []string
	// Args is the command line, argv[0] included, that starts copy.
	Args(copy string) []string
}

// Options say how the app starts again.
type Options struct {
	// Copies, when set, are quit before the relaunch and started after it;
	// else this copy starts again with Args, os.Args unless set.
	Copies Copies
	Args   []string
	// Quit ends the app once the new release is starting.
	Quit func()
}

// Source is the Flatpak portal's update monitor, as an appupdate.Source.
type Source struct {
	opts    Options
	conn    *dbus.Conn
	portal  dbus.BusObject
	monitor dbus.BusObject
	signals chan *dbus.Signal
	changed chan struct{}

	mu       sync.Mutex
	heard    map[string]string // the last UpdateAvailable: running-commit, local-commit, remote-commit
	progress chan progress     // the running Fetch's, or nil
	closed   bool
	desk     dbus.BusObject // the Notification portal, once Present has run
}

type progress struct {
	ops, op, percent, status uint32
	errName, errMessage      string
}

// New connects to the session bus and makes an update monitor. Outside a
// Flatpak, the portal refuses, and New says so.
func New(opts Options) (*Source, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("flatpak: session bus: %w", err)
	}
	s := &Source{
		opts:    opts,
		conn:    conn,
		portal:  conn.Object(portalName, portalPath),
		signals: make(chan *dbus.Signal, 16),
		changed: make(chan struct{}, 1),
	}
	var path dbus.ObjectPath
	if err := s.portal.Call(portalIface+".CreateUpdateMonitor", 0, map[string]dbus.Variant{}).Store(&path); err != nil {
		conn.Close()
		return nil, fmt.Errorf("flatpak: update monitor: %w", err)
	}
	s.monitor = conn.Object(portalName, path)
	if err := conn.AddMatchSignal(dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(monitorIface)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("flatpak: signals: %w", err)
	}
	conn.Signal(s.signals)
	go s.listen()
	return s, nil
}

// Close ends the monitor and the connection.
func (s *Source) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.monitor.Call(monitorIface+".Close", 0)
	return s.conn.Close()
}

func (s *Source) listen() {
	for sig := range s.signals {
		if len(sig.Body) == 0 {
			continue
		}
		info, ok := sig.Body[0].(map[string]dbus.Variant)
		if !ok {
			continue
		}
		switch sig.Name {
		case monitorIface + ".UpdateAvailable":
			heard := map[string]string{}
			for _, k := range []string{"running-commit", "local-commit", "remote-commit"} {
				heard[k], _ = info[k].Value().(string)
			}
			s.mu.Lock()
			s.heard = heard
			s.mu.Unlock()
			select {
			case s.changed <- struct{}{}:
			default:
			}
		case monitorIface + ".Progress":
			p := progress{}
			p.ops, _ = info["n_ops"].Value().(uint32)
			p.op, _ = info["op"].Value().(uint32)
			p.percent, _ = info["progress"].Value().(uint32)
			p.status, _ = info["status"].Value().(uint32)
			p.errName, _ = info["error"].Value().(string)
			p.errMessage, _ = info["error_message"].Value().(string)
			s.mu.Lock()
			ch := s.progress
			s.mu.Unlock()
			if ch != nil {
				select {
				case ch <- p:
				default:
				}
			}
		}
	}
}

// Look says what the monitor last heard: an update installed beside the
// running one is ready; a newer remote commit is one to make.
func (s *Source) Look(ctx context.Context) (appupdate.Found, error) {
	s.mu.Lock()
	h := s.heard
	s.mu.Unlock()
	running, local, remote := h["running-commit"], h["local-commit"], h["remote-commit"]
	switch {
	case remote != "" && remote != local && remote != running:
		return appupdate.Found{ID: remote}, nil
	case local != "" && local != running:
		return appupdate.Found{ID: local, Ready: true}, nil
	}
	return appupdate.Found{}, nil
}

// Fetch makes the update through the portal, reporting its progress.
func (s *Source) Fetch(ctx context.Context, f appupdate.Found, report func(done, total int64)) error {
	ch := make(chan progress, 16)
	s.mu.Lock()
	s.progress = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.progress = nil
		s.mu.Unlock()
	}()
	if err := s.monitor.CallWithContext(ctx, monitorIface+".Update", 0, "", map[string]dbus.Variant{}).Err; err != nil {
		return refusal(err.Error(), "")
	}
	for {
		select {
		case <-ctx.Done():
			// The portal has no cancel: the update finishes in the
			// background, and the next round finds it installed.
			return ctx.Err()
		case p := <-ch:
			if p.ops > 0 {
				report(int64(p.op)*100+int64(p.percent), int64(p.ops)*100)
			}
			switch p.status {
			case statusDone, statusEmpty:
				return nil
			case statusError:
				return refusal(p.errName, p.errMessage)
			}
		}
	}
}

// refusal words a failed update. The portal refuses with NotSupported an
// update that asks for new permissions ("Self update not supported, new
// version requires new permissions") or a system with no Access portal,
// and with AccessDenied one the person turned down: each is made, if at
// all, in the software centre.
func refusal(name, message string) error {
	err := fmt.Errorf("flatpak: %s: %s", name, message)
	switch name {
	case "org.freedesktop.DBus.Error.NotSupported", "org.freedesktop.DBus.Error.AccessDenied":
		return fmt.Errorf("%w: %w", appupdate.ErrNeedsUser, err)
	}
	return err
}

// Install starts the new deployment, once per copy with Copies, and quits.
// Spawn starts it at once, not after this process: an app with one copy per
// profile has the new one wait for the old one's single-instance lock.
func (s *Source) Install() error {
	var commands [][]string
	if s.opts.Copies != nil {
		for _, c := range s.opts.Copies.QuitOthers() {
			commands = append(commands, s.opts.Copies.Args(c))
		}
	} else {
		args := s.opts.Args
		if len(args) == 0 {
			args = os.Args
		}
		commands = [][]string{args}
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "/"
	}
	s.unpresent()
	for _, argv := range commands {
		if err := s.spawn(cwd, argv); err != nil {
			return err
		}
	}
	if s.opts.Quit != nil {
		s.opts.Quit()
	}
	return nil
}

func (s *Source) spawn(cwd string, argv []string) error {
	if len(argv) == 0 {
		return errors.New("flatpak: nothing to spawn")
	}
	bytes := make([][]byte, len(argv))
	for i, a := range argv {
		bytes[i] = append([]byte(a), 0)
	}
	var pid uint32
	return s.portal.Call(portalIface+".Spawn", 0,
		append([]byte(cwd), 0), bytes,
		map[uint32]dbus.UnixFD{}, map[string]string{},
		uint32(spawnLatest), map[string]dbus.Variant{}).Store(&pid)
}

// Changed is sent on when the monitor hears of an update.
func (s *Source) Changed() <-chan struct{} { return s.changed }
