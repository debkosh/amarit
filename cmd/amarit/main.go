// Command amarit is the release side of the library: it stamps builds and
// writes the releases.json a project publishes next to its release assets.
//
//	amarit stamp   [-config amarit.json] -version 0.5.3
//	amarit release [-config amarit.json] -version 0.5.3 -dist dist [-channel stable] [-out releases.json]
//	amarit release -version 0.2.1+g6c3c4d4 -version-code 3 -channel latest -asset app-latest.apk -url https://.../app-latest.apk -prune
//	amarit publish -github owner/repo -tag channels -version 0.3.9-dev.202611201415 -channel dev -prune [file ...]
//	amarit next-version [-pre dev] 0.3.8
//	amarit version-code 0.3.9-dev.202611201415
//	amarit update  [-check] [-rolling] [unit|glob ...]
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sudiptadeb/amarit"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "stamp":
		err = stamp(os.Args[2:])
	case "release":
		err = release(os.Args[2:])
	case "publish":
		err = publish(os.Args[2:])
	case "next-version":
		err = nextVersion(os.Args[2:])
	case "version-code":
		err = versionCode(os.Args[2:])
	case "install":
		err = install(os.Args[2:])
	case "uninstall":
		err = uninstall(os.Args[2:])
	case "daemon":
		err = daemon(os.Args[2:])
	case "run":
		err = run(os.Args[2:])
	case "ls", "status":
		err = ls(os.Args[2:])
	case "stop":
		err = named(os.Args[2:], func(n string) error { return amarit.DefaultBase().Enable(n, false) })
	case "start":
		err = start(os.Args[2:])
	case "restart":
		err = restart(os.Args[2:])
	case "update":
		err = update(os.Args[2:])
	case "rm":
		err = named(os.Args[2:], amarit.DefaultBase().RemoveUnit)
	case "logs":
		err = logs(os.Args[2:])
	case "__exec":
		// Hidden: the daemon starts a unit that has limits through this.
		err = amarit.ExecLimited(os.Args[2:])
	case "version", "-version", "--version":
		fmt.Println(buildVersion())
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "amarit:", err)
		os.Exit(1)
	}
}

// buildVersion is what the library resolved for this build (the stamp, else
// the module version `go install` recorded), so the banner and the updater
// never disagree; "dev" when neither is there.
func buildVersion() string {
	if v := amarit.BuildVersion(); v != "" {
		return "v" + v
	}
	return "dev"
}

func usage() {
	fmt.Fprintln(os.Stderr, `amarit: keep it alive, keep it current.

keep it alive (this machine):
  amarit install [--system]        keep the amarit daemon itself running; --system needs sudo once,
                                   after which 'amarit run' never does
  amarit daemon [--no-auto-update] what the job runs; a release build keeps itself current
                                   unless told not to (or AMARIT_AUTO_UPDATE=0 in its environment)
  amarit run [-name N] [-instances N] [-force] <binary> [args...]
                                   keep a program running (unit named after the binary);
                                   -instances N runs N copies named <name>@1..N; refuses a
                                   program launchd or systemd already runs unless -force
  amarit ls                        every unit, its state, pid, restarts
  amarit logs [-f] <unit>[@<i>]    a unit's output, or one instance's
  amarit stop <unit>[@<i>]         stop a unit, or one instance of it
  amarit start [-force] <unit>[@<i>]
  amarit restart <unit|glob>... [-rolling]
                                   restart units now (web-*, worker@*); -rolling does one
                                   at a time and waits for each to stay up before the next
  amarit update [-check] [-rolling] [unit|glob ...]
                                   bring units onto the release their manifest points at: runs
                                   '<binary> update' once per binary, then restarts the units
                                   whose binary changed (-rolling as above); no names = every
                                   unit whose program was built with amarit; -check only reports;
                                   if the restart fails the previous binary is put back
  amarit rm <unit>
  amarit uninstall

A unit is a JSON file in ~/.amarit/units/; see docs/daemon.md for its fields
(env, env_file, dir, restart, no_restart_exit, stop_timeout, limits, instances).

keep it current (release side):
  amarit stamp   [-config amarit.json] -version X                print the -ldflags that stamp a build
  amarit release [-config amarit.json] -version X -dist DIR      write releases.json for the assets in DIR
                 [-channel C] [-out FILE] [-expires D]           merged into FILE when it already exists
                 [-version-code N]                               the APK versionCode; required for the android target
                 [-asset PATTERN] [-url PATTERN] [-prune]        this run's patterns over release.asset and release.url;
                                                                 -prune drops the release the channel pointed at until
                                                                 now, unless another channel still points at it
  amarit publish -github OWNER/REPO -tag T [release flags] [file ...]
                                                                 release onto GitHub release T (made a pre-release if
                                                                 missing): merge into its releases.json (else the latest
                                                                 release's), upload the version's assets that live on T,
                                                                 the files named, then the manifest; -prune also deletes
                                                                 the dropped release's files from T
  amarit next-version [-pre dev] X                               the name of a rolling build after release X:
                                                                 0.3.9-dev.<UTC minute> after 0.3.8, which semver orders
  amarit version-code X                                          the APK versionCode of X, its pre-release part dropped:
                                                                 major*1000000 + minor*1000 + patch, 3009 for 0.3.9-dev.*

A program built with amarit updates itself: '<program> update' or '<program> --auto-update'.`)
}

func named(args []string, fn func(string) error) error {
	if len(args) != 1 {
		return errors.New("one unit name is required")
	}
	return fn(args[0])
}

func loadConfig(path string) (*amarit.ProjectConfig, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	c, err := amarit.ParseProjectConfig(raw)
	return c, raw, err
}

// stamp prints the linker flags that put amarit.json and the version into
// the binary, so a build script can do: go build -ldflags "$(amarit stamp -version X)".
func stamp(args []string) error {
	fs := flag.NewFlagSet("stamp", flag.ContinueOnError)
	config := fs.String("config", "amarit.json", "project configuration")
	version := fs.String("version", "", "version being built")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" {
		return fmt.Errorf("-version is required")
	}
	_, raw, err := loadConfig(*config)
	if err != nil {
		return err
	}
	fmt.Printf("-X github.com/sudiptadeb/amarit.stampedProject=%s -X github.com/sudiptadeb/amarit.stampedVersion=%s\n",
		base64.StdEncoding.EncodeToString(raw), strings.TrimPrefix(*version, "v"))
	return nil
}

// release hashes the built assets and writes the manifest. The asset file
// name and its download URL come from patterns in amarit.json, with
// {version}, {os}, {arch} and {asset} filled in. Without a url pattern the
// url is the asset's name, which a client resolves against the manifest's
// own location: the assets sit beside releases.json.
//
// -asset and -url are the same patterns for this run alone, for a channel
// whose asset is not named like the others (a rolling build published
// under one fixed name). The android target has no arch and carries the
// APK's versionCode, which CI knows and passes with -version-code.
func release(args []string) error {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	r := releaseFlags(fs, "releases.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, err := r.write("")
	return err
}

// releaseRun is the flags of `amarit release`, which `amarit publish` takes too.
type releaseRun struct {
	config, version, dist, channel, out, assetPattern, urlFlag *string
	expires                                                    *time.Duration
	versionCode                                                *int
	prune                                                      *bool
}

func releaseFlags(fs *flag.FlagSet, out string) *releaseRun {
	return &releaseRun{
		config:       fs.String("config", "amarit.json", "project configuration"),
		version:      fs.String("version", "", "version being released"),
		dist:         fs.String("dist", "dist", "directory holding the built assets"),
		channel:      fs.String("channel", "stable", "channel to point at this version"),
		out:          fs.String("out", out, "manifest to write"),
		expires:      fs.Duration("expires", 0, "how long the manifest stays fresh (0 = no expiry)"),
		versionCode:  fs.Int("version-code", 0, "the APK versionCode (android target)"),
		assetPattern: fs.String("asset", "", "asset name pattern for this run, over release.asset"),
		urlFlag:      fs.String("url", "", "download URL pattern for this run, over release.url"),
		prune:        fs.Bool("prune", false, "drop the release this channel pointed at until now, unless another channel still does"),
	}
}

// released is what a run wrote: the manifest, the version, each target's
// file, and the release -prune dropped ("" when none).
type released struct {
	manifest amarit.Manifest
	version  string
	files    map[string]string
	pruned   string
}

// write merges the run into the manifest at -out and writes it back.
// defaultURL is the url pattern when neither -url nor release.url gives one.
func (r *releaseRun) write(defaultURL string) (*released, error) {
	if *r.version == "" {
		return nil, fmt.Errorf("-version is required")
	}
	c, _, err := loadConfig(*r.config)
	if err != nil {
		return nil, err
	}
	if c.Release == nil || len(c.Release.Targets) == 0 {
		return nil, fmt.Errorf("%s: release.targets is empty", *r.config)
	}
	if *r.assetPattern == "" {
		*r.assetPattern = c.Release.Asset
	}
	if *r.assetPattern == "" {
		return nil, fmt.Errorf("%s: release.asset pattern is required", *r.config)
	}
	urlPattern := c.Release.URL
	if *r.urlFlag != "" {
		urlPattern = *r.urlFlag
	}
	if urlPattern == "" {
		urlPattern = defaultURL
	}
	if urlPattern == "" {
		urlPattern = "{asset}"
	}
	v := strings.TrimPrefix(*r.version, "v")
	for _, target := range c.Release.Targets {
		if target == "android" && *r.versionCode <= 0 {
			return nil, fmt.Errorf("the android target needs -version-code")
		}
	}

	m := amarit.Manifest{
		Spec:      amarit.SpecVersion,
		Project:   c.Project,
		Published: time.Now().UTC().Truncate(time.Second),
		Channels:  map[string]string{},
		Releases:  map[string]amarit.Release{},
	}
	if *r.expires > 0 {
		e := m.Published.Add(*r.expires)
		m.Expires = &e
	}
	// Keep what an existing manifest already says about other channels and
	// versions, so promoting canary never forgets stable.
	if old, err := os.ReadFile(*r.out); err == nil {
		if om, err := amarit.ParseManifest(old); err == nil && om.Project == c.Project {
			m.Channels, m.Releases, m.MinVersion = om.Channels, om.Releases, om.MinVersion
		}
	}
	if c.Release.MinVersion != "" {
		m.MinVersion = c.Release.MinVersion
	}

	assets := map[string]amarit.Asset{}
	files := map[string]string{}
	for _, target := range c.Release.Targets {
		goos, goarch, _ := strings.Cut(target, "/")
		fill := strings.NewReplacer("{version}", v, "{os}", goos, "{arch}", goarch)
		name := fill.Replace(*r.assetPattern)
		path := findAsset(*r.dist, name)
		if path == "" {
			return nil, fmt.Errorf("%s: no file named %s under %s", target, name, *r.dist)
		}
		sum, size, err := digest(path)
		if err != nil {
			return nil, err
		}
		url := strings.NewReplacer("{version}", v, "{os}", goos, "{arch}", goarch, "{asset}", name).Replace(urlPattern)
		a := amarit.Asset{URL: url, SHA256: sum, Size: size}
		if target == "android" {
			a.VersionCode = *r.versionCode
		}
		assets[target] = a
		files[target] = path
		fmt.Fprintf(os.Stderr, "  %-14s %s  %d bytes  %s\n", target, name, size, sum[:12])
	}
	m.Releases[v] = amarit.Release{Assets: assets}
	previous := m.Channels[*r.channel]
	m.Channels[*r.channel] = v
	pruned := ""
	if *r.prune && previous != "" && previous != v && !slices.Contains(slices.Collect(maps.Values(m.Channels)), previous) {
		// Only the build this channel leaves: another channel's old
		// releases are that channel's business.
		delete(m.Releases, previous)
		pruned = previous
		fmt.Fprintf(os.Stderr, "  pruned %s\n", previous)
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if _, err := amarit.ParseManifest(data); err != nil {
		return nil, fmt.Errorf("refusing to write an invalid manifest: %w", err)
	}
	if err := os.WriteFile(*r.out, append(data, '\n'), 0o644); err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "wrote %s: %s %s -> %s\n", *r.out, c.Project, *r.channel, v)
	return &released{manifest: m, version: v, files: files, pruned: pruned}, nil
}

// findAsset looks for the file directly under dist or one level down
// (build scripts like dist/<os>/<file>).
func findAsset(dist, name string) string {
	for _, pattern := range []string{filepath.Join(dist, name), filepath.Join(dist, "*", name)} {
		if hits, _ := filepath.Glob(pattern); len(hits) > 0 {
			return hits[0]
		}
	}
	return ""
}

func digest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
