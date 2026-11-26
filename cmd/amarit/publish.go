package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/debkosh/amarit"
)

// gh runs the GitHub CLI and returns its standard output; a variable so a
// test can stand in for GitHub. gh brings its own login, on a laptop or in CI.
var gh = func(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("gh %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// errNoRelease is gh's answer for a tag with no release, or a repository
// with no latest release.
func errNoRelease(err error) bool {
	return err != nil && strings.Contains(err.Error(), "release not found")
}

// publish is `amarit release` against a GitHub release with a fixed tag,
// the place a channel's manifest lives: it creates the release as a
// pre-release when it is missing, starts from the manifest there (or, the
// first time, from the latest release's), merges this version in, uploads
// the version's assets that live on the tag and the files named after the
// flags, then the manifest, and deletes the files of the release -prune
// dropped. One publish at a time: two at once would each merge into the
// manifest they fetched, and the second upload would lose the first.
func publish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	repo := fs.String("github", "", "owner/repo")
	tag := fs.String("tag", "", "the fixed tag of the release the manifest lives on")
	r := releaseFlags(fs, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" || *tag == "" {
		return errors.New("-github owner/repo and -tag are required")
	}
	if *r.out == "" {
		*r.out = filepath.Join(*r.dist, "releases.json")
	}
	if filepath.Base(*r.out) != "releases.json" {
		return fmt.Errorf("-out %s: the manifest is uploaded as releases.json, so name it that", *r.out)
	}
	extras := fs.Args()
	for _, f := range extras {
		if st, err := os.Stat(f); err != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("%s: not a file to upload", f)
		}
	}
	c, _, err := loadConfig(*r.config)
	if err != nil {
		return err
	}
	base := "https://github.com/" + *repo + "/releases/download/" + *tag + "/"

	_, onTag, err := releaseAssets(*repo, *tag)
	if errNoRelease(err) {
		fmt.Fprintf(os.Stderr, "creating the pre-release %s on %s\n", *tag, *repo)
		_, err = gh("release", "create", *tag, "--repo", *repo, "--prerelease", "--title", *tag,
			"--notes", c.Project+"'s update manifest and the channel builds it names, kept by amarit publish. Install from a numbered release.")
	}
	if err != nil {
		return err
	}
	if err := fetchManifest(*repo, *tag, onTag, *r.out, c.Project); err != nil {
		return err
	}

	run, err := r.write(base + "{asset}")
	if err != nil {
		return err
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return err
	}
	var upload []string
	for target, path := range run.files {
		ref, err := url.Parse(run.manifest.Releases[run.version].Assets[target].URL)
		if err != nil {
			return err
		}
		name, ok := strings.CutPrefix(baseURL.ResolveReference(ref).String(), base)
		if !ok {
			continue // the asset lives elsewhere, a numbered release say
		}
		if name != filepath.Base(path) {
			return fmt.Errorf("%s: the manifest names it %s on %s, but the file is %s", target, name, *tag, filepath.Base(path))
		}
		upload = append(upload, path)
	}
	upload = append(upload, extras...)
	if len(upload) > 0 {
		fmt.Fprintf(os.Stderr, "uploading %d files to %s\n", len(upload), *tag)
		if _, err := gh(append([]string{"release", "upload", *tag, "--repo", *repo, "--clobber"}, upload...)...); err != nil {
			return err
		}
	}
	// The manifest last, so no client reads one that names a file not there yet.
	if _, err := gh("release", "upload", *tag, "--repo", *repo, "--clobber", *r.out); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "published %sreleases.json: %s -> %s\n", base, *r.channel, run.version)

	if run.pruned == "" {
		return nil
	}
	var failed []string
	for _, name := range filesOf(run.pruned, onTag, run.manifest) {
		fmt.Fprintf(os.Stderr, "deleting %s from %s\n", name, *tag)
		if _, err := gh("release", "delete-asset", *tag, name, "--repo", *repo, "--yes"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			failed = append(failed, name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("published, but not deleted from %s: %s", *tag, strings.Join(failed, " "))
	}
	return nil
}

// releaseAssets is a release's tag and the names of its files; tag "" is
// the latest release.
func releaseAssets(repo, tag string) (string, []string, error) {
	args := []string{"release", "view"}
	if tag != "" {
		args = append(args, tag)
	}
	out, err := gh(append(args, "--repo", repo, "--json", "tagName,assets")...)
	if err != nil {
		return "", nil, err
	}
	var v struct {
		TagName string `json:"tagName"`
		Assets  []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return "", nil, fmt.Errorf("gh release view %s: %w", tag, err)
	}
	var names []string
	for _, a := range v.Assets {
		names = append(names, a.Name)
	}
	return v.TagName, names, nil
}

// fetchManifest puts the manifest this publish merges into at out: the
// tag's, else the latest release's, else none, the first publish of the
// project. A manifest that is there and cannot be read stops the publish
// rather than being replaced by one that forgets every other channel.
func fetchManifest(repo, tag string, onTag []string, out, project string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.Remove(out); err != nil && !os.IsNotExist(err) {
		return err
	}
	from := ""
	if slices.Contains(onTag, "releases.json") {
		from = tag
	} else {
		latest, names, err := releaseAssets(repo, "")
		if err != nil && !errNoRelease(err) {
			return err
		}
		if err == nil && slices.Contains(names, "releases.json") {
			from = latest
		}
	}
	if from == "" {
		fmt.Fprintf(os.Stderr, "no releases.json on %s or the latest release; starting a new manifest\n", repo)
		return nil
	}
	if _, err := gh("release", "download", from, "--repo", repo, "--pattern", "releases.json", "--output", out); err != nil {
		return err
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		return err
	}
	m, err := amarit.ParseManifest(raw)
	if err != nil {
		return fmt.Errorf("releases.json on %s: %w", from, err)
	}
	if m.Project != project {
		return fmt.Errorf("releases.json on %s is for %q, not %q", from, m.Project, project)
	}
	fmt.Fprintf(os.Stderr, "merging into releases.json from %s\n", from)
	return nil
}

// filesOf is the files on the tag that belong to a dropped version: those
// whose names carry it, unless the longest version a name carries is one
// the manifest still has (0.3.9-dev.<minute> is not 0.3.9's).
func filesOf(dropped string, names []string, m amarit.Manifest) []string {
	var out []string
	for _, name := range names {
		owner := ""
		for _, v := range append(slices.Collect(maps.Keys(m.Releases)), dropped) {
			if len(v) > len(owner) && carries(name, v) {
				owner = v
			}
		}
		if owner == dropped {
			out = append(out, name)
		}
	}
	return out
}

// carries reports whether name has v in it, not as part of a longer number:
// app-0.3.1.zip carries 0.3.1, app-0.3.10.zip and app-10.3.1.zip do not.
func carries(name, v string) bool {
	for i := 0; ; {
		j := strings.Index(name[i:], v)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(v)
		before := start == 0 || !(isDigit(name[start-1]) || name[start-1] == '.')
		after := end == len(name) || !isDigit(name[end])
		if before && after {
			return true
		}
		i = start + 1
	}
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
