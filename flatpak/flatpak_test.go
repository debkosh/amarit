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
}
