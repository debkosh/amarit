package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/debkosh/amarit"
)

// fakeGitHub answers the gh commands publish runs, from releases in memory.
type fakeGitHub struct {
	files      map[string]map[string][]byte // tag -> file name -> bytes
	prerelease map[string]bool
	order      []string // tags in the order made; the latest is the last that is not a pre-release
	uploads    int
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{files: map[string]map[string][]byte{}, prerelease: map[string]bool{}}
	old := gh
	gh = f.run
	t.Cleanup(func() { gh = old })
	return f
}

func (f *fakeGitHub) add(tag string, pre bool) {
	f.files[tag] = map[string][]byte{}
	f.prerelease[tag] = pre
	f.order = append(f.order, tag)
}

func (f *fakeGitHub) run(args ...string) ([]byte, error) {
	var pos []string
	var output string
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--repo", "--json", "--title", "--notes", "--pattern":
			i++
		case "--output":
			i++
			output = args[i]
		case "--prerelease", "--clobber", "--yes":
		default:
			pos = append(pos, args[i])
		}
	}
	notFound := errors.New("gh: release not found")
	switch args[1] {
	case "view":
		tag := ""
		if len(pos) > 0 {
			tag = pos[0]
		} else {
			for _, t := range f.order {
				if !f.prerelease[t] {
					tag = t
				}
			}
		}
		files, ok := f.files[tag]
		if !ok {
			return nil, notFound
		}
		out := `{"tagName":"` + tag + `","assets":[`
		for i, n := range slices.Sorted(func(yield func(string) bool) {
			for n := range files {
				if !yield(n) {
					return
				}
			}
		}) {
			if i > 0 {
				out += ","
			}
			out += `{"name":"` + n + `"}`
		}
		return []byte(out + "]}"), nil
	case "create":
		f.add(pos[0], slices.Contains(args, "--prerelease"))
		return nil, nil
	case "download":
		data, ok := f.files[pos[0]]["releases.json"]
		if !ok {
			return nil, errors.New("gh: no assets match the file pattern")
		}
		return nil, os.WriteFile(output, data, 0o644)
	case "upload":
		for _, p := range pos[1:] {
			data, err := os.ReadFile(p)
			if err != nil {
				return nil, err
			}
			f.files[pos[0]][filepath.Base(p)] = data
			f.uploads++
		}
		return nil, nil
	case "delete-asset":
		delete(f.files[pos[0]], pos[1])
		return nil, nil
	}
	return nil, errors.New("fake gh: " + strings.Join(args, " "))
}

func (f *fakeGitHub) names(tag string) []string {
	var out []string
	for n := range f.files[tag] {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func (f *fakeGitHub) manifest(t *testing.T, tag string) *amarit.Manifest {
	t.Helper()
	m, err := amarit.ParseManifest(f.files[tag]["releases.json"])
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A dev channel on a fixed-tag pre-release beside numbered stable releases,
// as Project B publishes it.
func TestPublishChannels(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "amarit.json")
	os.WriteFile(cfg, []byte(`{"project":"pb","releases":"https://github.com/o/pb/releases/download/channels/releases.json",
		"release":{"targets":["darwin/arm64"],"asset":"PB-{version}-{os}-{arch}.zip","url":"https://github.com/o/pb/releases/download/v{version}/{asset}"}}`), 0o644)
	dist := filepath.Join(dir, "dist")
	gh := newFakeGitHub(t)

	build := func(v string) {
		os.RemoveAll(dist)
		os.MkdirAll(dist, 0o755)
		os.WriteFile(filepath.Join(dist, "PB-"+v+"-darwin-arm64.zip"), []byte("zip "+v), 0o644)
		os.WriteFile(filepath.Join(dist, "PB-"+v+".dmg"), []byte("dmg "+v), 0o644)
	}
	common := []string{"-github", "o/pb", "-tag", "channels", "-config", cfg, "-dist", dist}

	// The stable 0.3.8 was released before channels, with its manifest on its own release.
	build("0.3.8")
	if err := release([]string{"-config", cfg, "-version", "0.3.8", "-dist", dist, "-out", filepath.Join(dist, "releases.json")}); err != nil {
		t.Fatal(err)
	}
	gh.add("v0.3.8", false)
	data, _ := os.ReadFile(filepath.Join(dist, "releases.json"))
	gh.files["v0.3.8"]["releases.json"] = data

	// The first dev build makes the pre-release and starts from 0.3.8's manifest.
	build("0.3.9-dev.1")
	dev := func(v string) error {
		return publish(append(common, "-version", v, "-channel", "dev", "-url", "{asset}", "-prune", filepath.Join(dist, "PB-"+v+".dmg")))
	}
	if err := dev("0.3.9-dev.1"); err != nil {
		t.Fatal(err)
	}
	if !gh.prerelease["channels"] {
		t.Fatal("channels must be made a pre-release")
	}
	if got := gh.names("channels"); !slices.Equal(got, []string{"PB-0.3.9-dev.1-darwin-arm64.zip", "PB-0.3.9-dev.1.dmg", "releases.json"}) {
		t.Fatalf("on channels: %v", got)
	}
	m := gh.manifest(t, "channels")
	if m.Channels["stable"] != "0.3.8" || m.Channels["dev"] != "0.3.9-dev.1" {
		t.Fatalf("channels: %v", m.Channels)
	}
	if a := m.Releases["0.3.9-dev.1"].Assets["darwin/arm64"]; a.URL != "PB-0.3.9-dev.1-darwin-arm64.zip" {
		t.Fatalf("dev asset url %s", a.URL)
	}

	// The next one replaces it, files and all; stable is untouched.
	build("0.3.9-dev.2")
	if err := dev("0.3.9-dev.2"); err != nil {
		t.Fatal(err)
	}
	if got := gh.names("channels"); !slices.Equal(got, []string{"PB-0.3.9-dev.2-darwin-arm64.zip", "PB-0.3.9-dev.2.dmg", "releases.json"}) {
		t.Fatalf("on channels after the second build: %v", got)
	}
	m = gh.manifest(t, "channels")
	if _, ok := m.Releases["0.3.9-dev.1"]; ok || m.Channels["stable"] != "0.3.8" {
		t.Fatalf("after the second build: %v", m.Channels)
	}

	// Stable 0.3.9: its assets are on its own release, so only the manifest goes to channels.
	build("0.3.9")
	gh.add("v0.3.9", false)
	before := gh.uploads
	if err := publish(append(common, "-version", "0.3.9")); err != nil {
		t.Fatal(err)
	}
	if gh.uploads != before+1 {
		t.Fatalf("stable uploaded %d files to channels; want only the manifest", gh.uploads-before)
	}
	m = gh.manifest(t, "channels")
	if m.Channels["stable"] != "0.3.9" || m.Channels["dev"] != "0.3.9-dev.2" || len(m.Releases) != 3 {
		t.Fatalf("after stable: %v, %d releases", m.Channels, len(m.Releases))
	}

	// A manifest there that does not parse stops the publish before anything is uploaded.
	gh.files["channels"]["releases.json"] = []byte("{")
	before = gh.uploads
	if err := dev("0.3.9-dev.2"); err == nil || gh.uploads != before {
		t.Fatalf("a broken manifest must stop the publish: %v, %d uploads", err, gh.uploads-before)
	}
}

func TestFilesOfADroppedVersion(t *testing.T) {
	m := amarit.Manifest{Releases: map[string]amarit.Release{"0.3.9": {}, "0.3.9-dev.2": {}, "0.3.10": {}}}
	names := []string{"PB-0.3.9-dev.1.dmg", "PB-0.3.9-dev.1-darwin-arm64.zip", "PB-0.3.9-dev.12.dmg", "PB-0.3.9.dmg",
		"PB-0.3.9-dev.2.dmg", "releases.json", "app-latest.apk"}
	if got := filesOf("0.3.9-dev.1", names, m); !slices.Equal(got, []string{"PB-0.3.9-dev.1.dmg", "PB-0.3.9-dev.1-darwin-arm64.zip"}) {
		t.Fatalf("files of 0.3.9-dev.1: %v", got)
	}
	// A stable release's files, not the dev builds named after it, nor 0.3.10's.
	m = amarit.Manifest{Releases: map[string]amarit.Release{"0.3.9-dev.2": {}, "0.3.10": {}}}
	names = []string{"PB-0.3.9.dmg", "PB-v0.3.9-darwin-arm64.zip", "PB-0.3.9-dev.2.dmg", "PB-0.3.10.dmg", "PB-10.3.9.dmg"}
	if got := filesOf("0.3.9", names, m); !slices.Equal(got, []string{"PB-0.3.9.dmg", "PB-v0.3.9-darwin-arm64.zip"}) {
		t.Fatalf("files of 0.3.9: %v", got)
	}
}
