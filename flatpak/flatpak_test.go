package flatpak

import (
	"context"
	"errors"
	"testing"

	"github.com/sudiptadeb/amarit/appupdate"
)

func TestLookReadsWhatTheMonitorHeard(t *testing.T) {
	for _, c := range []struct {
		heard map[string]string
		want  appupdate.Found
	}{
		{nil, appupdate.Found{}},
		{map[string]string{"running-commit": "a", "local-commit": "a", "remote-commit": "a"}, appupdate.Found{}},
		{map[string]string{"running-commit": "a", "local-commit": "a", "remote-commit": "b"}, appupdate.Found{ID: "b"}},
		// GNOME Software updated the app while it ran.
		{map[string]string{"running-commit": "a", "local-commit": "b", "remote-commit": "b"}, appupdate.Found{ID: "b", Ready: true}},
		// Installed b beside the running a, and the remote has c already.
		{map[string]string{"running-commit": "a", "local-commit": "b", "remote-commit": "c"}, appupdate.Found{ID: "c"}},
	} {
		s := &Source{heard: c.heard}
		got, err := s.Look(context.Background())
		if err != nil || got != c.want {
			t.Errorf("heard %v: got %+v %v, want %+v", c.heard, got, err, c.want)
		}
	}
}

func TestRefusalForNewPermissionsNeedsTheUser(t *testing.T) {
	if err := refusal("org.freedesktop.DBus.Error.Failed", "network unreachable"); errors.Is(err, appupdate.ErrNeedsUser) {
		t.Fatalf("a network failure: %v", err)
	}
	if err := refusal("org.freedesktop.DBus.Error.NotSupported", "Self update not supported, new version requires new permissions"); !errors.Is(err, appupdate.ErrNeedsUser) {
		t.Fatalf("new permissions: %v", err)
	}
	if err := refusal("org.freedesktop.DBus.Error.AccessDenied", "Application update not allowed"); !errors.Is(err, appupdate.ErrNeedsUser) {
		t.Fatalf("turned down: %v", err)
	}
	// As GNOME 46 said it on Ubuntu 24.04, for an app not in front.
	err := refusal("org.freedesktop.DBus.Error.AccessDenied", "GDBus.Error:org.freedesktop.DBus.Error.AccessDenied: Only the focused app is allowed to show a system access dialog")
	if !errors.Is(err, appupdate.ErrNeedsPermission) || errors.Is(err, appupdate.ErrNeedsUser) {
		t.Fatalf("not in front: %v", err)
	}
}

// VersionOf names the commit Look found, the remote one to fetch or the
// local one already deployed; "" leaves Version empty.
func TestLookNamesTheVersion(t *testing.T) {
	versions := map[string]string{"b": "0.4.1", "c": "0.4.2"}
	s := &Source{opts: Options{VersionOf: func(ctx context.Context, commit string) string { return versions[commit] }}}
	for heard, want := range map[[3]string]appupdate.Found{
		{"a", "a", "c"}: {ID: "c", Version: "0.4.2"},
		{"a", "b", "b"}: {ID: "b", Version: "0.4.1", Ready: true},
		{"a", "a", "d"}: {ID: "d"},
		{"a", "a", "a"}: {},
	} {
		s.heard = map[string]string{"running-commit": heard[0], "local-commit": heard[1], "remote-commit": heard[2]}
		if got, _ := s.Look(context.Background()); got != want {
			t.Errorf("heard %v: got %+v, want %+v", heard, got, want)
		}
	}
}
